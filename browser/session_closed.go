package browser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// SessionClose says how a session that is gone was closed, as recorded in the
// command event log.
type SessionClose struct {
	At time.Time
	// Command is "watchdog.close" when the idle lease expired, or "close"
	// when a caller closed the session.
	Command     string
	IdleTimeout string // the lease that expired (watchdog.close only)
	Reason      string // the watchdog's IdleExpiryReason (watchdog.close only)
}

// IdleExpired reports whether the idle watchdog closed the session.
func (c SessionClose) IdleExpired() bool { return c.Command == "watchdog.close" }

// SessionOpenCommand is the command that opens a session, quoted in
// missing-session errors. A host CLI that embeds this package sets its own.
var SessionOpenCommand = "snapref open"

// sessionCloseScanBytes bounds the tail of the live command log that
// LastSessionClose reads. It runs only on a missing-session failure path.
const sessionCloseScanBytes = 1 << 20

// LastSessionClose returns the newest close of session in the live command
// log, when that close is the session's newest lifecycle event. A later open
// means the close does not explain why the session is gone, so ok is false;
// it is also false when the log has no lifecycle event for the session.
func LastSessionClose(session string) (SessionClose, bool) {
	path, err := CommandEventLogPath()
	if err != nil {
		return SessionClose{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return SessionClose{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return SessionClose{}, false
	}
	start := info.Size() - sessionCloseScanBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return SessionClose{}, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return SessionClose{}, false
	}
	if start > 0 {
		// Drop the partial first line of a mid-file tail.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}

	var last *ObserverEvent
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		// Cheap filter before a full decode: most lines are other sessions.
		if !bytes.Contains(line, []byte(`"session":`)) {
			continue
		}
		var ev ObserverEvent
		if json.Unmarshal(line, &ev) != nil || ev.Session != session || ev.Command == nil {
			continue
		}
		switch ev.Command.Command {
		case "open", "close", "watchdog.close":
			last = &ev
		}
	}
	if last == nil || last.Command.Command == "open" {
		return SessionClose{}, false
	}
	c := SessionClose{At: last.Timestamp, Command: last.Command.Command}
	if v, ok := last.Command.Data["idle_timeout"].(string); ok {
		c.IdleTimeout = v
	}
	if v, ok := last.Command.Data["reason"].(string); ok {
		c.Reason = v
	}
	return c, true
}

// SessionMissingError is a request for a session that is not open. It is the
// caller's input (class usage), whatever its text says.
type SessionMissingError struct {
	Session string
	msg     string
	cause   error
}

func (e *SessionMissingError) Error() string { return e.msg }
func (e *SessionMissingError) Unwrap() error { return e.cause }

// MissingSessionError explains why session cannot be used and how to get it
// back. cause is the LoadSession error, or nil when the record exists but the
// browser process is gone.
func MissingSessionError(session, openCommand string, cause error) error {
	reopen := fmt.Sprintf("%s %s", openCommand, session)
	e := &SessionMissingError{Session: session, cause: cause}
	if c, ok := LastSessionClose(session); ok {
		at := c.At.UTC().Format("15:04:05Z")
		if c.IdleExpired() {
			e.msg = fmt.Sprintf("no such session %q: it closed at %s after %s idle (idle lease expired). Reopen: %s (--idle-timeout <duration> or --keep-alive keeps a long run open)", session, at, c.IdleTimeout, reopen)
		} else {
			e.msg = fmt.Sprintf("no such session %q: it was closed at %s. Reopen: %s", session, at, reopen)
		}
		return e
	}
	switch {
	case cause == nil:
		e.msg = fmt.Sprintf("no such session %q: its browser is not running. Reopen: %s", session, reopen)
	case errors.Is(cause, os.ErrNotExist):
		e.msg = fmt.Sprintf("no such session %q. Open it: %s", session, reopen)
	default:
		e.msg = fmt.Sprintf("no such session %q: %v", session, cause)
	}
	return e
}
