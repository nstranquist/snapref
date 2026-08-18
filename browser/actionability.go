package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const actionabilityPollInterval = 100 * time.Millisecond
const actionabilityOuterGrace = 500 * time.Millisecond

type actionabilityResult struct {
	OK           bool    `json:"ok"`
	Precondition string  `json:"precondition"`
	Reason       string  `json:"reason"`
	Node         string  `json:"node,omitempty"`
	X            float64 `json:"x,omitempty"`
	Y            float64 `json:"y,omitempty"`
	W            float64 `json:"w,omitempty"`
	H            float64 `json:"h,omitempty"`
}

type actionabilityFailure struct {
	Action       string
	Selector     string
	Precondition string
	Reason       string
	Timeout      time.Duration
}

func (e actionabilityFailure) Error() string {
	target := "focused element"
	if e.Selector != "" {
		target = fmt.Sprintf("%q", e.Selector)
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "page did not satisfy the precondition"
	}
	if e.Timeout > 0 {
		return fmt.Sprintf("%s %s: actionability timeout after %s waiting for precondition=%s (%s)", e.Action, target, e.Timeout, e.Precondition, reason)
	}
	return fmt.Sprintf("%s %s: actionability failed precondition=%s (%s)", e.Action, target, e.Precondition, reason)
}

func waitForSelectorActionable(ctx context.Context, action, selector string, timeout time.Duration) error {
	return waitForActionable(ctx, action, selector, timeout, selectorActionabilityJS(selector))
}

func waitForFocusedActionable(ctx context.Context, action string, timeout time.Duration) error {
	return waitForActionable(ctx, action, "", timeout, focusedActionabilityJS())
}

func waitForActionable(ctx context.Context, action, selector string, timeout time.Duration, js string) error {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	if err := installActionabilityProbe(ctx); err != nil {
		return actionabilityFailure{
			Action:       action,
			Selector:     selector,
			Precondition: "cdp_eval",
			Reason:       err.Error(),
		}
	}
	deadline := time.Now().Add(timeout)
	last := actionabilityResult{Precondition: "ready", Reason: "not checked yet"}
	for {
		res, err := probeActionability(ctx, js)
		if err != nil {
			last = actionabilityResult{Precondition: "cdp_eval", Reason: err.Error()}
		} else if res.OK {
			return nil
		} else {
			last = res
			if res.Precondition == "selector_valid" {
				return actionabilityFailure{
					Action:       action,
					Selector:     selector,
					Precondition: res.Precondition,
					Reason:       res.Reason,
				}
			}
		}
		if time.Now().After(deadline) {
			return actionabilityFailure{
				Action:       action,
				Selector:     selector,
				Precondition: fallbackPrecondition(last.Precondition),
				Reason:       last.Reason,
				Timeout:      timeout,
			}
		}
		if err := sleepCtx(ctx, actionabilityPollInterval); err != nil {
			return err
		}
	}
}

func fallbackPrecondition(v string) string {
	if strings.TrimSpace(v) == "" {
		return "ready"
	}
	return v
}

func probeActionability(ctx context.Context, js string) (actionabilityResult, error) {
	var out actionabilityResult
	err := chromedp.Run(ctx, chromedp.Evaluate(js, &out, awaitPromise))
	if err != nil {
		if installErr := installActionabilityProbe(ctx); installErr != nil {
			return out, err
		}
		err = chromedp.Run(ctx, chromedp.Evaluate(js, &out, awaitPromise))
	}
	return out, err
}

func actionabilityTimeout(timeout time.Duration) time.Duration {
	if timeout == 0 {
		return 60 * time.Second
	}
	return timeout
}

func actionabilityRunTimeout(timeout time.Duration) time.Duration {
	return actionabilityTimeout(timeout) + actionabilityOuterGrace
}

func awaitPromise(p *runtime.EvaluateParams) *runtime.EvaluateParams {
	return p.WithAwaitPromise(true)
}

func selectorActionabilityJS(selector string) string {
	expression, _, err := targetElementExpression(selector)
	if err != nil {
		expression = "null"
	}
	return fmt.Sprintf(`(() => new Promise(resolve => {
  const finish = value => resolve(value);
  let el;
  try {
	el = %s;
  } catch (err) {
    finish({ok: false, precondition: "selector_valid", reason: String(err && err.message || err)});
    return;
  }
  window.__snaprefActionabilityProbe(el, finish);
}))()`, expression)
}

func focusedActionabilityJS() string {
	return `(() => new Promise(resolve => {
  const finish = value => resolve(value);
  const el = document.activeElement;
  if (!el || el === document.body || el === document.documentElement) {
    requestAnimationFrame(() => requestAnimationFrame(() => finish({ok: true, precondition: "ready", reason: "no focused element"})));
    return;
  }
  window.__snaprefActionabilityProbe(el, finish);
}))()`
}

func actionabilityBootstrapJS() string {
	return `(() => {
  if (window.__snaprefActionabilityProbe) return true;
  window.__snaprefActionabilityProbe = (el, finish) => {
    const describe = node => {
      if (!node) return "none";
      const id = node.id ? "#" + node.id : "";
      const cls = typeof node.className === "string" && node.className ? "." + node.className.trim().split(/\s+/).slice(0, 3).join(".") : "";
      return String(node.tagName || node.nodeName || "node").toLowerCase() + id + cls;
    };
    const rectJSON = r => ({x: r.x, y: r.y, w: r.width, h: r.height});
    const fail = (precondition, reason, rect) => finish(Object.assign({ok: false, precondition, reason}, rect || {}));
    if (!document || document.readyState === "loading") {
      fail("document_ready", "document is still loading");
      return;
    }
    if (!el) {
      fail("present", "selector matched no element");
      return;
    }
    if (!el.isConnected) {
      fail("present", "matched element is detached");
      return;
    }
	const ownerWindow = el.ownerDocument && el.ownerDocument.defaultView || window;
	const root = el.getRootNode && el.getRootNode();
	const style = ownerWindow.getComputedStyle(el);
    const r1 = el.getBoundingClientRect();
    const visible = el.getClientRects().length > 0 &&
      r1.width > 0 && r1.height > 0 &&
      style.visibility !== "hidden" &&
      style.display !== "none" &&
      Number.parseFloat(style.opacity || "1") > 0.01;
    if (!visible) {
      fail("visible", "element is hidden, transparent, display:none, or zero-size", rectJSON(r1));
      return;
    }
	if (r1.right <= 0 || r1.bottom <= 0 || r1.left >= ownerWindow.innerWidth || r1.top >= ownerWindow.innerHeight) {
	  // Snapshot discovery intentionally includes CSS-visible controls outside the
	  // current viewport. Bring those executable targets into view before the
	  // stability and hit-test checks, matching the behavior agents expect from
	  // an actionability-aware click rather than timing out on a valid ref.
	  el.scrollIntoView({block: "center", inline: "center", behavior: "instant"});
	  fail("in_view", "scrolling element into the viewport", rectJSON(r1));
      return;
    }
    const disabled = el.disabled === true ||
      el.getAttribute("aria-disabled") === "true" ||
      Boolean(el.closest && el.closest("fieldset[disabled]"));
    if (disabled) {
      fail("enabled", "element is disabled or aria-disabled", rectJSON(r1));
      return;
    }
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (!el.isConnected) {
        fail("present", "matched element detached between frames");
        return;
      }
      const r2 = el.getBoundingClientRect();
      const stable = Math.abs(r1.x - r2.x) <= 0.5 &&
        Math.abs(r1.y - r2.y) <= 0.5 &&
        Math.abs(r1.width - r2.width) <= 0.5 &&
        Math.abs(r1.height - r2.height) <= 0.5;
      if (!stable) {
        fail("stable", "element moved or resized across two animation frames", rectJSON(r2));
        return;
      }
	  const cx = Math.max(0, Math.min(ownerWindow.innerWidth - 1, r2.left + r2.width / 2));
	  const cy = Math.max(0, Math.min(ownerWindow.innerHeight - 1, r2.top + r2.height / 2));
	  const hitRoot = root && typeof root.elementFromPoint === "function" ? root : el.ownerDocument;
	  const top = hitRoot.elementFromPoint(cx, cy);
	  if (!top || !(top === el || el.contains(top) || top.contains(el))) {
		fail("unoccluded", "center point is covered by " + describe(top), rectJSON(r2));
		return;
	  }
	  let frameView = ownerWindow;
	  while (frameView && frameView.frameElement) {
		const frame = frameView.frameElement;
		const frameRect = frame.getBoundingClientRect();
		const parentView = frame.ownerDocument.defaultView;
		const frameX = Math.max(0, Math.min(parentView.innerWidth - 1, frameRect.left + frameRect.width / 2));
		const frameY = Math.max(0, Math.min(parentView.innerHeight - 1, frameRect.top + frameRect.height / 2));
		const frameTop = frame.ownerDocument.elementFromPoint(frameX, frameY);
		if (!frameTop || !(frameTop === frame || frame.contains(frameTop) || frameTop.contains(frame))) {
		  fail("unoccluded", "frame boundary is covered by " + describe(frameTop), rectJSON(frameRect));
		  return;
		}
		frameView = parentView;
	  }
	  finish(Object.assign({ok: true, precondition: "ready", reason: "actionable"}, rectJSON(r2)));
    }));
  };
  return true;
})()`
}

func installActionabilityProbe(ctx context.Context) error {
	var ok bool
	return chromedp.Run(ctx, chromedp.Evaluate(actionabilityBootstrapJS(), &ok))
}
