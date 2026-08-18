package browser

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Findings ledger — the agent-written outcome layer that the mechanical
// telemetry (events.jsonl) can't capture. events.jsonl records THAT a snapshot/
// screenshot/console ran; findings.jsonl records what the agent CONCLUDED and
// whether the page got better. One JSONL row per finding; `snapref stats`
// rolls it up alongside the computed adoption metrics so a run is judged on both
// "how well the tool was used" (computed) and "what it found / fixed" (written).
//
// JSONL on purpose: append-only, bounded, single-append/many-read, no joins.
// The catalog-serve JSONL→SQLite mirror is the upgrade path if an indexed UI
// ever needs it — not warranted here.

// FindingSeverities is the closed severity set (ascending).
var FindingSeverities = []string{"info", "low", "medium", "high", "critical"}

// FindingCategories is the suggested category set; "other" is the catch-all.
// Categories are validated against this set so the rollup stays meaningful.
var FindingCategories = []string{
	"js-error", "a11y", "responsive", "fragile-selector",
	"empty-state", "perf", "visual", "content", "other",
}

// FindingEvidence pins a finding to the telemetry/artifacts that prove it.
type FindingEvidence struct {
	SnapshotRef string `json:"snapshot_ref,omitempty"` // executable sr-…:eN ref from a snapshot inventory
	Screenshot  string `json:"screenshot,omitempty"`   // screenshot path
	Console     string `json:"console,omitempty"`      // console excerpt
	Selector    string `json:"selector,omitempty"`     // selector the finding concerns
}

// FindingRecord is one agent-written finding with its evidence and outcome.
type FindingRecord struct {
	Timestamp time.Time       `json:"ts"`
	RunID     string          `json:"run_id,omitempty"`
	Session   string          `json:"session,omitempty"`
	URL       string          `json:"url,omitempty"`
	View      string          `json:"view,omitempty"`     // logical view/route, e.g. "graph"
	Finding   string          `json:"finding"`            // required: what's wrong / observed
	Severity  string          `json:"severity"`           // info|low|medium|high|critical (default medium)
	Category  string          `json:"category,omitempty"` // one of FindingCategories
	Evidence  FindingEvidence `json:"evidence,omitempty"`
	Action    string          `json:"action_taken,omitempty"` // the fix applied
	Before    string          `json:"before,omitempty"`       // before measurement/state
	After     string          `json:"after,omitempty"`        // after measurement/state
	Verified  bool            `json:"verified,omitempty"`     // re-checked that the fix holds
}

// FindingsLogPath is the per-browser-home JSONL findings ledger.
func FindingsLogPath() (string, error) {
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "findings.jsonl"), nil
}

func validSeverity(s string) bool {
	for _, v := range FindingSeverities {
		if v == s {
			return true
		}
	}
	return false
}

func validCategory(c string) bool {
	for _, v := range FindingCategories {
		if v == c {
			return true
		}
	}
	return false
}

// NormalizeFinding validates and defaults a record in place, returning an error
// for unrecoverable problems (empty finding, bad severity/category). It stamps
// Timestamp when zero and defaults Severity to "medium".
func NormalizeFinding(rec *FindingRecord) error {
	rec.Finding = strings.TrimSpace(rec.Finding)
	if rec.Finding == "" {
		return fmt.Errorf("finding text is required")
	}
	rec.Severity = strings.ToLower(strings.TrimSpace(rec.Severity))
	if rec.Severity == "" {
		rec.Severity = "medium"
	}
	if !validSeverity(rec.Severity) {
		return fmt.Errorf("severity %q not in %s", rec.Severity, strings.Join(FindingSeverities, "|"))
	}
	rec.Category = strings.ToLower(strings.TrimSpace(rec.Category))
	if rec.Category != "" && !validCategory(rec.Category) {
		return fmt.Errorf("category %q not in %s", rec.Category, strings.Join(FindingCategories, "|"))
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	return nil
}

// AppendFinding validates and appends a finding to the ledger.
func AppendFinding(rec FindingRecord) error {
	if err := NormalizeFinding(&rec); err != nil {
		return err
	}
	path, err := FindingsLogPath()
	if err != nil {
		return err
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return appendAndRotateJSONL(path, body, 0o600)
}

// FindingsFilter narrows ReadFindings.
type FindingsFilter struct {
	Session  string
	Severity string
	Category string
	Since    time.Duration
}

// ReadFindings returns ledger records (oldest first) matching the filter. A
// missing ledger is not an error — it returns an empty slice.
func ReadFindings(filter FindingsFilter) ([]FindingRecord, error) {
	path, err := FindingsLogPath()
	if err != nil {
		return nil, err
	}
	var cutoff time.Time
	if filter.Since > 0 {
		cutoff = time.Now().Add(-filter.Since)
	}
	out := []FindingRecord{}
	err = forEachJSONLFamilyLine(path, func(line []byte) error {
		var rec FindingRecord
		if json.Unmarshal(line, &rec) != nil {
			return nil // skip a torn last line (concurrent append)
		}
		if filter.Session != "" && rec.Session != filter.Session {
			return nil
		}
		if filter.Severity != "" && rec.Severity != strings.ToLower(filter.Severity) {
			return nil
		}
		if filter.Category != "" && rec.Category != strings.ToLower(filter.Category) {
			return nil
		}
		if !cutoff.IsZero() && rec.Timestamp.Before(cutoff) {
			return nil
		}
		out = append(out, rec)
		return nil
	})
	return out, err
}
