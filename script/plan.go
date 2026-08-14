// Package browserscript is the engine behind `pageskein script`: it runs a
// Plan — an ordered sequence of browser verbs — as ONE unit against an
// already-open session, streaming a per-step JSON verdict for immediate
// feedback and (via the cmd layer) writing the same telemetry/audit we keep for
// ad-hoc `pageskein exec`.
//
// The engine is the substrate for all three authoring surfaces: the Go SDK
// (build a Plan directly), the YAML/JSON loader, and the Starlark loader. Each
// produces a Plan and hands it to a Runner. The Runner depends only on the
// browser.Driver interface, so the whole engine is unit-testable with a fake
// driver — no Chrome required.
package browserscript

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/pageskein/contract"
)

// Verb is the closed instruction set. It is 1:1 with browser.Driver plus the
// assertion verbs.
type Verb string

const (
	VerbNavigate      Verb = Verb(browsercontract.Navigate)
	VerbClick         Verb = Verb(browsercontract.Click)
	VerbClickText     Verb = Verb(browsercontract.ClickText)
	VerbClickXPath    Verb = Verb(browsercontract.ClickXPath)
	VerbType          Verb = Verb(browsercontract.Type)
	VerbPress         Verb = Verb(browsercontract.Press)
	VerbWait          Verb = Verb(browsercontract.Wait)
	VerbWaitText      Verb = Verb(browsercontract.WaitText)
	VerbWaitXPath     Verb = Verb(browsercontract.WaitXPath)
	VerbText          Verb = Verb(browsercontract.Text)
	VerbEval          Verb = Verb(browsercontract.Eval)
	VerbFetch         Verb = Verb(browsercontract.Fetch)
	VerbConsole       Verb = Verb(browsercontract.Console)
	VerbSnapshot      Verb = Verb(browsercontract.Snapshot)
	VerbScreenshot    Verb = Verb(browsercontract.Screenshot)
	VerbViewport      Verb = Verb(browsercontract.Viewport)
	VerbAssertVisible Verb = Verb(browsercontract.AssertVisible)
	VerbAssertCount   Verb = Verb(browsercontract.AssertCount)
	VerbAssertText    Verb = Verb(browsercontract.AssertText)
	VerbAssertURL     Verb = Verb(browsercontract.AssertURL)
)

// AllVerbs is the canonical ordered list (used by validate / new / help).
var AllVerbs = scriptVerbsFromContract()

func scriptVerbsFromContract() []Verb {
	operations := browsercontract.ScriptOperations()
	out := make([]Verb, 0, len(operations))
	for _, operation := range operations {
		out = append(out, Verb(operation.ScriptVerb))
	}
	return out
}

// IsMutating reports whether v actuates the page (and is therefore drive-gated).
func IsMutating(v Verb) bool {
	operation, ok := browsercontract.ByScript(string(v))
	return ok && operation.Permission == browsercontract.PermissionDrive
}

// RequiresDrive classifies a concrete step, including conditional console
// actuation that cannot be represented by a verb-only lookup.
func RequiresDrive(st Step) bool {
	if IsMutating(st.Verb) {
		return true
	}
	operation, ok := browsercontract.ByScript(string(st.Verb))
	return ok && operation.Permission == browsercontract.PermissionConditionalDrive && st.Console != nil &&
		(st.Console.Navigate != "" || st.Console.Reload || st.Console.Eval != "")
}

// RequiresHostWrite reports whether a screenshot selects a caller-controlled
// filesystem path instead of the engine's unique managed temp location.
func RequiresHostWrite(st Step) bool {
	operation, ok := browsercontract.ByScript(string(st.Verb))
	return ok && operation.Permission == browsercontract.PermissionConditionalWrite && strings.TrimSpace(st.Path) != ""
}

// knownVerb reports whether v is part of the closed instruction set.
func knownVerb(v Verb) bool {
	_, ok := browsercontract.ByScript(string(v))
	return ok
}

// ErrorPolicy controls what happens when a step fails.
type ErrorPolicy string

const (
	// OnErrorStop aborts the plan at the first failing step (default).
	OnErrorStop ErrorPolicy = ""
	// OnErrorContinue records the failure and proceeds to the next step.
	OnErrorContinue ErrorPolicy = "continue"
)

// ScreenshotMode makes the intended evidence shape explicit. The empty value
// preserves PageSkein's historical full-page default.
type ScreenshotMode string

const (
	ScreenshotModeFullPage ScreenshotMode = "full-page"
	ScreenshotModeViewport ScreenshotMode = "viewport"
)

func (m ScreenshotMode) valid() bool {
	return m == "" || m == ScreenshotModeFullPage || m == ScreenshotModeViewport
}

func (m ScreenshotMode) captureBeyondViewport() bool {
	return m != ScreenshotModeViewport
}

// Assertion is a value check. Exactly one mode is normally set; if more than one
// is set they are AND-ed.
type Assertion struct {
	Equals   string `json:"equals,omitempty"`
	Contains string `json:"contains,omitempty"`
	Regex    string `json:"regex,omitempty"`
}

func (a *Assertion) empty() bool {
	return a == nil || (a.Equals == "" && a.Contains == "" && a.Regex == "")
}

// CountAssertion checks a selector match count. Pointers allow zero to be an
// intentional expected value, e.g. command-palette count equals 0 after Escape.
type CountAssertion struct {
	Equals  *int `json:"equals,omitempty"`
	AtLeast *int `json:"at_least,omitempty"`
	AtMost  *int `json:"at_most,omitempty"`
}

func (a *CountAssertion) empty() bool {
	return a == nil || (a.Equals == nil && a.AtLeast == nil && a.AtMost == nil)
}

// ConsoleArgs configures a console-capture step (mirrors
// browser.ConsoleCaptureOptions).
type ConsoleArgs struct {
	Navigate    string   `json:"navigate,omitempty"`
	Reload      bool     `json:"reload,omitempty"`
	Eval        string   `json:"eval,omitempty"`
	Window      Duration `json:"window,omitempty"`
	Level       string   `json:"level,omitempty"`         // all | warning | error
	Kind        string   `json:"kind,omitempty"`          // all | js | log
	FailOnError bool     `json:"fail_on_error,omitempty"` // fail step on any error/exception
}

// SnapshotArgs configures a snapshot step (mirrors browser.SnapshotOptions).
type SnapshotArgs struct {
	Max             int      `json:"max,omitempty"`
	Roles           []string `json:"roles,omitempty"`
	Filter          string   `json:"filter,omitempty"`
	IncludeHidden   bool     `json:"include_hidden,omitempty"`
	IncludeRect     bool     `json:"include_rect,omitempty"`
	WaitForSelector string   `json:"wait_for_selector,omitempty"`
	Settle          Duration `json:"settle,omitempty"`
}

// Step is one instruction. Only the fields relevant to the Verb are used.
type Step struct {
	Verb           Verb            `json:"verb"`
	Selector       string          `json:"selector,omitempty"`
	URL            string          `json:"url,omitempty"`
	Reload         bool            `json:"reload,omitempty"`
	BypassCache    bool            `json:"bypass_cache,omitempty"`
	Text           string          `json:"text,omitempty"`
	Key            string          `json:"key,omitempty"`
	JS             string          `json:"js,omitempty"`
	Path           string          `json:"path,omitempty"`            // screenshot output path
	ScreenshotMode ScreenshotMode  `json:"screenshot_mode,omitempty"` // full-page (default) | viewport
	Timeout        Duration        `json:"timeout,omitempty"`         // per-step ceiling; default 30s
	Capture        string          `json:"capture,omitempty"`         // bind the step's value to a var
	Assert         *Assertion      `json:"assert,omitempty"`          // check the step's value
	Label          string          `json:"label,omitempty"`           // human/agent annotation
	OnError        ErrorPolicy     `json:"on_error,omitempty"`
	Console        *ConsoleArgs    `json:"console,omitempty"`
	Snapshot       *SnapshotArgs   `json:"snapshot,omitempty"`
	Count          *CountAssertion `json:"count,omitempty"`
	Width          int             `json:"width,omitempty"`  // viewport width (device px)
	Height         int             `json:"height,omitempty"` // viewport height (device px)
}

// Plan is a named, ordered sequence of steps against one open session.
type Plan struct {
	Name           string            `json:"name"`
	Session        string            `json:"session"`
	Vars           map[string]string `json:"vars,omitempty"`
	ScreenshotMode ScreenshotMode    `json:"screenshot_mode,omitempty"`
	Steps          []Step            `json:"steps"`
}

// Duration is a JSON/YAML-friendly time.Duration that accepts "30s" strings as
// well as integer nanoseconds.
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// MarshalJSON renders the duration as a Go duration string ("30s").
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts either a duration string ("1500ms", "30s") or a numeric
// nanosecond count.
func (d *Duration) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		*d = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("invalid duration %s: %w", b, err)
	}
	*d = Duration(n)
	return nil
}

// Validate checks the plan is structurally sound before execution. It does not
// touch a browser.
func (p Plan) Validate() error {
	if strings.TrimSpace(p.Session) == "" {
		return fmt.Errorf("plan %q: session is required", p.Name)
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan %q: no steps", p.Name)
	}
	if !p.ScreenshotMode.valid() {
		return fmt.Errorf("plan %q: invalid screenshot_mode %q (want full-page or viewport)", p.Name, p.ScreenshotMode)
	}
	for i, st := range p.Steps {
		if err := st.validate(); err != nil {
			return fmt.Errorf("plan %q step %d (%s): %w", p.Name, i, st.Verb, err)
		}
	}
	return nil
}

func (s Step) validate() error {
	if !knownVerb(s.Verb) {
		return fmt.Errorf("unknown verb %q", s.Verb)
	}
	if !s.ScreenshotMode.valid() {
		return fmt.Errorf("invalid screenshot_mode %q (want full-page or viewport)", s.ScreenshotMode)
	}
	if s.ScreenshotMode != "" && s.Verb != VerbScreenshot {
		return fmt.Errorf("screenshot_mode is only valid for screenshot steps")
	}
	switch s.Verb {
	case VerbNavigate, VerbFetch:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("requires url")
		}
	case VerbClick, VerbWait, VerbText, VerbClickXPath, VerbWaitXPath, VerbAssertVisible:
		if strings.TrimSpace(s.Selector) == "" {
			return fmt.Errorf("requires selector")
		}
	case VerbAssertCount:
		if strings.TrimSpace(s.Selector) == "" {
			return fmt.Errorf("requires selector")
		}
		if s.Count.empty() {
			return fmt.Errorf("requires count assertion (equals/at_least/at_most)")
		}
	case VerbWaitText:
		if strings.TrimSpace(s.Text) == "" {
			return fmt.Errorf("requires text (the visible text to wait for)")
		}
	case VerbType:
		if strings.TrimSpace(s.Selector) == "" {
			return fmt.Errorf("requires selector")
		}
	case VerbClickText:
		if strings.TrimSpace(s.Text) == "" {
			return fmt.Errorf("requires text (the visible label to click)")
		}
	case VerbPress:
		if strings.TrimSpace(s.Key) == "" {
			return fmt.Errorf("requires key")
		}
	case VerbViewport:
		if s.Width <= 0 || s.Height <= 0 {
			return fmt.Errorf("requires positive width and height (device px)")
		}
	case VerbEval:
		if strings.TrimSpace(s.JS) == "" {
			return fmt.Errorf("requires js")
		}
	case VerbAssertText:
		if strings.TrimSpace(s.Selector) == "" {
			return fmt.Errorf("requires selector")
		}
		if s.Assert.empty() {
			return fmt.Errorf("requires an assertion (equals/contains/regex)")
		}
	case VerbAssertURL:
		if s.Assert.empty() {
			return fmt.Errorf("requires an assertion (equals/contains/regex)")
		}
	}
	if s.OnError != OnErrorStop && s.OnError != OnErrorContinue {
		return fmt.Errorf("invalid on_error %q (want \"\" or \"continue\")", s.OnError)
	}
	return nil
}

// Hash returns a stable sha256 of the plan's normalized JSON, used for audit
// provenance. Vars are excluded so the same flow with different params hashes
// identically.
func (p Plan) Hash() string {
	norm := Plan{Name: p.Name, Session: p.Session, ScreenshotMode: p.ScreenshotMode, Steps: p.Steps}
	b, _ := json.Marshal(norm)
	sum := sha256.Sum256(b)
	return "sha256:" + fmt.Sprintf("%x", sum[:])
}

// varNames returns the sorted parameter names (helper for deterministic output).
func (p Plan) varNames() []string {
	out := make([]string, 0, len(p.Vars))
	for k := range p.Vars {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
