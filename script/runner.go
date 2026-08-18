package browserscript

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nstranquist/snapref/browser"
)

// Result is the per-step verdict: the streamed immediate-feedback unit AND an
// audit row in the run manifest.
type Result struct {
	SchemaVersion      int                `json:"schema_version,omitempty"`
	BuildVersion       string             `json:"build_version,omitempty"`
	Index              int                `json:"index"`
	Verb               Verb               `json:"verb"`
	Label              string             `json:"label,omitempty"`
	OK                 bool               `json:"ok"`
	Skipped            bool               `json:"skipped,omitempty"`
	DurationMS         int64              `json:"duration_ms"`
	Captured           map[string]any     `json:"captured,omitempty"`
	Assert             *AssertOutcome     `json:"assert,omitempty"`
	Screenshot         string             `json:"screenshot,omitempty"`
	ScreenshotMode     ScreenshotMode     `json:"screenshot_mode,omitempty"`
	ScreenshotWidthPX  int                `json:"screenshot_width_px,omitempty"`
	ScreenshotHeightPX int                `json:"screenshot_height_px,omitempty"`
	Error              string             `json:"error,omitempty"`
	ErrorClass         browser.ErrorClass `json:"error_class,omitempty"`
}

// RunReport is the full outcome of a Plan run. The cmd layer persists it as the
// per-run manifest under ~/.snapref/browser/scripts/<run-id>.json.
type RunReport struct {
	SchemaVersion int               `json:"schema_version,omitempty"`
	BuildVersion  string            `json:"build_version,omitempty"`
	RunID         string            `json:"run_id"`
	Plan          string            `json:"plan"`
	PlanHash      string            `json:"plan_hash"`
	Source        string            `json:"source"` // yaml | json | star | sdk
	Session       string            `json:"session"`
	StartedAt     time.Time         `json:"started_at"`
	EndedAt       time.Time         `json:"ended_at"`
	OK            bool              `json:"ok"`
	Steps         []Result          `json:"steps"`
	Outputs       []map[string]any  `json:"outputs,omitempty"` // emit() rows (Starlark)
	Vars          map[string]string `json:"vars,omitempty"`    // redacted snapshot
}

// Telemetry receives run lifecycle events for persistence (events.jsonl rows +
// run manifest). A nil Telemetry disables persistence (used in tests). The
// engine drives the calls; FileTelemetry is the production implementation.
type Telemetry interface {
	Begin(report *RunReport)
	Step(report *RunReport, r Result)
	Finish(report *RunReport)
}

// RunOptions controls a single Run.
type RunOptions struct {
	AllowDrive     bool              // enable all page actuation and caller-supplied page code
	AllowHostWrite bool              // allow screenshot steps to select a host filesystem path
	AllowedVerbs   map[Verb]bool     // per-verb allow (in addition to AllowDrive)
	MaxSteps       int               // cap executed steps (0 = all)
	Source         string            // "yaml" | "json" | "star" | "sdk" for the manifest
	Session        string            // session for the Starlark path (Plan path uses plan.Session)
	Vars           map[string]string // params for the Starlark path (Plan path uses plan.Vars)
	Sink           io.Writer         // per-step JSONL feedback; nil => io.Discard
	Telemetry      Telemetry         // optional persistence hook
	Now            func() time.Time
	RunID          string // explicit run id; empty => derived from Now + counter
}

// Runner executes Plans against a browser.Driver. A Runner is not safe for
// concurrent Run calls (it carries a monotonic id counter); use one per run.
type Runner struct {
	drv browser.Driver
	seq int64
}

// NewRunner returns a Runner backed by d.
func NewRunner(d browser.Driver) *Runner { return &Runner{drv: d} }

// Run executes the plan top to bottom against an already-open session, streaming
// one JSON Result line to opts.Sink as each step completes. It stops at the
// first failing step unless that step sets on_error: continue. The returned
// RunReport.OK is true only if every executed step passed.
func (r *Runner) Run(ctx context.Context, plan Plan, opts RunOptions) (*RunReport, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if opts.Source == "" {
		opts.Source = SourceSDK
	}
	e := r.begin(ctx, plan.Name, plan.Session, plan.Hash(), plan.Vars, opts)

	limit := len(plan.Steps)
	if opts.MaxSteps > 0 && opts.MaxSteps < limit {
		limit = opts.MaxSteps
	}
	for i := 0; i < limit; i++ {
		st := plan.Steps[i]
		if st.Verb == VerbScreenshot && st.ScreenshotMode == "" {
			st.ScreenshotMode = plan.ScreenshotMode
		}
		_, res := e.run(st)
		if !res.OK && st.OnError != OnErrorContinue {
			break
		}
	}
	return e.finish(), nil
}

// execCtx carries the live run state shared by the Plan loop and the Starlark
// built-ins, so both surfaces produce identical telemetry and feedback.
type execCtx struct {
	r       *Runner
	ctx     context.Context
	session string
	sc      *scope
	opts    RunOptions
	now     func() time.Time
	sink    io.Writer
	report  *RunReport
	idx     int
}

func (r *Runner) begin(ctx context.Context, name, session, hash string, vars map[string]string, opts RunOptions) *execCtx {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sink := opts.Sink
	if sink == nil {
		sink = io.Discard
	}
	start := now().UTC()
	r.seq++
	runID := opts.RunID
	if runID == "" {
		runID = makeRunID(start, r.seq)
	}
	e := &execCtx{
		r:       r,
		ctx:     ctx,
		session: session,
		sc:      newScope(vars),
		opts:    opts,
		now:     now,
		sink:    sink,
		report: &RunReport{
			SchemaVersion: browser.BrowserTelemetrySchemaVersion,
			BuildVersion:  browser.BrowserBuildVersion(),
			RunID:         runID,
			Plan:          name,
			PlanHash:      hash,
			Source:        opts.Source,
			Session:       session,
			StartedAt:     start,
			OK:            true,
		},
	}
	if opts.Telemetry != nil {
		opts.Telemetry.Begin(e.report)
	}
	return e
}

// run executes one step, records it, and returns the step's value (for the
// Starlark built-ins to branch on) alongside the verdict.
func (e *execCtx) run(st Step) (string, Result) {
	res := Result{SchemaVersion: browser.BrowserTelemetrySchemaVersion, BuildVersion: browser.BrowserBuildVersion(), Index: e.idx, Verb: st.Verb, Label: st.Label}
	e.idx++
	start := e.now()

	if RequiresDrive(st) && !e.opts.AllowDrive && !e.opts.AllowedVerbs[st.Verb] {
		res.OK = false
		res.Error = driveGatedMsg(st.Verb)
		res.ErrorClass = browser.ErrorClassPolicy
		res.DurationMS = elapsed(e.now, start)
		return e.record("", res)
	}
	if RequiresHostWrite(st) && !e.opts.AllowHostWrite {
		res.OK = false
		res.Error = "screenshot host path is disabled; omit path for a unique managed temp file or pass --allow-host-write"
		res.ErrorClass = browser.ErrorClassPolicy
		res.DurationMS = elapsed(e.now, start)
		return e.record("", res)
	}

	stepCtx := e.ctx
	if to := st.Timeout.D(); to > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(e.ctx, to)
		defer cancel()
	}

	var artifact *browser.ScreenshotArtifact
	var value, screenshot string
	var err error
	if st.Verb == VerbScreenshot {
		value, screenshot, artifact, err = e.r.execScreenshot(stepCtx, e.session, st, e.sc)
	} else {
		value, screenshot, err = e.r.execVerb(stepCtx, e.session, st, e.sc)
	}
	res.Screenshot = screenshot
	if artifact != nil {
		res.ScreenshotMode = st.ScreenshotMode
		if res.ScreenshotMode == "" {
			res.ScreenshotMode = ScreenshotModeFullPage
		}
		res.ScreenshotWidthPX = artifact.WidthPX
		res.ScreenshotHeightPX = artifact.HeightPX
	}
	if err != nil {
		res.OK = false
		res.Error = err.Error()
		res.ErrorClass = browser.ClassifyError(err)
		res.DurationMS = elapsed(e.now, start)
		return e.record(value, res)
	}
	res.OK = true

	if st.Verb == VerbAssertCount {
		ao := evalCountAssertion(value, st.Count)
		res.Assert = &ao
		if !ao.OK {
			res.OK = false
			res.Error = "assertion failed: " + ao.Expected
			res.ErrorClass = browser.ErrorClassAssertion
		}
	} else if needsAssertion(st.Verb) || !st.Assert.empty() {
		ao := evalAssertion(value, st.Assert)
		res.Assert = &ao
		if !ao.OK {
			res.OK = false
			res.Error = "assertion failed: " + ao.Expected
			res.ErrorClass = browser.ErrorClassAssertion
		}
	}

	if st.Capture != "" {
		e.sc.set(st.Capture, value)
		res.Captured = map[string]any{st.Capture: value}
	}

	res.DurationMS = elapsed(e.now, start)
	return e.record(value, res)
}

func (e *execCtx) record(value string, res Result) (string, Result) {
	e.report.Steps = append(e.report.Steps, res)
	emitLine(e.sink, res)
	if e.opts.Telemetry != nil {
		e.opts.Telemetry.Step(e.report, res)
	}
	if !res.OK {
		e.report.OK = false
	}
	return value, res
}

// emit records a structured output row (Starlark emit()).
func (e *execCtx) emit(obj map[string]any) {
	e.report.Outputs = append(e.report.Outputs, obj)
	b, err := json.Marshal(map[string]any{"emit": obj})
	if err != nil {
		return
	}
	_, _ = e.sink.Write(append(b, '\n'))
}

func (e *execCtx) finish() *RunReport {
	e.report.EndedAt = e.now().UTC()
	e.report.Vars = redactVars(e.sc.snapshot())
	if e.opts.Telemetry != nil {
		e.opts.Telemetry.Finish(e.report)
	}
	return e.report
}

func (r *Runner) execVerb(ctx context.Context, session string, st Step, sc *scope) (value string, screenshot string, err error) {
	switch st.Verb {
	case VerbNavigate:
		return "", "", r.drv.Navigate(ctx, session, sc.expand(st.URL), browser.NavigateOptions{
			Reload:      st.Reload,
			BypassCache: st.BypassCache,
		})
	case VerbClick:
		return "", "", r.drv.Click(ctx, session, sc.expand(st.Selector))
	case VerbClickText:
		return "", "", r.drv.ClickText(ctx, session, sc.expand(st.Text))
	case VerbClickXPath:
		return "", "", r.drv.ClickXPath(ctx, session, sc.expand(st.Selector))
	case VerbType:
		return "", "", r.drv.Type(ctx, session, sc.expand(st.Selector), sc.expand(st.Text))
	case VerbPress:
		return "", "", r.drv.Press(ctx, session, sc.expand(st.Key))
	case VerbWait:
		return "", "", r.drv.WaitVisible(ctx, session, sc.expand(st.Selector))
	case VerbAssertVisible:
		return "visible", "", r.drv.WaitVisible(ctx, session, sc.expand(st.Selector))
	case VerbWaitText:
		return "", "", r.drv.WaitText(ctx, session, sc.expand(st.Text))
	case VerbWaitXPath:
		return "", "", r.drv.WaitXPath(ctx, session, sc.expand(st.Selector))
	case VerbText, VerbAssertText:
		v, e := r.drv.Text(ctx, session, sc.expand(st.Selector))
		return v, "", e
	case VerbEval:
		v, e := r.drv.Eval(ctx, session, sc.expand(st.JS))
		return v, "", e
	case VerbFetch:
		v, e := r.drv.Fetch(ctx, session, sc.expand(st.URL))
		return v, "", e
	case VerbViewport:
		return fmt.Sprintf("%dx%d", st.Width, st.Height), "", r.drv.Viewport(ctx, session, st.Width, st.Height)
	case VerbConsole:
		return r.execConsole(ctx, session, st, sc)
	case VerbSnapshot:
		return r.execSnapshot(ctx, session, st, sc)
	case VerbAssertCount:
		v, e := r.countSelector(ctx, session, sc.expand(st.Selector))
		return v, "", e
	case VerbAssertURL:
		u, e := r.currentURL(ctx, session)
		return u, "", e
	}
	return "", "", fmt.Errorf("unhandled verb %q", st.Verb)
}

func (r *Runner) execScreenshot(ctx context.Context, session string, st Step, sc *scope) (value string, screenshot string, artifact *browser.ScreenshotArtifact, err error) {
	path := sc.expand(st.Path)
	captureBeyondViewport := st.ScreenshotMode.captureBeyondViewport()
	captureDriver, ok := r.drv.(browser.ScreenshotCaptureDriver)
	if !ok {
		if !captureBeyondViewport {
			return "", "", nil, fmt.Errorf("browser driver does not support viewport screenshot mode")
		}
		written, captureErr := r.drv.Screenshot(ctx, session, path)
		if captureErr != nil {
			return "", "", nil, captureErr
		}
		fallback := &browser.ScreenshotArtifact{Path: written}
		return written, written, fallback, nil
	}
	captured, captureErr := captureDriver.ScreenshotWithCapture(ctx, session, path, captureBeyondViewport)
	if captureErr != nil {
		return "", "", nil, captureErr
	}
	return captured.Path, captured.Path, &captured, nil
}

func (r *Runner) countSelector(ctx context.Context, session, selector string) (string, error) {
	selector, err := r.drv.ResolveTarget(ctx, session, selector)
	if err != nil {
		return "", err
	}
	expression, err := browser.TargetCountExpression(selector)
	if err != nil {
		return "", err
	}
	raw, err := r.drv.Eval(ctx, session, expression)
	if err != nil {
		return "", err
	}
	var n int
	if json.Unmarshal([]byte(raw), &n) == nil {
		return fmt.Sprintf("%d", n), nil
	}
	return raw, nil
}

func selectorCountJS(selector string) string {
	expression, _ := browser.TargetCountExpression(selector)
	return expression
}

// execSnapshot runs a read-only page-inventory step, returning the snapshot as a
// JSON string so it can be captured into a var or asserted on.
func (r *Runner) execSnapshot(ctx context.Context, session string, st Step, sc *scope) (string, string, error) {
	sa := st.Snapshot
	if sa == nil {
		sa = &SnapshotArgs{}
	}
	roles := make([]string, 0, len(sa.Roles))
	for _, role := range sa.Roles {
		roles = append(roles, sc.expand(role))
	}
	snap, err := r.drv.Snapshot(ctx, session, browser.SnapshotOptions{
		Max:             sa.Max,
		Roles:           roles,
		Filter:          sc.expand(sa.Filter),
		IncludeHidden:   sa.IncludeHidden,
		IncludeRect:     sa.IncludeRect,
		WaitForSelector: sc.expand(sa.WaitForSelector),
		Settle:          sa.Settle.D(),
	})
	if err != nil {
		return "", "", err
	}
	b, _ := json.Marshal(snap)
	return string(b), "", nil
}

func (r *Runner) execConsole(ctx context.Context, session string, st Step, sc *scope) (string, string, error) {
	ca := st.Console
	if ca == nil {
		ca = &ConsoleArgs{}
	}
	entries, err := r.drv.Console(ctx, session, browser.ConsoleCaptureOptions{
		Navigate: sc.expand(ca.Navigate),
		Reload:   ca.Reload,
		Eval:     sc.expand(ca.Eval),
		Window:   ca.Window.D(),
		Level:    ca.Level,
		Kind:     ca.Kind,
	})
	if err != nil {
		return "", "", err
	}
	b, _ := json.Marshal(entries)
	value := string(b)
	if ca.FailOnError {
		for _, e := range entries {
			if e.Kind == "exception" || e.Level == "error" {
				return value, "", fmt.Errorf("console captured %s: %s", e.Kind, truncate(e.Text, 200))
			}
		}
	}
	return value, "", nil
}

// currentURL reads location.href via the driver's Eval (which returns a
// JSON-encoded value).
func (r *Runner) currentURL(ctx context.Context, session string) (string, error) {
	raw, err := r.drv.Eval(ctx, session, "location.href")
	if err != nil {
		return "", err
	}
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		return s, nil
	}
	return raw, nil
}

func needsAssertion(v Verb) bool { return v == VerbAssertText || v == VerbAssertURL }

func driveGatedMsg(v Verb) string {
	return fmt.Sprintf("verb %q is drive-gated; pass --allow-drive (or allow it explicitly) to run mutating steps", v)
}

func elapsed(now func() time.Time, start time.Time) int64 {
	return now().Sub(start).Milliseconds()
}

// makeRunID is deterministic within a process (fixed clock + monotonic seq →
// stable id, used by tests) while the pid segment avoids manifest-filename
// collisions between two `script run` processes started in the same millisecond.
func makeRunID(t time.Time, seq int64) string {
	return fmt.Sprintf("run-%s-%d-%03d", t.UTC().Format("20060102T150405.000"), os.Getpid(), seq)
}

func emitLine(w io.Writer, r Result) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	b = append(b, '\n')
	_, _ = w.Write(b)
}
