package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// DeviceEmulation is the page-level device override of a persistent session.
//
// Chrome scopes Emulation.* overrides to the DevTools session that set them.
// Every `exec` call opens and closes its own CDP connection, so Chrome drops
// the override when that call ends; the old layout survives only until the
// next navigation or resize. The session record therefore stores the override,
// and every connection the session opens re-applies it before its first
// command (dialFirstPage for raw CDP, remoteCtx for chromedp).
type DeviceEmulation struct {
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty"` // 0 = 1
	Mobile            bool    `json:"mobile,omitempty"`
	Touch             bool    `json:"touch,omitempty"`
}

// MobileDeviceScaleFactor is the default DPR for --mobile emulation.
const MobileDeviceScaleFactor = 2

// Reserved CDP message-ID ranges keep re-apply and wait calls apart from the
// caller's own sequence, which starts at 1 on every connection. Chrome parses
// the id as a 32-bit int and drops the connection on larger values, so both
// ranges stay below 2^31.
const (
	emulationCallIDBase int64 = 1_000_000_000
	waitCallIDBase      int64 = 1_500_000_000
)

// Validate rejects sizes and scale factors Chrome would refuse or misapply.
func (e DeviceEmulation) Validate() error {
	if e.Width <= 0 || e.Height <= 0 {
		return errors.New("viewport dimensions must be positive")
	}
	if e.Width > 10000 || e.Height > 10000 {
		return fmt.Errorf("viewport %dx%d exceeds 10000 px", e.Width, e.Height)
	}
	if e.DeviceScaleFactor < 0 || e.DeviceScaleFactor > 8 {
		return fmt.Errorf("device scale factor %g out of range (0-8)", e.DeviceScaleFactor)
	}
	return nil
}

// Scale returns the effective device scale factor.
func (e DeviceEmulation) Scale() float64 {
	if e.DeviceScaleFactor <= 0 {
		return 1
	}
	return e.DeviceScaleFactor
}

// String renders "390x844", plus "@2x", " mobile" and " touch" when set.
func (e DeviceEmulation) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%dx%d", e.Width, e.Height)
	if s := e.Scale(); s != 1 {
		b.WriteString("@" + strconv.FormatFloat(s, 'f', -1, 64) + "x")
	}
	if e.Mobile {
		b.WriteString(" mobile")
	}
	if e.Touch {
		b.WriteString(" touch")
	}
	return b.String()
}

func (e DeviceEmulation) metricsParams() map[string]any {
	return map[string]any{
		"width":             e.Width,
		"height":            e.Height,
		"deviceScaleFactor": e.Scale(),
		"mobile":            e.Mobile,
		"scale":             1,
		"screenWidth":       e.Width,
		"screenHeight":      e.Height,
	}
}

func touchParams(enabled bool) map[string]any {
	if !enabled {
		return map[string]any{"enabled": false}
	}
	return map[string]any{"enabled": true, "maxTouchPoints": 5}
}

// applyEmulationOnConn sets (e != nil) or clears (e == nil) the device
// override on one raw CDP connection. Touch is always written so a previous
// touch override never leaks into a non-touch emulation.
func applyEmulationOnConn(ctx context.Context, conn cdpTransport, e *DeviceEmulation) error {
	id := emulationCallIDBase
	next := func() int64 { id++; return id }
	if e == nil {
		if _, err := cdpCall(ctx, conn, next(), "Emulation.clearDeviceMetricsOverride", nil); err != nil {
			return err
		}
		_, err := cdpCall(ctx, conn, next(), "Emulation.setTouchEmulationEnabled", touchParams(false))
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if _, err := cdpCall(ctx, conn, next(), "Emulation.setDeviceMetricsOverride", e.metricsParams()); err != nil {
		return err
	}
	_, err := cdpCall(ctx, conn, next(), "Emulation.setTouchEmulationEnabled", touchParams(e.Touch))
	return err
}

// emulationActions is the chromedp form of applyEmulationOnConn.
func emulationActions(e *DeviceEmulation) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if e == nil {
			if err := emulation.ClearDeviceMetricsOverride().Do(ctx); err != nil {
				return err
			}
			return emulation.SetTouchEmulationEnabled(false).Do(ctx)
		}
		if err := e.Validate(); err != nil {
			return err
		}
		if err := emulation.SetDeviceMetricsOverride(int64(e.Width), int64(e.Height), e.Scale(), e.Mobile).
			WithScale(1).
			WithScreenWidth(int64(e.Width)).
			WithScreenHeight(int64(e.Height)).
			Do(ctx); err != nil {
			return err
		}
		touch := emulation.SetTouchEmulationEnabled(e.Touch)
		if e.Touch {
			touch = touch.WithMaxTouchPoints(5)
		}
		return touch.Do(ctx)
	})
}

// SetSessionEmulation records (e != nil) or removes (e == nil) the persisted
// device emulation of a saved session under its lifecycle lock.
func SetSessionEmulation(name string, e *DeviceEmulation) (*Session, error) {
	if e != nil {
		if err := e.Validate(); err != nil {
			return nil, err
		}
	}
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s, err := LoadSession(name)
	if err != nil {
		return nil, err
	}
	if e != nil {
		copied := *e
		s.Emulation = &copied
	} else {
		s.Emulation = nil
	}
	if err := SaveSession(s); err != nil {
		return nil, err
	}
	return s, nil
}

// RemoteSetDeviceEmulation applies e to the session's page and persists it, so
// later navigations, evals and screenshots in any process keep it. A nil e
// clears the override and the persisted record.
func RemoteSetDeviceEmulation(ctx context.Context, s *Session, e *DeviceEmulation, timeout time.Duration) error {
	if e != nil {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if err := rawApplyEmulation(ctx, s, e, timeout); err != nil {
		return err
	}
	if s.Name != "" {
		saved, err := SetSessionEmulation(s.Name, e)
		if err != nil {
			return fmt.Errorf("persist viewport: %w", err)
		}
		s.Emulation = saved.Emulation
	} else if e != nil {
		copied := *e
		s.Emulation = &copied
	} else {
		s.Emulation = nil
	}
	return nil
}

// rawApplyEmulation sets or clears the override on the page for this
// connection only, with no persistence. dialFirstPage first re-applies the
// persisted emulation, so the explicit call wins.
func rawApplyEmulation(ctx context.Context, s *Session, e *DeviceEmulation, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialFirstPage(cctx, s)
	if err != nil {
		return err
	}
	defer conn.Close()
	return applyEmulationOnConn(cctx, conn, e)
}

// restoreSessionEmulation returns the page to the session's persisted state
// after a one-shot override: the persisted emulation, or none.
func restoreSessionEmulation(ctx context.Context, s *Session, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return rawApplyEmulation(ctx, s, s.Emulation, timeout)
}

// restoreAfterOneShot restores the persisted emulation after a failed one-shot
// step and returns cause (the restore error only when cause is nil).
func (s *Session) restoreAfterOneShot(parent context.Context, oneShot *DeviceEmulation, cause error) error {
	if oneShot == nil {
		return cause
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := restoreSessionEmulation(ctx, s, 10*time.Second); err != nil && cause == nil {
		return err
	}
	return cause
}

// waitVisibleOnConn polls until selector matches a rendered, visible element
// on this connection, or ctx ends.
func waitVisibleOnConn(ctx context.Context, conn cdpTransport, selector string) error {
	sel, err := json.Marshal(selector)
	if err != nil {
		return err
	}
	js := `(() => { const el = document.querySelector(` + string(sel) + `); if (!el) return false;
  const r = el.getBoundingClientRect(); const cs = getComputedStyle(el);
  return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; })()`
	id := waitCallIDBase
	for {
		id++
		res, err := cdpCall(ctx, conn, id, "Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true})
		if err != nil {
			return fmt.Errorf("wait for %s: %w", selector, err)
		}
		var r struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text string `json:"text"`
			} `json:"exceptionDetails"`
		}
		if err := json.Unmarshal(res, &r); err != nil {
			return fmt.Errorf("wait for %s: decode: %w", selector, err)
		}
		if r.ExceptionDetails != nil {
			return fmt.Errorf("wait for %s: %s", selector, r.ExceptionDetails.Text)
		}
		if r.Result.Value {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for visible %s: %w", selector, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
