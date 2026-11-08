package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/chromedp/cdproto/runtime"
	"strings"
	"testing"
)

func TestOperationErrorsIgnorePageText(t *testing.T) {
	for _, message := range []string{"policy denied", "context canceled selector transport", "secret=https://user:password@example.test"} {
		exception := &runtime.ExceptionDetails{Text: message}
		eval := fmt.Errorf("outer: %w", evaluationError(exception))
		if got := ClassifyError(eval); got != ErrorClassUsage {
			t.Fatalf("eval class=%s", got)
		}
		if got := ErrorReason(eval); got != "javascript_exception" {
			t.Fatalf("eval reason=%s", got)
		}
		if !errors.Is(eval, exception) {
			t.Fatal("lost evaluation cause")
		}
		selection := selectionError(exception)
		if got := ClassifyError(selection); got != ErrorClassSelector {
			t.Fatalf("select class=%s", got)
		}
		if got := ErrorReason(selection); got != "select_option_failed" {
			t.Fatalf("select reason=%s", got)
		}
	}
	for _, original := range []error{context.DeadlineExceeded, context.Canceled, errors.New("websocket connection refused")} {
		if evaluationError(original) != original || selectionError(original) != original {
			t.Fatal("non-JS error was rewritten")
		}
	}
}

func TestTelemetryStatelessCohortPreservesHistoricalRate(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	t.Setenv("SNAPREF_RUN_KIND", "negative-probe")
	events := []ObserverCommand{
		{Command: "exec.eval", OK: true},
		{Command: "exec.click", OK: false, ErrorClass: ErrorClassSelector},
		{Command: "eval", Cohort: StatelessTelemetryCohort, OK: false, ErrorClass: ErrorClassUsage, ReasonCode: "javascript_exception"},
		{Command: "screenshot", Cohort: StatelessTelemetryCohort, OK: true},
	}
	for _, event := range events {
		if err := AppendCommandEvent("cohort", event); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalEvents != 2 || stats.NonOK != 1 || stats.ErrorRatePct != 50 {
		t.Fatalf("historical cohort changed: %+v", stats)
	}
	if stats.Stateless.TotalEvents != 2 || stats.Stateless.NonOK != 1 || stats.Stateless.ErrorRatePct != 50 {
		t.Fatalf("stateless=%+v", stats.Stateless)
	}
	rows, _, err := ReadCommandEventFamily()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows=%d", len(rows))
	}
	for _, row := range rows {
		if row.Command.RunKind != "negative-probe" {
			t.Fatal("probe metadata lost")
		}
		if !row.Command.OK && row.Command.ReasonCode == "" {
			t.Fatal("failure reason missing")
		}
		body, _ := json.Marshal(row)
		if strings.Contains(string(body), "password") {
			t.Fatal("unsafe error text persisted")
		}
	}
}
