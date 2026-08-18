package skills

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nstranquist/snapref/browser"
)

// StatelessPage is a Page that launches a fresh chromium per call.
// One Page can drive multiple skill runs by carrying the LaunchOptions.
//
// The intended pattern is:
//
//	page := skills.NewStatelessPage(opts)
//	for _, url := range urls {
//	    if err := page.Navigate(ctx, url); err != nil { ... }
//	    report, _ := skills.CheckSelectors(ctx, page, domain, url)
//	}
//
// Internally each Navigate+Eval pair shares one Chromium launch (we
// remember the most recent URL so subsequent Eval calls hit the same
// page). For cross-URL skills, call Navigate again.
type StatelessPage struct {
	Opts    browser.LaunchOptions
	currURL string
}

// NewStatelessPage returns a Page using stateless one-shot ops. The
// underlying browser launches per Navigate and stays warm for adjacent
// Eval calls (chromedp manages the lifetime; here we remember the URL
// so Eval-after-Navigate stays on the same page).
//
// For batch work you want NewSessionPage(session) instead — much faster
// since chromium stays open between URLs.
func NewStatelessPage(opts browser.LaunchOptions) *StatelessPage {
	return &StatelessPage{Opts: opts}
}

func (p *StatelessPage) Navigate(ctx context.Context, url string) error {
	p.currURL = url
	return nil
}

func (p *StatelessPage) Eval(ctx context.Context, js string, out any) error {
	return browser.Eval(ctx, p.Opts, p.currURL, js, out)
}

// SessionPage is a Page that reuses an open browser.Session via CDP.
// Persistent across calls — much faster for batch use cases.
//
// Holds a single chromedp tab context across Navigate + Eval calls so
// successive operations hit the same page. Without this, each
// chromedp.NewRemoteAllocator + NewContext lands in a fresh blank tab
// and selectors never match.
//
// Call Close when done with the page so the chromedp context releases.
type SessionPage struct {
	Session *browser.Session
	Timeout time.Duration

	currURL   string
	chromeCtx chromedpContext // lazy-initialized
}

// chromedpContext is the bag of cancel funcs we own for one SessionPage.
type chromedpContext struct {
	taskCtx     context.Context
	cancelAlloc context.CancelFunc
	cancelTask  context.CancelFunc
	ready       bool
}

func NewSessionPage(s *browser.Session) *SessionPage {
	return &SessionPage{Session: s, Timeout: 60 * time.Second}
}

// Close releases the underlying chromedp context. Safe to call multiple
// times. Defer this in callers that own the SessionPage's lifetime.
func (p *SessionPage) Close() {
	if p.chromeCtx.ready {
		p.chromeCtx.cancelTask()
		p.chromeCtx.cancelAlloc()
		p.chromeCtx.ready = false
	}
}

func (p *SessionPage) ensureCtx() error {
	if p.chromeCtx.ready {
		return nil
	}
	taskCtx, cancelAlloc, cancelTask, err := browser.NewRemoteContext(context.Background(), p.Session)
	if err != nil {
		return err
	}
	p.chromeCtx = chromedpContext{
		taskCtx:     taskCtx,
		cancelAlloc: cancelAlloc,
		cancelTask:  cancelTask,
		ready:       true,
	}
	return nil
}

func (p *SessionPage) renewIdleLease() error {
	if p.Session == nil {
		return fmt.Errorf("browser session is required")
	}
	if p.Session.IdleTimeout == "" {
		return nil
	}
	if err := browser.TouchSession(p.Session.Name); err != nil {
		return fmt.Errorf("renew session %q idle lease: %w", p.Session.Name, err)
	}
	return nil
}

func (p *SessionPage) withIdleLease(operation func() error) (err error) {
	if err := p.renewIdleLease(); err != nil {
		return err
	}
	defer func() {
		if renewErr := p.renewIdleLease(); renewErr != nil {
			err = errors.Join(err, renewErr)
		}
	}()
	return operation()
}

// Navigate moves the persistent tab to url and waits for body.
func (p *SessionPage) Navigate(parentCtx context.Context, url string) error {
	return p.withIdleLease(func() error {
		p.currURL = url
		if err := p.ensureCtx(); err != nil {
			return err
		}
		return browser.NavigateOn(parentCtx, p.chromeCtx.taskCtx, url, p.Timeout)
	})
}

// Eval evaluates js in the persistent tab.
func (p *SessionPage) Eval(parentCtx context.Context, js string, out any) error {
	return p.withIdleLease(func() error {
		if err := p.ensureCtx(); err != nil {
			return err
		}
		return browser.EvaluateOn(parentCtx, p.chromeCtx.taskCtx, js, out, p.Timeout)
	})
}
