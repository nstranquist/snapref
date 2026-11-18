package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Chrome builds a clipped or full-page capture from the device metrics of
// the DevTools session that sends Page.captureScreenshot, and afterwards it
// restores that session's own metrics on the page. While a holder owns the
// viewport, an exec connection has no override of its own: its capture comes
// out at DPR 1, and the restore leaves the whole page at DPR 1. So while a
// holder serves the session, a plain capture runs on the holder's connection,
// the one that owns the override. The image is in device pixels and the page
// keeps its viewport.
//
// The protocol is files in one directory per session:
//
//	<id>.req.json   written atomically by the client
//	<id>.taken      the holder's claim (renamed from the request)
//	<id>.png        the image, written before the result
//	<id>.res.json   the outcome; the client reads it, then removes all three

const (
	holderCaptureClaimWait = 3 * time.Second
	holderCapturePoll      = 20 * time.Millisecond
	holderCaptureStaleAge  = 2 * time.Minute
)

type holderCaptureRequest struct {
	ID             string    `json:"id"`
	BeyondViewport bool      `json:"beyond_viewport"`
	CreatedAt      time.Time `json:"created_at"`
}

type holderCaptureResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

var errHolderCaptureUnclaimed = errors.New("the viewport holder did not take the capture request")

func holderCaptureDir(name string) (string, error) {
	if err := ValidateSessionName(name); err != nil {
		return "", err
	}
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "holder-capture", name), nil
}

func newHolderCaptureID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// holderCapture asks the live holder of s to capture the page on its own
// connection and waits for the image. It fails closed: an unclaimed request
// or a holder error is an error, never a capture at the wrong scale.
func holderCapture(ctx context.Context, s *Session, beyondViewport bool) ([]byte, error) {
	dir, err := holderCaptureDir(s.Name)
	if err != nil {
		return nil, err
	}
	id, err := newHolderCaptureID()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(dir, id)
	req := holderCaptureRequest{ID: id, BeyondViewport: beyondViewport, CreatedAt: time.Now().UTC()}
	if err := writeJSONAtomic(base+".req.json", req, 0o600); err != nil {
		return nil, fmt.Errorf("write capture request: %w", err)
	}
	defer func() {
		for _, suffix := range []string{".req.json", ".taken", ".png", ".res.json"} {
			_ = os.Remove(base + suffix)
		}
	}()
	claimBy := time.Now().Add(holderCaptureClaimWait)
	for {
		if raw, err := os.ReadFile(base + ".res.json"); err == nil {
			var res holderCaptureResult
			if err := json.Unmarshal(raw, &res); err != nil {
				return nil, fmt.Errorf("read capture result: %w", err)
			}
			if !res.OK {
				return nil, fmt.Errorf("viewport holder capture: %s", res.Error)
			}
			return os.ReadFile(base + ".png")
		}
		if time.Now().After(claimBy) {
			if _, err := os.Stat(base + ".req.json"); err == nil {
				// Take the request back so a late holder cannot serve it.
				if os.Remove(base+".req.json") == nil {
					return nil, errHolderCaptureUnclaimed
				}
			}
		}
		if err := sleepCtx(ctx, holderCapturePoll); err != nil {
			return nil, fmt.Errorf("capture screenshot via viewport holder: %w", err)
		}
	}
}

// serveHolderCaptures runs every pending capture request of one session on
// the holder's connection. It removes requests older than
// holderCaptureStaleAge that no client waits for any more.
func serveHolderCaptures(ctx context.Context, name string, conn cdpTransport) {
	dir, err := holderCaptureDir(name)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		file := entry.Name()
		if !strings.HasSuffix(file, ".req.json") {
			continue
		}
		base := filepath.Join(dir, strings.TrimSuffix(file, ".req.json"))
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > holderCaptureStaleAge {
			_ = os.Remove(base + ".req.json")
			continue
		}
		// The rename is the claim: the client cannot withdraw a taken request.
		if err := os.Rename(base+".req.json", base+".taken"); err != nil {
			continue
		}
		res := holderCaptureResult{ID: filepath.Base(base)}
		raw, err := os.ReadFile(base + ".taken")
		var req holderCaptureRequest
		if err == nil {
			err = json.Unmarshal(raw, &req)
		}
		var png []byte
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			png, err = captureCurrentScreenshot(cctx, conn, req.BeyondViewport)
			cancel()
		}
		if err == nil {
			err = writeFileAtomic(base+".png", png, 0o600)
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
		_ = writeJSONAtomic(base+".res.json", res, 0o600)
	}
}

func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-capture-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
