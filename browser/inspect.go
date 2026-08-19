package browser

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TargetSnapshot is a read-only view of one Chrome DevTools Protocol target.
type TargetSnapshot struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url,omitempty"`
	Title                string `json:"title,omitempty"`
	DevToolsFrontendURL  string `json:"devtools_frontend_url,omitempty"`
	WebSocketDebuggerURL string `json:"web_socket_debugger_url,omitempty"`
}

// ThumbnailSnapshot describes an optional inspect-time page thumbnail.
type ThumbnailSnapshot struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	Format string `json:"format"`
}

// InspectOptions controls optional expensive inspect fields.
type InspectOptions struct {
	Thumbnail     bool
	ThumbnailPath string
}

// CurrentPageInfo returns the first page target's URL and title, if any.
func CurrentPageInfo(s *Session) (pageURL, title string) {
	if s == nil {
		return "", ""
	}
	entries, ok := fetchTargetList(s.Port)
	if !ok {
		return "", ""
	}
	for _, entry := range entries {
		if entry.Type == "page" {
			return entry.URL, entry.Title
		}
	}
	return "", ""
}

// InspectReport is the full read-only observer view for one session.
type InspectReport struct {
	OK        bool               `json:"ok"`
	Session   *SessionStatus     `json:"session"`
	Current   *TargetSnapshot    `json:"current,omitempty"`
	Targets   []TargetSnapshot   `json:"targets"`
	Thumbnail *ThumbnailSnapshot `json:"thumbnail,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// ObserverCommand is the sanitized command record consumed by observers.
type ObserverCommand struct {
	Command    string         `json:"command"`
	Operation  string         `json:"operation,omitempty"`
	ErrorClass ErrorClass     `json:"error_class,omitempty"`
	ExitCode   int            `json:"exit_code"`
	OK         bool           `json:"ok"`
	DurationMS int64          `json:"duration_ms"`
	Data       map[string]any `json:"data,omitempty"`
}

// ObserverEvent is the JSONL unit emitted by observe.
type ObserverEvent struct {
	SchemaVersion int              `json:"schema_version,omitempty"`
	BuildVersion  string           `json:"build_version,omitempty"`
	Timestamp     time.Time        `json:"timestamp"`
	Kind          string           `json:"kind"`
	Session       string           `json:"session,omitempty"`
	Status        *SessionStatus   `json:"status,omitempty"`
	Target        *TargetSnapshot  `json:"target,omitempty"`
	Command       *ObserverCommand `json:"command,omitempty"`
	Error         string           `json:"error,omitempty"`
	ErrorClass    ErrorClass       `json:"error_class,omitempty"`
}

// Event kinds appended to the command event log. KindCommand is the original
// ad-hoc `exec` record; KindScript / KindStep are emitted by the browserscript
// engine so script runs ride the same observer timeline.
const (
	KindCommand = "browser.command"
	KindScript  = "browser.script"
	KindStep    = "browser.step"
)

// InspectSession returns a read-only view of an existing session. It does not
// launch Chrome or mutate the page unless Thumbnail is requested.
func InspectSession(name string, opts InspectOptions) (*InspectReport, error) {
	st, err := Status(name)
	if err != nil {
		return nil, err
	}
	report := &InspectReport{
		OK:      st.Reachable,
		Session: st,
		Targets: []TargetSnapshot{},
		Error:   st.Error,
	}
	if !st.Reachable {
		return report, nil
	}

	targets, err := SessionTargets(&st.Session)
	if err != nil {
		report.OK = false
		report.Error = err.Error()
		return report, nil
	}
	report.Targets = targets
	for i := range targets {
		if targets[i].Type == "page" {
			report.Current = &targets[i]
			break
		}
	}

	if opts.Thumbnail {
		path, err := RemoteSaveScreenshotCurrent(&st.Session, opts.ThumbnailPath)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		report.Thumbnail = &ThumbnailSnapshot{
			Path:   path,
			Bytes:  int(info.Size()),
			Format: "png",
		}
	}

	return report, nil
}

// SessionTargets reads the DevTools target list for an already-running session.
func SessionTargets(s *Session) ([]TargetSnapshot, error) {
	entries, ok := fetchTargetList(s.Port)
	if !ok {
		return nil, fmt.Errorf("session %q targets are not reachable on port %d", s.Name, s.Port)
	}
	out := make([]TargetSnapshot, 0, len(entries))
	for _, entry := range entries {
		out = append(out, TargetSnapshot{
			ID:                   entry.ID,
			Type:                 entry.Type,
			URL:                  entry.URL,
			Title:                entry.Title,
			DevToolsFrontendURL:  normalizeDevToolsURL(s.Port, entry.DevToolsFrontendURL),
			WebSocketDebuggerURL: entry.WebSocketDebuggerURL,
		})
	}
	return out, nil
}

func normalizeDevToolsURL(port int, raw string) string {
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		return fmt.Sprintf("http://127.0.0.1:%d%s", port, raw)
	}
	return raw
}

// AppendCommandEvent records a sanitized command event for future observers.
func AppendCommandEvent(session string, cmd ObserverCommand) error {
	return AppendEvent(ObserverEvent{
		Timestamp:  time.Now().UTC(),
		Kind:       KindCommand,
		Session:    session,
		Command:    &cmd,
		ErrorClass: cmd.ErrorClass,
	})
}

// AppendEvent appends a pre-built event to the command event log. Callers that
// want a non-command kind (browser.script / browser.step) build the event and
// pass it here. A zero Timestamp is stamped with the current UTC time.
func AppendEvent(event ObserverEvent) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = BrowserTelemetrySchemaVersion
	}
	if event.BuildVersion == "" {
		event.BuildVersion = BrowserBuildVersion()
	}
	if event.Command != nil {
		if event.Command.Operation == "" {
			event.Command.Operation = telemetryOperation(event.Command.Command, event.Command.Data)
		}
		if event.Command.ErrorClass == "" {
			event.Command.ErrorClass = event.ErrorClass
		}
	}
	if event.ErrorClass == "" && event.Error != "" {
		event.ErrorClass = ClassifyError(errors.New(event.Error))
		if event.Command != nil && event.Command.ErrorClass == "" {
			event.Command.ErrorClass = event.ErrorClass
		}
	}
	path, err := CommandEventLogPath()
	if err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return appendAndRotateJSONL(path, body, 0o600)
}

// ScriptRunsDir is the directory holding per-run browserscript manifests
// (~/.snapref/browser/scripts).
func ScriptRunsDir() (string, error) {
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "scripts"), nil
}

// CommandEventLogPath is the per-browser-home JSONL command event log.
func CommandEventLogPath() (string, error) {
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "events.jsonl"), nil
}

// CommandEventLogSize returns the current event log size, or zero if absent.
func CommandEventLogSize() (int64, error) {
	path, err := CommandEventLogPath()
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// liveCommandLogCursor is the byte size of the live events.jsonl file.
// Observe starts here so a rotate (empty hot file) does not replay gzip history.
func liveCommandLogCursor(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// ReadCommandEventsAfter tails the live command log after byte offset.
// Rotated gzip siblings are not part of the tail. Observe starts at
// CommandEventLogSize() (end of live); after a rotate that size is 0 and
// this must return 0 new events, not the archived family.
func ReadCommandEventsAfter(offset int64) ([]ObserverEvent, int64, error) {
	path, err := CommandEventLogPath()
	if err != nil {
		return nil, offset, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, offset, nil
	}
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if offset > info.Size() {
		// Live file shrank (rotate). Resume at the start of the new live
		// window — never family-replay archives from a tail cursor.
		offset = 0
	}
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	next := offset + int64(len(data))
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	var events []ObserverEvent
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event ObserverEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return events, next, fmt.Errorf("parse command event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return events, next, err
	}
	return events, next, nil
}

// ReadCommandEventFamily replays live + gzip rotations once. The returned
// cursor is the live file size so ReadCommandEventsAfter(cursor) (and
// observe after --replay-commands) does not re-emit those family rows.
func ReadCommandEventFamily() ([]ObserverEvent, int64, error) {
	path, err := CommandEventLogPath()
	if err != nil {
		return nil, 0, err
	}
	var events []ObserverEvent
	if err := forEachJSONLFamilyLine(path, func(raw []byte) error {
		var event ObserverEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("parse command event: %w", err)
		}
		events = append(events, event)
		return nil
	}); err != nil {
		return nil, 0, err
	}
	return events, liveCommandLogCursor(path), nil
}

// ObserveSnapshot emits current command, session, and target events. It is
// read-only: no browser process is launched and no page interaction occurs.
func ObserveSnapshot(sessionFilter string, commandOffset int64) ([]ObserverEvent, int64, error) {
	events, nextOffset, err := ReadCommandEventsAfter(commandOffset)
	if err != nil {
		return nil, commandOffset, err
	}
	if sessionFilter != "" {
		filtered := events[:0]
		for _, event := range events {
			if event.Session == sessionFilter {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}
	sessions, err := ListSessions()
	if err != nil {
		return nil, nextOffset, err
	}
	now := time.Now().UTC()
	for i := range sessions {
		s := sessions[i]
		if sessionFilter != "" && s.Name != sessionFilter {
			continue
		}
		st, err := Status(s.Name)
		if err != nil {
			events = append(events, ObserverEvent{
				Timestamp: now,
				Kind:      "browser.session",
				Session:   s.Name,
				Error:     err.Error(),
			})
			continue
		}
		events = append(events, ObserverEvent{
			Timestamp: now,
			Kind:      "browser.session",
			Session:   s.Name,
			Status:    st,
			Error:     st.Error,
		})
		if !st.Reachable {
			continue
		}
		targets, err := SessionTargets(&st.Session)
		if err != nil {
			events = append(events, ObserverEvent{
				Timestamp: now,
				Kind:      "browser.target",
				Session:   s.Name,
				Error:     err.Error(),
			})
			continue
		}
		for i := range targets {
			target := targets[i]
			events = append(events, ObserverEvent{
				Timestamp: now,
				Kind:      "browser.target",
				Session:   s.Name,
				Target:    &target,
			})
		}
	}
	return events, nextOffset, nil
}
