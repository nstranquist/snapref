package browserscript

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/nstranquist/pageskein/browser"
)

// FileTelemetry persists a run to the same surfaces ad-hoc `pageskein exec`
// uses: per-step rows in the command event log (events.jsonl, kinds
// browser.script / browser.step) so the observe stream and the Agent browser
// viewer pick script runs up for free, plus a per-run manifest under
// ~/.pageskein/browser/scripts/<run-id>.json.
//
// It is best-effort: a telemetry write error never aborts a run (the step
// already happened). Errors are retained on LastErr for callers that want to
// surface them.
type FileTelemetry struct {
	LastErr error
}

// NewFileTelemetry returns the production telemetry sink.
func NewFileTelemetry() *FileTelemetry { return &FileTelemetry{} }

func (t *FileTelemetry) note(err error) {
	if err != nil {
		t.LastErr = err
	}
}

// Begin records a browser.script start event.
func (t *FileTelemetry) Begin(rep *RunReport) {
	t.note(browser.AppendEvent(browser.ObserverEvent{
		Timestamp: rep.StartedAt,
		Kind:      browser.KindScript,
		Session:   rep.Session,
		Command: &browser.ObserverCommand{
			Command: "script:start",
			OK:      true,
			Data: map[string]any{
				"run_id": rep.RunID,
				"plan":   rep.Plan,
				"hash":   rep.PlanHash,
				"source": rep.Source,
			},
		},
	}))
}

// Step records one browser.step event per executed step.
func (t *FileTelemetry) Step(rep *RunReport, r Result) {
	exit := 0
	if !r.OK {
		exit = 1
	}
	data := map[string]any{
		"run_id": rep.RunID,
		"plan":   rep.Plan,
		"index":  r.Index,
	}
	if len(r.Captured) > 0 {
		keys := make([]string, 0, len(r.Captured))
		for k := range r.Captured {
			keys = append(keys, k)
		}
		data["captured_keys"] = keys
	}
	if r.Assert != nil {
		data["assert_ok"] = r.Assert.OK
	}
	if r.Label != "" {
		data["label"] = r.Label
	}
	t.note(browser.AppendEvent(browser.ObserverEvent{
		Kind:       browser.KindStep,
		Session:    rep.Session,
		Error:      r.Error,
		ErrorClass: r.ErrorClass,
		Command: &browser.ObserverCommand{
			Command:    "script:" + string(r.Verb),
			ExitCode:   exit,
			OK:         r.OK,
			DurationMS: r.DurationMS,
			Data:       data,
		},
	}))
}

// Finish records a browser.script end event and writes the run manifest.
func (t *FileTelemetry) Finish(rep *RunReport) {
	t.note(browser.AppendEvent(browser.ObserverEvent{
		Timestamp: rep.EndedAt,
		Kind:      browser.KindScript,
		Session:   rep.Session,
		Command: &browser.ObserverCommand{
			Command: "script:end",
			OK:      rep.OK,
			Data: map[string]any{
				"run_id": rep.RunID,
				"plan":   rep.Plan,
				"steps":  len(rep.Steps),
			},
		},
	}))
	t.note(t.writeManifest(rep))
}

func (t *FileTelemetry) writeManifest(rep *RunReport) error {
	dir, err := browser.ScriptRunsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(redactReportForManifest(rep), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rep.RunID+".json"), b, 0o600)
}

// ManifestPath returns the on-disk path for a run id's manifest.
func ManifestPath(runID string) (string, error) {
	dir, err := browser.ScriptRunsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, runID+".json"), nil
}

// ListRuns returns the run manifests on disk, newest filename first.
func ListRuns() ([]RunReport, error) {
	dir, err := browser.ScriptRunsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RunReport
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rep RunReport
		if json.Unmarshal(b, &rep) == nil {
			out = append(out, rep)
		}
	}
	return out, nil
}
