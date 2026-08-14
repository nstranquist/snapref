package browsercontract

import "testing"

func TestRegistryContractIsCompleteAndUnique(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	if len(All()) != 22 {
		t.Fatalf("operation count = %d, want 22", len(All()))
	}
	for _, id := range []OperationID{Navigate, Click, Snapshot, Screenshot, Console, ScriptRun} {
		if _, ok := ByID(id); !ok {
			t.Fatalf("missing operation %q", id)
		}
	}
}

func TestRegistryPermissionClasses(t *testing.T) {
	tests := map[OperationID]Permission{
		Snapshot:   PermissionRead,
		Navigate:   PermissionDrive,
		Console:    PermissionConditionalDrive,
		Screenshot: PermissionConditionalWrite,
		ScriptRun:  PermissionHostRead,
	}
	for id, want := range tests {
		got, ok := ByID(id)
		if !ok || got.Permission != want {
			t.Fatalf("%s permission = %q, want %q", id, got.Permission, want)
		}
	}
}

func TestCLIAndScriptAvailabilityAreExplicit(t *testing.T) {
	for _, verb := range []string{"navigate", "click", "type", "wait", "assert", "snapshot", "screenshot-current"} {
		if _, ok := ByCLIVerb(verb); !ok {
			t.Errorf("CLI verb %q is not registered", verb)
		}
	}
	if operation, _ := ByID(ClickText); operation.CLIVerb != "" || operation.ScriptVerb == "" {
		t.Fatalf("click_text availability is wrong: %+v", operation)
	}
}
