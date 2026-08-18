package main

import "testing"

func TestRefForLabelSelectsNamedControl(t *testing.T) {
	snapshot := "- textbox \"Password\" [ref=e1]\n- button \"Benchmark action\" [ref=e2]\n"
	if got := refForLabel(snapshot, "Benchmark action"); got != "e2" {
		t.Fatalf("refForLabel() = %q, want e2", got)
	}
	atSnapshot := "textbox Password @a1\nbutton Benchmark action @a2\n"
	if got := refForLabel(atSnapshot, "Benchmark action"); got != "@a2" {
		t.Fatalf("refForLabel(@) = %q, want @a2", got)
	}
}
