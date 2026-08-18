package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"time"
)

// driverDefaultTimeout bounds a single driver operation when the caller's ctx
// carries no deadline.
const driverDefaultTimeout = 30 * time.Second

// driverTimeout derives a per-operation timeout from the caller's ctx deadline,
// so a browserscript Step.Timeout (which wraps the ctx) can EXTEND a wait past
// the default — not just shorten it. No deadline → the default.
func driverTimeout(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			return d
		}
	}
	return driverDefaultTimeout
}

// SessionInfo is a read-only view of one open browser session.
type SessionInfo struct {
	Name            string              `json:"name"`
	Port            int                 `json:"port"`
	Alive           bool                `json:"alive"`
	Headful         bool                `json:"headful"`
	LifecyclePolicy LifecyclePolicy     `json:"lifecycle_policy,omitempty"`
	IdleTimeout     string              `json:"idle_timeout,omitempty"`
	LastActivityAt  time.Time           `json:"last_activity_at,omitempty"`
	PurgeOnExpiry   bool                `json:"purge_profile_on_expiry,omitempty"`
	RuntimeKind     RuntimeKind         `json:"runtime_kind,omitempty"`
	RuntimeVersion  string              `json:"runtime_version,omitempty"`
	RuntimeManaged  bool                `json:"runtime_managed,omitempty"`
	RuntimeCaps     RuntimeCapabilities `json:"runtime_capabilities,omitempty"`
}

// NavigateOptions tweaks a persistent-session navigation. The zero value keeps
// the historical Page.navigate behavior.
type NavigateOptions struct {
	// Reload performs a Page.reload after the navigate settles. This gives SPAs
	// with hash routers a deterministic remount path after fragment changes.
	Reload bool `json:"reload,omitempty"`
	// BypassCache disables the page HTTP cache before navigating/reloading.
	BypassCache bool `json:"bypass_cache,omitempty"`
}

// Driver is the actuation + read surface over an already-open, saved session.
// It is an interface so callers (the MCP server's gating/audit logic, the
// browserscript runner) are unit-testable with a fake, without a real browser.
//
// A Driver performs zero launching: the operator opens sessions with
// `snapref open`; callers only drive existing ones.
type Driver interface {
	List() ([]SessionInfo, error)
	Navigate(ctx context.Context, session, url string, opts ...NavigateOptions) error
	Click(ctx context.Context, session, selector string) error
	ClickText(ctx context.Context, session, text string) error
	ClickXPath(ctx context.Context, session, xpath string) error
	Type(ctx context.Context, session, selector, text string) error
	Press(ctx context.Context, session, key string) error
	WaitVisible(ctx context.Context, session, selector string) error
	WaitText(ctx context.Context, session, text string) error
	WaitXPath(ctx context.Context, session, xpath string) error
	Text(ctx context.Context, session, selector string) (string, error)
	Eval(ctx context.Context, session, js string) (string, error)
	Fetch(ctx context.Context, session, url string) (string, error)
	Screenshot(ctx context.Context, session, path string) (string, error)
	Console(ctx context.Context, session string, opts ConsoleCaptureOptions) ([]ConsoleEntry, error)
	// Snapshot returns the read-only interactive-element inventory of the
	// session's current page — the agent "eyes on the page" discovery surface.
	Snapshot(ctx context.Context, session string, opts SnapshotOptions) (*PageSnapshot, error)
	// Viewport applies page-level device metrics (width×height) so responsive
	// layouts and screenshots can be driven without reopening the session.
	Viewport(ctx context.Context, session string, width, height int) error
	// ResolveTarget returns a CSS selector for either a selector input or a
	// revision-bound executable snapshot ref.
	ResolveTarget(ctx context.Context, session, target string) (string, error)
}

// ScreenshotArtifact is the durable result of one PNG capture. Dimensions are
// CSS pixels, independent of the host display scale.
type ScreenshotArtifact struct {
	Path     string `json:"path"`
	WidthPX  int    `json:"width_px"`
	HeightPX int    `json:"height_px"`
}

// ScreenshotCaptureDriver is the optional capture-mode extension implemented
// by the production driver. Driver keeps its original method for compatibility;
// callers that need an explicit viewport/full-page contract can require this
// interface without breaking existing custom drivers.
type ScreenshotCaptureDriver interface {
	ScreenshotWithCapture(ctx context.Context, session, path string, captureBeyondViewport bool) (ScreenshotArtifact, error)
}

// remoteDriver wraps the remote primitives against an already-open, saved
// session. It performs zero launching.
type remoteDriver struct{}

// NewRemoteDriver returns the production driver backed by the remote session
// primitives in this package.
func NewRemoteDriver() Driver { return remoteDriver{} }

func (remoteDriver) session(name string) (*Session, error) {
	if name == "" {
		return nil, fmt.Errorf("session name is required")
	}
	s, err := LoadSession(name)
	if err != nil {
		return nil, fmt.Errorf("no such session %q (open one with `snapref open %s`)", name, name)
	}
	if !s.IsAlive() {
		return nil, fmt.Errorf("session %q is not running", name)
	}
	if err := TouchSession(name); err != nil {
		return nil, fmt.Errorf("renew session %q idle lease: %w", name, err)
	}
	return s, nil
}

func (remoteDriver) List() ([]SessionInfo, error) {
	sessions, err := ListSessions()
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(sessions))
	for i := range sessions {
		s := sessions[i]
		out = append(out, SessionInfo{
			Name:            s.Name,
			Port:            s.Port,
			Alive:           s.IsAlive(),
			Headful:         s.Headful,
			LifecyclePolicy: s.EffectiveLifecyclePolicy(),
			IdleTimeout:     s.IdleTimeout,
			LastActivityAt:  s.LastActivityAt,
			PurgeOnExpiry:   s.PurgeOnExpiry,
			RuntimeKind:     s.RuntimeKind,
			RuntimeVersion:  s.RuntimeVersion,
			RuntimeManaged:  s.RuntimeManaged,
			RuntimeCaps:     s.RuntimeCaps,
		})
	}
	return out, nil
}

func withRemoteSessionResult[T any](d remoteDriver, name string, operation func(*Session) (T, error)) (T, error) {
	var zero T
	s, err := d.session(name)
	if err != nil {
		return zero, err
	}
	result, operationErr := operation(s)
	touchErr := TouchSession(name)
	if touchErr != nil {
		leaseErr := fmt.Errorf("renew session %q idle lease after operation: %w", name, touchErr)
		if operationErr != nil {
			return result, errors.Join(operationErr, leaseErr)
		}
		return result, leaseErr
	}
	return result, operationErr
}

func withRemoteSession(d remoteDriver, name string, operation func(*Session) error) error {
	_, err := withRemoteSessionResult(d, name, func(s *Session) (struct{}, error) {
		return struct{}{}, operation(s)
	})
	return err
}

func (d remoteDriver) Navigate(ctx context.Context, session, url string, opts ...NavigateOptions) error {
	return withRemoteSession(d, session, func(s *Session) error {
		return RemoteNavigate(ctx, s, url, driverTimeout(ctx), opts...)
	})
}

func (d remoteDriver) Click(ctx context.Context, session, selector string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		resolved, err := ResolveTarget(ctx, s, selector, driverTimeout(ctx))
		if err != nil {
			return err
		}
		return RemoteClick(ctx, s, resolved, driverTimeout(ctx))
	})
}

func (d remoteDriver) ClickText(ctx context.Context, session, text string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		return RemoteClickText(ctx, s, text, driverTimeout(ctx))
	})
}

func (d remoteDriver) ClickXPath(ctx context.Context, session, xpath string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		return RemoteClickXPath(ctx, s, xpath, driverTimeout(ctx))
	})
}

func (d remoteDriver) Type(ctx context.Context, session, selector, text string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		resolved, err := ResolveTarget(ctx, s, selector, driverTimeout(ctx))
		if err != nil {
			return err
		}
		return RemoteType(ctx, s, resolved, text, driverTimeout(ctx))
	})
}

func (d remoteDriver) Press(ctx context.Context, session, key string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		return RemotePress(ctx, s, key, driverTimeout(ctx))
	})
}

func (d remoteDriver) Viewport(ctx context.Context, session string, width, height int) error {
	return withRemoteSession(d, session, func(s *Session) error {
		return RemoteSetViewport(ctx, s, Viewport{Width: width, Height: height}, driverTimeout(ctx))
	})
}

func (d remoteDriver) WaitVisible(ctx context.Context, session, selector string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		resolved, err := ResolveTarget(ctx, s, selector, driverTimeout(ctx))
		if err != nil {
			return err
		}
		return RemoteWaitVisible(ctx, s, resolved, driverTimeout(ctx))
	})
}

func (d remoteDriver) WaitText(ctx context.Context, session, text string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		return RemoteWaitText(ctx, s, text, driverTimeout(ctx))
	})
}

func (d remoteDriver) WaitXPath(ctx context.Context, session, xpath string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		return RemoteWaitXPath(ctx, s, xpath, driverTimeout(ctx))
	})
}

func (d remoteDriver) Text(ctx context.Context, session, selector string) (string, error) {
	return withRemoteSessionResult(d, session, func(s *Session) (string, error) {
		resolved, err := ResolveTarget(ctx, s, selector, driverTimeout(ctx))
		if err != nil {
			return "", err
		}
		return RemoteText(ctx, s, resolved, driverTimeout(ctx))
	})
}

func (d remoteDriver) ResolveTarget(ctx context.Context, session, target string) (string, error) {
	return withRemoteSessionResult(d, session, func(s *Session) (string, error) {
		return ResolveTarget(ctx, s, target, driverTimeout(ctx))
	})
}

func (d remoteDriver) Eval(ctx context.Context, session, js string) (string, error) {
	return withRemoteSessionResult(d, session, func(s *Session) (string, error) {
		var out any
		if err := RemoteEval(ctx, s, "", js, &out, driverTimeout(ctx)); err != nil {
			return "", err
		}
		b, err := json.Marshal(out)
		if err != nil {
			return fmt.Sprintf("%v", out), nil
		}
		return string(b), nil
	})
}

func (d remoteDriver) Fetch(ctx context.Context, session, url string) (string, error) {
	return withRemoteSessionResult(d, session, func(s *Session) (string, error) {
		return RemoteFetchHTML(ctx, s, url, driverTimeout(ctx))
	})
}

func (d remoteDriver) Screenshot(ctx context.Context, session, path string) (string, error) {
	artifact, err := d.ScreenshotWithCapture(ctx, session, path, true)
	return artifact.Path, err
}

func (d remoteDriver) ScreenshotWithCapture(ctx context.Context, session, path string, captureBeyondViewport bool) (ScreenshotArtifact, error) {
	return withRemoteSessionResult(d, session, func(s *Session) (ScreenshotArtifact, error) {
		written, err := RemoteSaveScreenshotCurrentWithCaptureTimeout(s, path, captureBeyondViewport, driverTimeout(ctx))
		if err != nil {
			return ScreenshotArtifact{}, err
		}
		f, err := os.Open(written)
		if err != nil {
			return ScreenshotArtifact{}, fmt.Errorf("open captured screenshot: %w", err)
		}
		defer f.Close()
		cfg, format, err := image.DecodeConfig(f)
		if err != nil || format != "png" {
			return ScreenshotArtifact{}, fmt.Errorf("decode captured screenshot %s: format=%q err=%v", written, format, err)
		}
		return ScreenshotArtifact{Path: written, WidthPX: cfg.Width, HeightPX: cfg.Height}, nil
	})
}

func (d remoteDriver) Console(ctx context.Context, session string, opts ConsoleCaptureOptions) ([]ConsoleEntry, error) {
	return withRemoteSessionResult(d, session, func(s *Session) ([]ConsoleEntry, error) {
		return RemoteConsoleCapture(ctx, s, opts)
	})
}

func (d remoteDriver) Snapshot(ctx context.Context, session string, opts SnapshotOptions) (*PageSnapshot, error) {
	return withRemoteSessionResult(d, session, func(s *Session) (*PageSnapshot, error) {
		return RemoteSnapshot(ctx, s, opts, driverTimeout(ctx))
	})
}
