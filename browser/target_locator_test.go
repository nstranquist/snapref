package browser

import (
	"strings"
	"testing"
)

func TestTargetLocatorRoundTripAndSafeExpression(t *testing.T) {
	want := TargetLocator{
		Selector: `button[data-test="save"]`,
		Context: []SnapshotRoot{
			{Kind: SnapshotRootFrame, Selector: `iframe[name="editor"]`},
			{Kind: SnapshotRootShadow, Selector: `#toolbar-host`},
		},
	}
	encoded, err := encodeTargetLocator(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(encoded, nestedTargetPrefix) {
		t.Fatalf("encoded target missing internal prefix: %q", encoded)
	}
	got, nested, err := decodeTargetLocator(encoded)
	if err != nil || !nested {
		t.Fatalf("decode = %+v nested=%v err=%v", got, nested, err)
	}
	if got.Selector != want.Selector || !snapshotContextsEqual(got.Context, want.Context) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	expression, nested, err := targetElementExpression(encoded)
	if err != nil || !nested {
		t.Fatalf("expression: nested=%v err=%v", nested, err)
	}
	for _, phrase := range []string{"contentDocument", "shadowRoot", "querySelector", `button[data-test=\"save\"]`} {
		if !strings.Contains(expression, phrase) {
			t.Errorf("expression missing %q: %s", phrase, expression)
		}
	}
	if TargetSelector(encoded) != want.Selector {
		t.Fatalf("TargetSelector = %q, want %q", TargetSelector(encoded), want.Selector)
	}
}

func TestTargetLocatorPreservesOrdinarySelectorAndCounts(t *testing.T) {
	selector := `button[data-test="save"]`
	encoded, err := encodeTargetLocator(TargetLocator{Selector: selector})
	if err != nil || encoded != selector {
		t.Fatalf("top-level target = %q, %v", encoded, err)
	}
	expression, err := TargetCountExpression(selector)
	if err != nil || expression != `document.querySelectorAll("button[data-test=\"save\"]").length` {
		t.Fatalf("count expression = %q, %v", expression, err)
	}
}

func TestTargetLocatorRejectsUnsupportedContext(t *testing.T) {
	for _, locator := range []TargetLocator{
		{},
		{Selector: "button", Context: []SnapshotRoot{{Kind: "closed-shadow", Selector: "#host"}}},
		{Selector: "button", Context: []SnapshotRoot{{Kind: SnapshotRootFrame}}},
	} {
		if _, err := encodeTargetLocator(locator); err == nil {
			t.Fatalf("encodeTargetLocator(%+v) unexpectedly succeeded", locator)
		}
	}
}
