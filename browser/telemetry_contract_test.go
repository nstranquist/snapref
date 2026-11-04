package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClassifyErrorClosedClasses(t *testing.T) {
	tests := []struct {
		err  error
		want ErrorClass
	}{
		{context.DeadlineExceeded, ErrorClassTimeout},
		{errors.New("browser_click is disabled on this bridge"), ErrorClassPolicy},
		{errors.New("assertion failed: contains Live"), ErrorClassAssertion},
		{&SnapshotRefError{Ref: "sr-bad:e1", Reason: "stale", Err: ErrStaleSnapshotRef}, ErrorClassSelector},
		{errors.New("websocket connection refused"), ErrorClassTransport},
		{errors.New("invalid --max value"), ErrorClassUsage},
		{errors.New(`session "s" is already running (pid 1, port 2) — close it first`), ErrorClassUsage},
		{errors.New("console --fail-on-error: captured 3 error(s)"), ErrorClassAssertion},
		{errors.New("product upstream returned http 503"), ErrorClassProduct},
		{errors.New("unexpected invariant"), ErrorClassInternal},
	}
	for _, test := range tests {
		if got := ClassifyError(test.err); got != test.want {
			t.Errorf("ClassifyError(%q) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestAppendEventAddsTelemetrySchemaBuildOperationAndErrorClass(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if err := AppendEvent(ObserverEvent{
		Kind: KindCommand, Session: "s", Error: "snapshot ref stale: stale snapshot ref",
		Command: &ObserverCommand{Command: "exec.click", OK: false, ExitCode: 1},
	}); err != nil {
		t.Fatal(err)
	}
	path, _ := CommandEventLogPath()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var event ObserverEvent
	if !bufio.NewScanner(file).Scan() {
		t.Fatal("missing telemetry row")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(body))), &event); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != BrowserTelemetrySchemaVersion || event.BuildVersion == "" {
		t.Fatalf("telemetry contract missing: %+v", event)
	}
	if event.Command == nil || event.Command.Operation != "browser.click" {
		t.Fatalf("canonical operation missing: %+v", event.Command)
	}
	if event.ErrorClass != ErrorClassSelector || event.Command.ErrorClass != ErrorClassSelector {
		t.Fatalf("error class missing: event=%q command=%q", event.ErrorClass, event.Command.ErrorClass)
	}
}

func TestLegacyEventStillDecodes(t *testing.T) {
	legacy := []byte(`{"timestamp":"2026-07-01T00:00:00Z","kind":"browser.command","session":"s","command":{"command":"exec.wait","exit_code":0,"ok":true,"duration_ms":2}}`)
	var event ObserverEvent
	if err := json.Unmarshal(legacy, &event); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != 0 || event.BuildVersion != "" || event.ErrorClass != "" {
		t.Fatalf("legacy absent fields should retain zero values: %+v", event)
	}
	if event.Timestamp != (time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("legacy timestamp = %v", event.Timestamp)
	}
}

func TestBrowserBuildVersionIsNeverEmpty(t *testing.T) {
	if BrowserBuildVersion() == "" {
		t.Fatal("build version is empty")
	}
}
