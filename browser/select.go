package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// selectElementJS selects one enabled option by its normalized visible label.
// It deliberately does not accept the option value: values are often opaque,
// unstable implementation details that are not present in a page snapshot.
const selectElementJS = `(function(element, requestedLabel) {
  if (!element) throw new Error("selector matched no element");
  const view = element.ownerDocument && element.ownerDocument.defaultView;
  if (!view) throw new Error("matched element has no document window");
  if (!(element instanceof view.HTMLSelectElement)) {
    throw new Error("matched element is not a select element");
  }

  const normalize = value => String(value || "").replace(/\s+/g, " ").trim();
  const wanted = normalize(requestedLabel);
  const labeled = Array.from(element.options)
    .map((option, index) => ({option, index}))
    .filter(candidate => normalize(candidate.option.label) === wanted);
  if (labeled.length === 0) {
    throw new Error("no option has the requested visible label");
  }
  const enabled = labeled.filter(candidate => {
    const group = candidate.option.parentElement;
    return !candidate.option.disabled &&
      !(group instanceof view.HTMLOptGroupElement && group.disabled);
  });
  if (enabled.length === 0) {
    throw new Error("the requested option is disabled");
  }
  if (enabled.length > 1) {
    throw new Error("more than one enabled option has the requested visible label");
  }

  const match = enabled[0];

  try {
    element.focus({preventScroll: true});
  } catch (_) {
    element.focus();
  }

  const descriptor = Object.getOwnPropertyDescriptor(
    view.HTMLSelectElement.prototype,
    "selectedIndex"
  );
  if (!descriptor || typeof descriptor.set !== "function") {
    throw new Error("matched select has no native selectedIndex setter");
  }
  descriptor.set.call(element, match.index);
  if (element.selectedIndex !== match.index) {
    throw new Error("browser did not select the requested option");
  }
  element.dispatchEvent(new view.Event("input", {bubbles: true, composed: true}));
  element.dispatchEvent(new view.Event("change", {bubbles: true, composed: true}));
  return true;
})`

func selectExpression(target, optionLabel string) (string, error) {
	element, _, err := targetElementExpression(target)
	if err != nil {
		return "", err
	}
	labelJSON, err := json.Marshal(optionLabel)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(%s)(%s, %s)", selectElementJS, element, labelJSON), nil
}

// SelectOn selects exactly one enabled option by normalized visible label. It
// performs the same actionability checks as click and type, then emits native
// bubbling input and change events for framework-controlled forms.
func SelectOn(parent context.Context, taskCtx context.Context, target, optionLabel string, timeout time.Duration) error {
	if strings.TrimSpace(optionLabel) == "" {
		return errors.New("select option label is required")
	}
	expression, err := selectExpression(target, optionLabel)
	if err != nil {
		return err
	}
	actionTimeout := actionabilityTimeout(timeout)
	return runWithTimeout(parent, taskCtx, actionabilityRunTimeout(timeout), func(ctx context.Context) error {
		if err := waitForSelectorActionable(ctx, "select", target, actionTimeout); err != nil {
			return err
		}
		var selected bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &selected)); err != nil {
			return fmt.Errorf("select option: %w", err)
		}
		if !selected {
			return errors.New("select option: browser did not confirm the selection")
		}
		return nil
	})
}

// RemoteSelect selects one option in the session's first tab.
func RemoteSelect(ctx context.Context, session *Session, target, optionLabel string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, session)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return SelectOn(ctx, taskCtx, target, optionLabel, timeout)
}

func (d remoteDriver) Select(ctx context.Context, session, target, optionLabel string) error {
	return withRemoteSession(d, session, func(s *Session) error {
		resolved, err := ResolveTarget(ctx, s, target, driverTimeout(ctx))
		if err != nil {
			return err
		}
		return RemoteSelect(ctx, s, resolved, optionLabel, driverTimeout(ctx))
	})
}
