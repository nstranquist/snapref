package browser

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const nestedTargetPrefix = "snapref-locator:v1:"

// TargetLocator is the typed internal form of a selector. Context is empty for
// the top-level document and contains ordered frame/shadow boundaries for a
// nested snapshot element.
type TargetLocator struct {
	Selector string         `json:"selector"`
	Context  []SnapshotRoot `json:"context,omitempty"`
}

func encodeTargetLocator(locator TargetLocator) (string, error) {
	if err := validateTargetLocator(locator); err != nil {
		return "", err
	}
	if len(locator.Context) == 0 {
		return locator.Selector, nil
	}
	body, err := json.Marshal(locator)
	if err != nil {
		return "", err
	}
	return nestedTargetPrefix + base64.RawURLEncoding.EncodeToString(body), nil
}

func decodeTargetLocator(target string) (TargetLocator, bool, error) {
	if !strings.HasPrefix(target, nestedTargetPrefix) {
		locator := TargetLocator{Selector: target}
		return locator, false, validateTargetLocator(locator)
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(target, nestedTargetPrefix))
	if err != nil {
		return TargetLocator{}, true, fmt.Errorf("decode nested browser target: %w", err)
	}
	var locator TargetLocator
	if err := json.Unmarshal(body, &locator); err != nil {
		return TargetLocator{}, true, fmt.Errorf("decode nested browser target: %w", err)
	}
	if err := validateTargetLocator(locator); err != nil {
		return TargetLocator{}, true, err
	}
	if len(locator.Context) == 0 {
		return TargetLocator{}, true, errors.New("nested browser target has no context")
	}
	return locator, true, nil
}

func validateTargetLocator(locator TargetLocator) error {
	if strings.TrimSpace(locator.Selector) == "" {
		return errors.New("browser target selector is empty")
	}
	for i, root := range locator.Context {
		if strings.TrimSpace(root.Selector) == "" {
			return fmt.Errorf("browser target context %d has an empty selector", i)
		}
		switch root.Kind {
		case SnapshotRootShadow, SnapshotRootFrame:
		default:
			return fmt.Errorf("browser target context %d has unsupported kind %q", i, root.Kind)
		}
	}
	return nil
}

// targetElementExpression renders a safe JS expression returning the target
// element. Every selector is JSON-quoted and every root kind is validated.
func targetElementExpression(target string) (string, bool, error) {
	locator, nested, err := decodeTargetLocator(target)
	if err != nil {
		return "", nested, err
	}
	selectorJSON, _ := json.Marshal(locator.Selector)
	if !nested {
		return "document.querySelector(" + string(selectorJSON) + ")", false, nil
	}
	var expression strings.Builder
	expression.WriteString("(() => { let root = document, node; ")
	for _, hop := range locator.Context {
		hopJSON, _ := json.Marshal(hop.Selector)
		expression.WriteString("node = root.querySelector(")
		expression.Write(hopJSON)
		expression.WriteString("); if (!node) return null; ")
		switch hop.Kind {
		case SnapshotRootShadow:
			expression.WriteString("root = node.shadowRoot; ")
		case SnapshotRootFrame:
			expression.WriteString("root = node.contentDocument; ")
		}
		expression.WriteString("if (!root) return null; ")
	}
	expression.WriteString("return root.querySelector(")
	expression.Write(selectorJSON)
	expression.WriteString("); })()")
	return expression.String(), true, nil
}

// TargetCountExpression returns a read-only JS expression for counting a
// resolved target. Snapshot refs are unique by contract, so nested targets are
// either present (1) or absent (0); ordinary selectors retain querySelectorAll
// semantics.
func TargetCountExpression(target string) (string, error) {
	expression, nested, err := targetElementExpression(target)
	if err != nil {
		return "", err
	}
	if nested {
		return "Number(Boolean(" + expression + "))", nil
	}
	locator, _, _ := decodeTargetLocator(target)
	selectorJSON, _ := json.Marshal(locator.Selector)
	return "document.querySelectorAll(" + string(selectorJSON) + ").length", nil
}

// TargetSelector returns the local CSS selector carried by a resolved target,
// suitable for diagnostics without exposing the internal transport encoding.
func TargetSelector(target string) string {
	locator, _, err := decodeTargetLocator(target)
	if err != nil {
		return target
	}
	return locator.Selector
}
