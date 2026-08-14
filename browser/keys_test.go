package browser

import (
	"testing"

	"github.com/chromedp/chromedp/kb"
)

func TestKeyEventArgNamedKeys(t *testing.T) {
	cases := map[string]string{
		"Enter":      kb.Enter,
		"enter":      kb.Enter,
		"  Escape  ": kb.Escape,
		"esc":        kb.Escape,
		"Tab":        kb.Tab,
		"ArrowDown":  kb.ArrowDown,
		"down":       kb.ArrowDown,
		"Backspace":  kb.Backspace,
		"space":      " ",
	}
	for in, want := range cases {
		if got := keyEventArg(in); got != want {
			t.Errorf("keyEventArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKeyEventArgLiteralFallthrough(t *testing.T) {
	// Single chars and arbitrary text are typed literally (unchanged behavior).
	for _, in := range []string{"a", "Z", "7", "hello", "ctrl+k"} {
		if got := keyEventArg(in); got != in {
			t.Errorf("keyEventArg(%q) = %q, want literal pass-through", in, got)
		}
	}
}
