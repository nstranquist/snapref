package browser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Stats — the computed adoption + health layer over the telemetry sinks. It
// answers "how much / how well was snapref used" objectively, by a single
// linear scan of events.jsonl + snapshot-events.jsonl + findings.jsonl. Nothing
// here trusts self-report: the adoption numbers (snapshot vs eval escape-hatch,
// console checks, script runs) are derived from the mechanical event stream the
// CLI/MCP/script surfaces already write. The findings rollup is the one
// agent-written layer (see findings.go), surfaced side by side so a run is
// judged on both how the tool was used and what it found/fixed.

// VerbStat is one verb's usage in the window.
type VerbStat struct {
	Verb  string `json:"verb"`
	Count int    `json:"count"`
	NonOK int    `json:"non_ok,omitempty"`
}

// SnapshotStats summarizes the discovery sink.
type SnapshotStats struct {
	Count         int     `json:"count"`
	Truncated     int     `json:"truncated,omitempty"`
	AvgMatched    float64 `json:"avg_matched"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
}

// AdoptionStats is the headline: did agents reach for the typed discovery tool
// (snapshot) or the untyped escape hatch (eval), and how much of the harness
// (console assertions, script runs) did they exercise.
type AdoptionStats struct {
	Snapshot      int     `json:"snapshot"`       // exec.snapshot + script:snapshot
	Eval          int     `json:"eval"`           // exec.eval + script:eval
	TypedToolPct  float64 `json:"typed_tool_pct"` // snapshot/(snapshot+eval)*100 — typed discovery vs eval escape-hatch
	ConsoleChecks int     `json:"console_checks"` // exec.console + script:console
	ScriptRuns    int     `json:"script_runs"`    // script:start
	Screenshots   int     `json:"screenshots"`    // screenshot + screenshot-current + script:screenshot
	Clicks        int     `json:"clicks"`         // exec.click + script:click + click_text/click_xpath
}

// FindingsStats rolls up the agent-written findings ledger.
type FindingsStats struct {
	Total      int            `json:"total"`
	Verified   int            `json:"verified"`
	BySeverity map[string]int `json:"by_severity,omitempty"`
	ByCategory map[string]int `json:"by_category,omitempty"`
}

// BrowserStats is the full computed summary.
type BrowserStats struct {
	Window            string        `json:"window,omitempty"`
	Since             string        `json:"since,omitempty"`
	TotalEvents       int           `json:"total_events"`
	ExcludedSynthetic int           `json:"excluded_synthetic,omitempty"`
	Sessions          int           `json:"sessions"`
	SessionNames      []string      `json:"session_names,omitempty"`
	NonOK             int           `json:"non_ok"`
	ErrorRatePct      float64       `json:"error_rate_pct"`
	Verbs             []VerbStat    `json:"verbs"`
	Snapshots         SnapshotStats `json:"snapshots"`
	Adoption          AdoptionStats `json:"adoption"`
	Findings          FindingsStats `json:"findings"`
}

// StatsOptions narrows the scan.
type StatsOptions struct {
	Since       time.Duration // 0 = all time
	WindowLabel string        // optional human/JSON label for Since, e.g. "14d"
	Session     string        // "" = all sessions
}

const (
	DefaultStatsWindow      = 14 * 24 * time.Hour
	DefaultStatsWindowLabel = "14d"

	DefaultRatchetMinTypedToolPct    = 15.0
	DefaultRatchetMaxClickFailurePct = 13.0
	DefaultRatchetMaxErrorRatePct    = 6.0
)

// StatsWindowLabel is the short user-facing label for a stats scan.
func StatsWindowLabel(opts StatsOptions) string {
	if opts.Since <= 0 {
		return "all time"
	}
	if opts.WindowLabel != "" {
		return "last " + opts.WindowLabel
	}
	return "last " + opts.Since.String()
}

func newJSONLScanner(f *os.File) *bufio.Scanner {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return sc
}

// ReadStats computes the adoption + health summary. It is read-only and
// tolerates a torn last line (a concurrently-appending session) by skipping it.
func ReadStats(opts StatsOptions) (*BrowserStats, error) {
	var cutoff time.Time
	if opts.Since > 0 {
		cutoff = time.Now().Add(-opts.Since)
	}

	st := &BrowserStats{}
	if opts.Since > 0 {
		if opts.WindowLabel != "" {
			st.Since = opts.WindowLabel
		} else {
			st.Since = opts.Since.String()
		}
		st.Window = "since"
	} else {
		st.Window = "all_time"
	}

	verbCounts := map[string]int{}
	verbNonOK := map[string]int{}
	sessions := map[string]bool{}

	// events.jsonl — the mechanical trace.
	if path, err := CommandEventLogPath(); err == nil {
		_ = forEachJSONLFamilyLine(path, func(line []byte) error {
			var ev ObserverEvent
			if json.Unmarshal(line, &ev) != nil {
				return nil
			}
			if !cutoff.IsZero() && !ev.Timestamp.IsZero() && ev.Timestamp.Before(cutoff) {
				return nil
			}
			if opts.Session != "" && ev.Session != opts.Session {
				return nil
			}
			if ev.Command == nil || ev.Command.Command == "" {
				return nil
			}
			if isSyntheticFixtureEvent(ev) {
				st.ExcludedSynthetic++
				return nil
			}
			st.TotalEvents++
			if ev.Session != "" {
				sessions[ev.Session] = true
			}
			verb := ev.Command.Command
			verbCounts[verb]++
			if !ev.Command.OK {
				verbNonOK[verb]++
				st.NonOK++
			}
			return nil
		})
	}

	for verb, n := range verbCounts {
		st.Verbs = append(st.Verbs, VerbStat{Verb: verb, Count: n, NonOK: verbNonOK[verb]})
	}
	sort.Slice(st.Verbs, func(i, j int) bool {
		if st.Verbs[i].Count != st.Verbs[j].Count {
			return st.Verbs[i].Count > st.Verbs[j].Count
		}
		return st.Verbs[i].Verb < st.Verbs[j].Verb
	})
	for s := range sessions {
		st.SessionNames = append(st.SessionNames, s)
	}
	sort.Strings(st.SessionNames)
	st.Sessions = len(st.SessionNames)
	if st.TotalEvents > 0 {
		st.ErrorRatePct = round1(float64(st.NonOK) / float64(st.TotalEvents) * 100)
	}

	st.Adoption = computeAdoption(verbCounts)

	// snapshot-events.jsonl — discovery sink.
	st.Snapshots = readSnapshotStats(cutoff, opts.Session)

	// findings.jsonl — agent-written outcomes.
	st.Findings = readFindingsStats(opts)

	return st, nil
}

const legacySyntheticRunIDPrefix = "run-19700101T000000."

var (
	legacyCLIFixtureStart  = time.Date(2026, 7, 12, 23, 53, 0, 0, time.UTC)
	legacyCLIFixtureEnd    = time.Date(2026, 7, 12, 23, 57, 0, 0, time.UTC)
	legacyIdleFixtureStart = time.Date(2026, 7, 13, 15, 55, 0, 0, time.UTC)
	legacyIdleFixtureEnd   = time.Date(2026, 7, 14, 21, 50, 0, 0, time.UTC)
)

// isSyntheticFixtureEvent recognizes the deterministic fixed-clock run IDs
// emitted by browserscript unit tests before those tests were isolated from
// the operator telemetry store. The raw JSONL remains untouched for audit;
// health/adoption stats omit these known synthetic rows and report the count.
func isSyntheticFixtureEvent(ev ObserverEvent) bool {
	if ev.Command == nil {
		return false
	}
	if runID, ok := ev.Command.Data["run_id"].(string); ok && strings.HasPrefix(runID, legacySyntheticRunIDPrefix) {
		return true
	}
	// The CLI idle-policy parser tests historically initialized the production
	// telemetry adapter before their temporary home override took effect. Match
	// only their deterministic invalid-open signatures; real open failures and
	// all successful fixture opens remain visible in operator stats.
	if ev.BuildVersion == "dev" && !ev.Timestamp.Before(legacyIdleFixtureStart) && ev.Timestamp.Before(legacyIdleFixtureEnd) &&
		ev.Command.Command == "open" && !ev.Command.OK && ev.Command.ExitCode == 2 &&
		ev.Command.ErrorClass == ErrorClassUsage {
		if ev.Session == "conflict" && ev.Command.Data["lifecycle_policy"] == "keep_alive" {
			return true
		}
		if ev.Session == "too-short" && ev.Command.Data["lifecycle_policy"] == "idle" &&
			ev.Command.Data["idle_timeout"] == "1s" {
			return true
		}
	}
	// Pre-fix cmd/ndev-go error-class tests escaped into the operator store when
	// adapter initialization let a stale SNAPREF_HOME override isolated test
	// homes. Preserve the raw rows, but omit only the exact synthetic command.
	if ev.Command.Command != "exec.click" || ev.Command.OK || ev.Command.ExitCode != 1 ||
		ev.Command.Data["selector"] != "#x" {
		return false
	}
	if ev.Session == "s" && ev.Command.DurationMS == 1 && isClosedErrorClass(ev.Command.ErrorClass) {
		return true
	}
	// The companion missing-session fixture did not have the eight-class/duration
	// signature, so retain the narrow historical time bound for those rows.
	return ev.Session == "missing" && !ev.Timestamp.Before(legacyCLIFixtureStart) && ev.Timestamp.Before(legacyCLIFixtureEnd)
}

func isClosedErrorClass(class ErrorClass) bool {
	switch class {
	case ErrorClassUsage, ErrorClassPolicy, ErrorClassAssertion, ErrorClassSelector,
		ErrorClassTransport, ErrorClassTimeout, ErrorClassProduct, ErrorClassInternal:
		return true
	default:
		return false
	}
}

// sumVerbs returns the total count over the named verbs (any surface prefix).
func sumVerbs(counts map[string]int, names ...string) int {
	total := 0
	for _, n := range names {
		total += counts[n]
	}
	return total
}

func computeAdoption(counts map[string]int) AdoptionStats {
	a := AdoptionStats{
		Snapshot:      sumVerbs(counts, "exec.snapshot", "script:snapshot"),
		Eval:          sumVerbs(counts, "exec.eval", "script:eval"),
		ConsoleChecks: sumVerbs(counts, "exec.console", "script:console"),
		ScriptRuns:    sumVerbs(counts, "script:start"),
		Screenshots:   sumVerbs(counts, "exec.screenshot", "exec.screenshot-current", "script:screenshot"),
		Clicks:        sumVerbs(counts, "exec.click", "script:click", "script:click_text", "script:click_xpath"),
	}
	if denom := a.Snapshot + a.Eval; denom > 0 {
		a.TypedToolPct = round1(float64(a.Snapshot) / float64(denom) * 100)
	}
	return a
}

func readSnapshotStats(cutoff time.Time, session string) SnapshotStats {
	out := SnapshotStats{}
	path, err := SnapshotEventLogPath()
	if err != nil {
		return out
	}
	var sumMatched, sumDur float64
	_ = forEachJSONLFamilyLine(path, func(line []byte) error {
		var ev SnapshotEvent
		if json.Unmarshal(line, &ev) != nil {
			return nil
		}
		if !cutoff.IsZero() && !ev.Timestamp.IsZero() && ev.Timestamp.Before(cutoff) {
			return nil
		}
		if session != "" && ev.Session != session {
			return nil
		}
		out.Count++
		if ev.Truncated {
			out.Truncated++
		}
		sumMatched += float64(ev.Matched)
		sumDur += float64(ev.DurationMS)
		return nil
	})
	if out.Count > 0 {
		out.AvgMatched = round1(sumMatched / float64(out.Count))
		out.AvgDurationMS = round1(sumDur / float64(out.Count))
	}
	return out
}

func readFindingsStats(opts StatsOptions) FindingsStats {
	recs, err := ReadFindings(FindingsFilter{Session: opts.Session, Since: opts.Since})
	out := FindingsStats{BySeverity: map[string]int{}, ByCategory: map[string]int{}}
	if err != nil {
		return out
	}
	for _, r := range recs {
		out.Total++
		if r.Verified {
			out.Verified++
		}
		if r.Severity != "" {
			out.BySeverity[r.Severity]++
		}
		if r.Category != "" {
			out.ByCategory[r.Category]++
		}
	}
	if len(out.BySeverity) == 0 {
		out.BySeverity = nil
	}
	if len(out.ByCategory) == 0 {
		out.ByCategory = nil
	}
	return out
}

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}

// StatsRatchetOptions defines the fail-closed self-eval gate over browser
// telemetry. Defaults intentionally match the current 14-day baseline closely:
// a regression fails, while the actionability fixture can pass stricter values.
type StatsRatchetOptions struct {
	MinTypedToolPct    float64
	MaxClickFailurePct float64
	MaxErrorRatePct    float64
	MinEvents          int
	MinSnapshots       int
	MinClickSamples    int
}

func DefaultStatsRatchetOptions() StatsRatchetOptions {
	return StatsRatchetOptions{
		MinTypedToolPct:    DefaultRatchetMinTypedToolPct,
		MaxClickFailurePct: DefaultRatchetMaxClickFailurePct,
		MaxErrorRatePct:    DefaultRatchetMaxErrorRatePct,
		MinEvents:          1,
		MinSnapshots:       1,
		MinClickSamples:    1,
	}
}

type StatsRatchetCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

type StatsRatchetResult struct {
	OK     bool                `json:"ok"`
	Window string              `json:"window"`
	Stats  *BrowserStats       `json:"stats"`
	Checks []StatsRatchetCheck `json:"checks"`
}

func EvaluateStatsRatchet(st *BrowserStats, opts StatsRatchetOptions) StatsRatchetResult {
	out := StatsRatchetResult{OK: true, Stats: st}
	if st != nil {
		out.Window = st.Since
		if out.Window == "" {
			out.Window = "all_time"
		}
	}
	add := func(name string, ok bool, expected, actual string) {
		out.Checks = append(out.Checks, StatsRatchetCheck{Name: name, OK: ok, Expected: expected, Actual: actual})
		if !ok {
			out.OK = false
		}
	}
	if st == nil {
		add("stats_present", false, "stats must be readable", "nil")
		return out
	}
	add("events_present", st.TotalEvents >= opts.MinEvents, fmt.Sprintf("events >= %d", opts.MinEvents), fmt.Sprintf("%d", st.TotalEvents))
	add("typed_discovery", st.Adoption.TypedToolPct >= opts.MinTypedToolPct, fmt.Sprintf("typed_tool_pct >= %.1f", opts.MinTypedToolPct), fmt.Sprintf("%.1f", st.Adoption.TypedToolPct))
	add("error_rate", st.ErrorRatePct <= opts.MaxErrorRatePct, fmt.Sprintf("error_rate_pct <= %.1f", opts.MaxErrorRatePct), fmt.Sprintf("%.1f", st.ErrorRatePct))
	add("snapshots_recorded", st.Snapshots.Count >= opts.MinSnapshots, fmt.Sprintf("snapshots >= %d", opts.MinSnapshots), fmt.Sprintf("%d", st.Snapshots.Count))

	clicks, clickFailures := st.verbTotals("exec.click", "script:click")
	clickFailurePct := 0.0
	if clicks > 0 {
		clickFailurePct = round1(float64(clickFailures) / float64(clicks) * 100)
	}
	add("click_samples", clicks >= opts.MinClickSamples, fmt.Sprintf("click samples >= %d", opts.MinClickSamples), fmt.Sprintf("%d", clicks))
	add("click_failure_rate", clickFailurePct <= opts.MaxClickFailurePct, fmt.Sprintf("click_failure_pct <= %.1f", opts.MaxClickFailurePct), fmt.Sprintf("%.1f (%d/%d)", clickFailurePct, clickFailures, clicks))
	return out
}

func (s *BrowserStats) verbTotals(names ...string) (count int, nonOK int) {
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	for _, v := range s.Verbs {
		if want[v.Verb] {
			count += v.Count
			nonOK += v.NonOK
		}
	}
	return count, nonOK
}

// AdoptionVerdict is a one-line human read of the typed-tool adoption, used by
// the CLI to make the snapshot-vs-eval number actionable rather than raw.
func (a AdoptionStats) AdoptionVerdict() string {
	switch {
	case a.Snapshot+a.Eval == 0:
		return "no discovery calls yet"
	case a.TypedToolPct >= 50:
		return "strong — snapshot is the primary discovery tool"
	case a.TypedToolPct >= 15:
		return "growing — snapshot used, but eval escape-hatch still dominates"
	default:
		return "low — agents still hand-roll eval instead of snapshot; surface snapshot harder"
	}
}

// TopVerbs returns at most n verbs (already sorted desc).
func (s *BrowserStats) TopVerbs(n int) []VerbStat {
	if n <= 0 || n >= len(s.Verbs) {
		return s.Verbs
	}
	return s.Verbs[:n]
}
