package browser

import (
	"testing"
	"time"
)

func cmdEvent(session, verb string, ok bool, ts time.Time) ObserverEvent {
	return ObserverEvent{
		Timestamp: ts,
		Kind:      KindCommand,
		Session:   session,
		Command:   &ObserverCommand{Command: verb, OK: ok},
	}
}

func TestReadStatsAdoptionAndVerbs(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	now := time.Now().UTC()
	events := []ObserverEvent{
		cmdEvent("atlas", "exec.snapshot", true, now),
		cmdEvent("atlas", "exec.snapshot", true, now),
		cmdEvent("atlas", "exec.snapshot", true, now),
		cmdEvent("atlas", "exec.eval", true, now),
		cmdEvent("atlas", "exec.eval", false, now), // one failure
		cmdEvent("atlas", "exec.console", true, now),
		cmdEvent("atlas", "exec.screenshot-current", true, now),
		cmdEvent("other", "exec.click", true, now),
		cmdEvent("other", "script:start", true, now),
	}
	for _, e := range events {
		if err := AppendEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	st, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalEvents != 9 || st.Sessions != 2 {
		t.Fatalf("totals: events=%d sessions=%d", st.TotalEvents, st.Sessions)
	}
	if st.NonOK != 1 || st.ErrorRatePct == 0 {
		t.Fatalf("error tracking: nonOK=%d rate=%.1f", st.NonOK, st.ErrorRatePct)
	}
	a := st.Adoption
	if a.Snapshot != 3 || a.Eval != 2 {
		t.Fatalf("adoption counts: snapshot=%d eval=%d", a.Snapshot, a.Eval)
	}
	// 3 / (3+2) = 60%
	if a.TypedToolPct != 60 {
		t.Fatalf("typed-tool pct = %.1f, want 60", a.TypedToolPct)
	}
	if a.ConsoleChecks != 1 || a.Screenshots != 1 || a.ScriptRuns != 1 || a.Clicks != 1 {
		t.Fatalf("adoption mix wrong: %+v", a)
	}
	if got := a.AdoptionVerdict(); got == "" {
		t.Fatal("expected a verdict string")
	}
	// verbs sorted desc by count; snapshot (3) should rank above eval (2)
	if len(st.Verbs) == 0 || st.Verbs[0].Verb != "exec.snapshot" || st.Verbs[0].Count != 3 {
		t.Fatalf("verb ranking wrong: %+v", st.Verbs)
	}
}

func TestReadStatsExcludesLegacySyntheticFixtureEvents(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	now := time.Now().UTC()
	synthetic := cmdEvent("a", "script:navigate", false, now)
	synthetic.Command.Data = map[string]any{"run_id": "run-19700101T000000.000-12345-001"}
	for _, event := range []ObserverEvent{
		synthetic,
		cmdEvent("atlas", "exec.snapshot", true, now),
	} {
		if err := AppendEvent(event); err != nil {
			t.Fatal(err)
		}
	}

	st, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalEvents != 1 || st.NonOK != 0 || st.ExcludedSynthetic != 1 {
		t.Fatalf("synthetic filtering: events=%d non_ok=%d excluded=%d", st.TotalEvents, st.NonOK, st.ExcludedSynthetic)
	}
}

func TestReadStatsExcludesEscapedCLIErrorClassFixtureWindow(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	synthetic := cmdEvent("s", "exec.click", false, legacyCLIFixtureStart.Add(time.Second))
	synthetic.ErrorClass = ErrorClassUsage
	synthetic.Command.ErrorClass = ErrorClassUsage
	synthetic.Command.ExitCode = 1
	synthetic.Command.DurationMS = 1
	synthetic.Command.Data = map[string]any{"selector": "#x"}
	real := cmdEvent("s", "exec.click", false, legacyCLIFixtureEnd.Add(time.Second))
	real.Command.Data = map[string]any{"selector": "#x"}
	for _, event := range []ObserverEvent{synthetic, real} {
		if err := AppendEvent(event); err != nil {
			t.Fatal(err)
		}
	}

	st, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalEvents != 1 || st.NonOK != 1 || st.ExcludedSynthetic != 1 {
		t.Fatalf("escaped fixture filtering: events=%d non_ok=%d excluded=%d", st.TotalEvents, st.NonOK, st.ExcludedSynthetic)
	}
}

func TestReadStatsExcludesExactCLIErrorClassFixtureOutsideLegacyWindow(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	synthetic := cmdEvent("s", "exec.click", false, time.Now().UTC())
	synthetic.ErrorClass = ErrorClassPolicy
	synthetic.Command.ErrorClass = ErrorClassPolicy
	synthetic.Command.ExitCode = 1
	synthetic.Command.DurationMS = 1
	synthetic.Command.Data = map[string]any{"selector": "#x"}
	if err := AppendEvent(synthetic); err != nil {
		t.Fatal(err)
	}

	st, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalEvents != 0 || st.ExcludedSynthetic != 1 {
		t.Fatalf("exact fixture filtering: events=%d excluded=%d", st.TotalEvents, st.ExcludedSynthetic)
	}
}

func TestReadStatsExcludesEscapedIdlePolicyParserFixtures(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	now := legacyIdleFixtureStart.Add(time.Minute)
	conflict := cmdEvent("conflict", "open", false, now)
	conflict.Command.ErrorClass = ErrorClassUsage
	conflict.Command.ExitCode = 2
	conflict.Command.Data = map[string]any{"lifecycle_policy": "keep_alive"}
	tooShort := cmdEvent("too-short", "open", false, now)
	tooShort.Command.ErrorClass = ErrorClassUsage
	tooShort.Command.ExitCode = 2
	tooShort.Command.Data = map[string]any{"lifecycle_policy": "idle", "idle_timeout": "1s"}
	realFailure := cmdEvent("real-session", "open", false, now)
	realFailure.Command.ErrorClass = ErrorClassInternal
	realFailure.Command.ExitCode = 1
	realFailure.Command.Data = map[string]any{"lifecycle_policy": "idle", "idle_timeout": "10m0s"}
	futureMatch := conflict
	futureMatch.Timestamp = legacyIdleFixtureEnd.Add(time.Second)
	nonDevMatch := tooShort
	nonDevMatch.BuildVersion = "abc123+dirty"
	for _, event := range []ObserverEvent{conflict, tooShort, futureMatch, nonDevMatch, realFailure} {
		if err := AppendEvent(event); err != nil {
			t.Fatal(err)
		}
	}

	st, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalEvents != 3 || st.NonOK != 3 || st.ExcludedSynthetic != 2 {
		t.Fatalf("idle fixture filtering: events=%d non_ok=%d excluded=%d", st.TotalEvents, st.NonOK, st.ExcludedSynthetic)
	}
}

func TestReadStatsSessionAndSinceFilter(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	_ = AppendEvent(cmdEvent("atlas", "exec.snapshot", true, now))
	_ = AppendEvent(cmdEvent("atlas", "exec.eval", true, old)) // outside a 24h window
	_ = AppendEvent(cmdEvent("zzz", "exec.eval", true, now))   // other session

	// session filter
	bySession, _ := ReadStats(StatsOptions{Session: "atlas"})
	if bySession.TotalEvents != 2 || bySession.Sessions != 1 {
		t.Fatalf("session filter: events=%d sessions=%d", bySession.TotalEvents, bySession.Sessions)
	}
	// since filter (24h) drops the 48h-old eval
	bySince, _ := ReadStats(StatsOptions{Since: 24 * time.Hour})
	if bySince.Adoption.Eval != 1 { // only the "zzz" recent eval remains
		t.Fatalf("since filter: eval=%d, want 1", bySince.Adoption.Eval)
	}
}

func TestStatsWindowLabelAndRatchet(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	now := time.Now().UTC()
	for _, e := range []ObserverEvent{
		cmdEvent("atlas", "exec.snapshot", true, now),
		cmdEvent("atlas", "exec.snapshot", true, now),
		cmdEvent("atlas", "exec.eval", true, now),
		cmdEvent("atlas", "exec.click", true, now),
	} {
		if err := AppendEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendSnapshotEvent("atlas", &PageSnapshot{URL: "x", Matched: 2, Returned: 2, DurationMS: 1}); err != nil {
		t.Fatal(err)
	}
	opts := StatsOptions{Since: 24 * time.Hour, WindowLabel: "24h"}
	st, err := ReadStats(opts)
	if err != nil {
		t.Fatal(err)
	}
	if st.Since != "24h" || StatsWindowLabel(opts) != "last 24h" {
		t.Fatalf("window labels: since=%q label=%q", st.Since, StatsWindowLabel(opts))
	}
	pass := EvaluateStatsRatchet(st, StatsRatchetOptions{
		MinTypedToolPct:    50,
		MaxClickFailurePct: 0,
		MaxErrorRatePct:    0,
		MinEvents:          1,
		MinSnapshots:       1,
		MinClickSamples:    1,
	})
	if !pass.OK {
		t.Fatalf("expected ratchet pass: %+v", pass.Checks)
	}
	fail := EvaluateStatsRatchet(st, StatsRatchetOptions{
		MinTypedToolPct:    90,
		MaxClickFailurePct: 0,
		MaxErrorRatePct:    0,
		MinEvents:          1,
		MinSnapshots:       1,
		MinClickSamples:    1,
	})
	if fail.OK {
		t.Fatalf("expected typed-discovery ratchet failure: %+v", fail.Checks)
	}
}

func TestSnapshotStatsRollup(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	_ = AppendSnapshotEvent("atlas", &PageSnapshot{URL: "x", Matched: 10, Returned: 10, DurationMS: 2})
	_ = AppendSnapshotEvent("atlas", &PageSnapshot{URL: "y", Matched: 20, Returned: 20, DurationMS: 4, Truncated: true})
	st, _ := ReadStats(StatsOptions{})
	s := st.Snapshots
	if s.Count != 2 || s.AvgMatched != 15 || s.AvgDurationMS != 3 || s.Truncated != 1 {
		t.Fatalf("snapshot rollup wrong: %+v", s)
	}
}

func TestFindingsRoundTripAndValidation(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())

	// valid add (severity defaults to medium when empty)
	if err := AppendFinding(FindingRecord{Session: "atlas", View: "graph", Finding: "nodes unnamed"}); err != nil {
		t.Fatalf("valid add failed: %v", err)
	}
	if err := AppendFinding(FindingRecord{Session: "atlas", Finding: "fragile selector", Severity: "HIGH", Category: "fragile-selector", Verified: true}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	// invalid: empty finding
	if err := AppendFinding(FindingRecord{Severity: "low"}); err == nil {
		t.Fatal("expected error on empty finding")
	}
	// invalid: bad severity
	if err := AppendFinding(FindingRecord{Finding: "x", Severity: "bogus"}); err == nil {
		t.Fatal("expected error on bad severity")
	}
	// invalid: bad category
	if err := AppendFinding(FindingRecord{Finding: "x", Category: "nope"}); err == nil {
		t.Fatal("expected error on bad category")
	}

	all, err := ReadFindings(FindingsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 valid findings, got %d", len(all))
	}
	if all[0].Severity != "medium" {
		t.Fatalf("empty severity should default to medium, got %q", all[0].Severity)
	}
	if all[1].Severity != "high" { // normalized from "HIGH"
		t.Fatalf("severity not normalized: %q", all[1].Severity)
	}

	// filters
	high, _ := ReadFindings(FindingsFilter{Severity: "high"})
	if len(high) != 1 || high[0].Finding != "fragile selector" {
		t.Fatalf("severity filter wrong: %+v", high)
	}

	// rollup via stats
	st, _ := ReadStats(StatsOptions{})
	if st.Findings.Total != 2 || st.Findings.Verified != 1 {
		t.Fatalf("findings rollup: total=%d verified=%d", st.Findings.Total, st.Findings.Verified)
	}
	if st.Findings.BySeverity["high"] != 1 || st.Findings.BySeverity["medium"] != 1 {
		t.Fatalf("by-severity rollup wrong: %+v", st.Findings.BySeverity)
	}
}
