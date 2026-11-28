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
	// ColorScheme emulates the prefers-color-scheme media feature: "light",
	// "dark", or "" for the browser's own value.
	ColorScheme string `json:"color_scheme,omitempty"`
}

// Color schemes accepted by DeviceEmulation.ColorScheme.
const (
	ColorSchemeLight = "light"
	ColorSchemeDark  = "dark"
)

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
	return validColorScheme(e.ColorScheme)
}

// validColorScheme accepts "" (no override), light or dark.
func validColorScheme(scheme string) error {
	switch scheme {
	case "", ColorSchemeLight, ColorSchemeDark:
		return nil
	}
	return fmt.Errorf("color scheme %q: use light or dark", scheme)
}

// Scale returns the effective device scale factor.
func (e DeviceEmulation) Scale() float64 {
	if e.DeviceScaleFactor <= 0 {
		return 1
	}
	return e.DeviceScaleFactor
}

// String renders "390x844", plus "@2x", " mobile", " touch" and the colour
// scheme when set. The holder compares these strings to see a new spec.
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
	if e.ColorScheme != "" {
		b.WriteString(" " + e.ColorScheme)
	}
	return b.String()
}

// mediaParams is the Emulation.setEmulatedMedia call for a colour scheme. An
// empty scheme clears the feature, so a previous dark override never leaks
// into a spec without one.
func mediaParams(colorScheme string) map[string]any {
	features := []map[string]string{}
	if colorScheme != "" {
		features = append(features, map[string]string{"name": "prefers-color-scheme", "value": colorScheme})
	}
	return map[string]any{"features": features}
}

func mediaFeatures(colorScheme string) []*emulation.MediaFeature {
	if colorScheme == "" {
		return []*emulation.MediaFeature{}
	}
	return []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: colorScheme}}
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
// override on one raw CDP connection. Touch and media are always written so a
// previous touch or colour scheme override never leaks into a spec without
// one.
func applyEmulationOnConn(ctx context.Context, conn cdpTransport, e *DeviceEmulation) error {
	id := emulationCallIDBase
	next := func() int64 { id++; return id }
	if e == nil {
		if _, err := cdpCall(ctx, conn, next(), "Emulation.clearDeviceMetricsOverride", nil); err != nil {
			return err
		}
		if _, err := cdpCall(ctx, conn, next(), "Emulation.setTouchEmulationEnabled", touchParams(false)); err != nil {
			return err
		}
		_, err := cdpCall(ctx, conn, next(), "Emulation.setEmulatedMedia", mediaParams(""))
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if _, err := cdpCall(ctx, conn, next(), "Emulation.setDeviceMetricsOverride", e.metricsParams()); err != nil {
		return err
	}
	if _, err := cdpCall(ctx, conn, next(), "Emulation.setTouchEmulationEnabled", touchParams(e.Touch)); err != nil {
		return err
	}
	_, err := cdpCall(ctx, conn, next(), "Emulation.setEmulatedMedia", mediaParams(e.ColorScheme))
	return err
}

// emulationActions is the chromedp form of applyEmulationOnConn.
func emulationActions(e *DeviceEmulation) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if e == nil {
			if err := emulation.ClearDeviceMetricsOverride().Do(ctx); err != nil {
				return err
			}
			if err := emulation.SetTouchEmulationEnabled(false).Do(ctx); err != nil {
				return err
			}
			return emulation.SetEmulatedMedia().WithFeatures(mediaFeatures("")).Do(ctx)
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
		if err := touch.Do(ctx); err != nil {
			return err
		}
		return emulation.SetEmulatedMedia().WithFeatures(mediaFeatures(e.ColorScheme)).Do(ctx)
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
// clears the override and the persisted record. With a holder launcher, a
// holder process keeps the override applied between calls (WORK-1320).
func RemoteSetDeviceEmulation(ctx context.Context, s *Session, e *DeviceEmulation, timeout time.Duration) error {
	if e != nil {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if s.Name == "" {
		if err := rawApplyEmulation(ctx, s, e, timeout); err != nil {
			return err
		}
		s.Emulation = nil
		if e != nil {
			copied := *e
			s.Emulation = &copied
		}
		return nil
	}
	saved, err := SetSessionEmulation(s.Name, e)
	if err != nil {
		return fmt.Errorf("persist viewport: %w", err)
	}
	if e == nil {
		// The holder sees the cleared spec and exits; closing its connection
		// clears its override. Then clear on a fresh connection as well.
		if holderAlive(saved) {
			if saved, err = waitHolder(ctx, s.Name, timeout, func(x *Session) bool { return !holderAlive(x) }); err != nil {
				return fmt.Errorf("stop viewport holder: %w", err)
			}
		}
		if err := rawApplyEmulation(ctx, saved, nil, timeout); err != nil {
			return err
		}
	} else if EmulationHolderLauncher != nil {
		if saved, err = holdSessionEmulation(ctx, saved, timeout); err != nil {
			// The page still gets the size on each call, as before.
			if applyErr := rawApplyEmulation(ctx, saved, e, timeout); applyErr != nil {
				return errors.Join(err, applyErr)
			}
			return fmt.Errorf("viewport %s applies per call only: %w", e, err)
		}
	} else if err := rawApplyEmulation(ctx, saved, e, timeout); err != nil {
		return err
	}
	s.Emulation, s.EmulationRequest, s.EmulationHolder = saved.Emulation, saved.EmulationRequest, saved.EmulationHolder
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
// A live holder is asked to apply it again: re-applying here would set an
// override that this connection's close clears at once.
func restoreSessionEmulation(ctx context.Context, s *Session, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if s.Name != "" {
		held, err := requestHolderApply(ctx, s.Name, timeout)
		if held || err != nil {
			return err
		}
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
