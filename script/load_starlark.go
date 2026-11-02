package browserscript

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/nstranquist/snapref/browser"
)

// starFileOptions enables module-level control flow (if/for/while) and global
// reassignment so playbooks read like ordinary Python-ish scripts rather than
// being forced into a single function. The same options are used for both
// execution and validation so a script that validates also runs.
func starFileOptions() *syntax.FileOptions {
	return &syntax.FileOptions{
		TopLevelControl: true,
		GlobalReassign:  true,
		While:           true,
		Set:             true,
	}
}

// ParseStarlark syntax-checks a Starlark playbook without executing it (used by
// `script validate`).
func ParseStarlark(src []byte, name string) error {
	_, err := starFileOptions().Parse(name, src, 0)
	return err
}

// RunStarlarkFile reads and eagerly executes a .star playbook against an
// already-open session.
func RunStarlarkFile(ctx context.Context, drv browser.Driver, path string, opts RunOptions) (*RunReport, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return RunStarlark(ctx, drv, src, filepath.Base(path), opts)
}

// RunStarlark eagerly executes a Starlark playbook: the built-ins drive the
// browser live (so `if text(sel) == x:` branches on real page state), each
// driving call records a step + streams a verdict, and assertion/drive-gating
// failures raise a Starlark error that halts the script. Starlark itself stays
// pure — only the injected, audited built-ins touch the browser.
func RunStarlark(ctx context.Context, drv browser.Driver, src []byte, name string, opts RunOptions) (*RunReport, error) {
	if strings.TrimSpace(opts.Session) == "" {
		return nil, fmt.Errorf("starlark playbook requires a session (pass --session)")
	}
	opts.Source = SourceStar

	r := NewRunner(drv)
	sum := sha256.Sum256(src)
	hash := fmt.Sprintf("sha256:%x", sum[:])
	e := r.begin(ctx, name, opts.Session, hash, opts.Vars, opts)

	thread := &starlark.Thread{Name: name}
	predeclared := starlarkBuiltins(e, opts.MaxSteps)
	predeclared["params"] = paramsDict(opts.Vars)

	_, execErr := starlark.ExecFileOptions(starFileOptions(), thread, name, src, predeclared)
	rep := e.finish()
	if execErr != nil {
		if rep.OK {
			rep.OK = false
		}
		if ee, ok := execErr.(*starlark.EvalError); ok {
			return rep, fmt.Errorf("%s", ee.Backtrace())
		}
		return rep, execErr
	}
	return rep, nil
}

// starlarkBuiltins returns the browser-driving built-ins bound to one run.
func starlarkBuiltins(e *execCtx, maxSteps int) starlark.StringDict {
	do := func(st Step) (string, error) {
		// Eager Starlark skips the upfront Plan.Validate(), so validate each
		// step here — keeps the contract (required fields, non-empty
		// assertions) identical to the YAML/JSON path.
		if err := st.validate(); err != nil {
			return "", err
		}
		if maxSteps > 0 && e.idx >= maxSteps {
			return "", fmt.Errorf("max steps (%d) exceeded", maxSteps)
		}
		v, res := e.run(st)
		if !res.OK {
			return "", fmt.Errorf("%s", res.Error)
		}
		return v, nil
	}

	action := func(verb Verb, build func(starlark.Tuple, []starlark.Tuple) (Step, error), value bool) *starlark.Builtin {
		return starlark.NewBuiltin(string(verb), func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kw []starlark.Tuple) (starlark.Value, error) {
			st, err := build(args, kw)
			if err != nil {
				return nil, err
			}
			v, err := do(st)
			if err != nil {
				return nil, err
			}
			if value {
				return starlark.String(v), nil
			}
			return starlark.None, nil
		})
	}

	return starlark.StringDict{
		"nav": action(VerbNavigate, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var url string
			var reload, bypassCache bool
			err := starlark.UnpackArgs("nav", a, kw, "url", &url, "reload?", &reload, "bypass_cache?", &bypassCache)
			return Step{Verb: VerbNavigate, URL: url, Reload: reload, BypassCache: bypassCache}, err
		}, false),
		"click": action(VerbClick, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var sel string
			err := starlark.UnpackArgs("click", a, kw, "selector", &sel)
			return Step{Verb: VerbClick, Selector: sel}, err
		}, false),
		"click_text": action(VerbClickText, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var text string
			err := starlark.UnpackArgs("click_text", a, kw, "text", &text)
			return Step{Verb: VerbClickText, Text: text}, err
		}, false),
		"click_xpath": action(VerbClickXPath, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var xpath string
			err := starlark.UnpackArgs("click_xpath", a, kw, "xpath", &xpath)
			return Step{Verb: VerbClickXPath, Selector: xpath}, err
		}, false),
		"fill": action(VerbType, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var sel, text string
			err := starlark.UnpackArgs("fill", a, kw, "selector", &sel, "text", &text)
			return Step{Verb: VerbType, Selector: sel, Text: text}, err
		}, false),
		"select_option": action(VerbSelect, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var sel, option string
			err := starlark.UnpackArgs("select_option", a, kw, "selector", &sel, "option", &option)
			return Step{Verb: VerbSelect, Selector: sel, Option: option}, err
		}, false),
		"press": action(VerbPress, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var key string
			err := starlark.UnpackArgs("press", a, kw, "key", &key)
			return Step{Verb: VerbPress, Key: key}, err
		}, false),
		"wait": action(VerbWait, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var sel string
			err := starlark.UnpackArgs("wait", a, kw, "selector", &sel)
			return Step{Verb: VerbWait, Selector: sel}, err
		}, false),
		"assert_visible": action(VerbAssertVisible, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var sel string
			err := starlark.UnpackArgs("assert_visible", a, kw, "selector", &sel)
			return Step{Verb: VerbAssertVisible, Selector: sel}, err
		}, false),
		"wait_text": action(VerbWaitText, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var text string
			err := starlark.UnpackArgs("wait_text", a, kw, "text", &text)
			return Step{Verb: VerbWaitText, Text: text}, err
		}, false),
		"wait_xpath": action(VerbWaitXPath, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var xpath string
			err := starlark.UnpackArgs("wait_xpath", a, kw, "xpath", &xpath)
			return Step{Verb: VerbWaitXPath, Selector: xpath}, err
		}, false),
		"text": action(VerbText, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var sel string
			err := starlark.UnpackArgs("text", a, kw, "selector", &sel)
			return Step{Verb: VerbText, Selector: sel}, err
		}, true),
		"eval": action(VerbEval, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var js string
			err := starlark.UnpackArgs("eval", a, kw, "js", &js)
			return Step{Verb: VerbEval, JS: js}, err
		}, true),
		"fetch": action(VerbFetch, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var url string
			err := starlark.UnpackArgs("fetch", a, kw, "url", &url)
			return Step{Verb: VerbFetch, URL: url}, err
		}, true),
		"screenshot": action(VerbScreenshot, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var path, mode string
			err := starlark.UnpackArgs("screenshot", a, kw, "path?", &path, "mode?", &mode)
			return Step{Verb: VerbScreenshot, Path: path, ScreenshotMode: ScreenshotMode(mode)}, err
		}, true),
		"viewport": action(VerbViewport, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var width, height int
			err := starlark.UnpackArgs("viewport", a, kw, "width", &width, "height", &height)
			return Step{Verb: VerbViewport, Width: width, Height: height}, err
		}, false),
		"console": action(VerbConsole, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var navigate, evalJS, window, level, kind string
			var reload, failOnError bool
			err := starlark.UnpackArgs("console", a, kw,
				"navigate?", &navigate, "reload?", &reload, "eval?", &evalJS,
				"window?", &window, "level?", &level, "kind?", &kind, "fail_on_error?", &failOnError)
			if err != nil {
				return Step{}, err
			}
			ca := &ConsoleArgs{Navigate: navigate, Reload: reload, Eval: evalJS, Level: level, Kind: kind, FailOnError: failOnError}
			if window != "" {
				d, perr := time.ParseDuration(window)
				if perr != nil {
					return Step{}, fmt.Errorf("console window: %w", perr)
				}
				ca.Window = Duration(d)
			}
			return Step{Verb: VerbConsole, Console: ca}, nil
		}, true),
		"snapshot": action(VerbSnapshot, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var filter, role string
			var max int
			var includeHidden, includeRect bool
			err := starlark.UnpackArgs("snapshot", a, kw,
				"max?", &max, "role?", &role, "filter?", &filter,
				"include_hidden?", &includeHidden, "include_rect?", &includeRect)
			if err != nil {
				return Step{}, err
			}
			sa := &SnapshotArgs{Max: max, Filter: filter, IncludeHidden: includeHidden, IncludeRect: includeRect}
			for _, r := range strings.Split(role, ",") {
				if r = strings.TrimSpace(r); r != "" {
					sa.Roles = append(sa.Roles, r)
				}
			}
			return Step{Verb: VerbSnapshot, Snapshot: sa}, nil
		}, true),
		"assert_text": action(VerbAssertText, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var sel, eq, contains, rx string
			err := starlark.UnpackArgs("assert_text", a, kw, "selector", &sel, "equals?", &eq, "contains?", &contains, "regex?", &rx)
			return Step{Verb: VerbAssertText, Selector: sel, Assert: &Assertion{Equals: eq, Contains: contains, Regex: rx}}, err
		}, false),
		"assert_count": action(VerbAssertCount, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var sel string
			var equals, atLeast, atMost starlark.Value = starlark.None, starlark.None, starlark.None
			if err := starlark.UnpackArgs("assert_count", a, kw, "selector", &sel, "equals?", &equals, "at_least?", &atLeast, "at_most?", &atMost); err != nil {
				return Step{}, err
			}
			eq, err := optionalStarlarkInt("equals", equals)
			if err != nil {
				return Step{}, err
			}
			min, err := optionalStarlarkInt("at_least", atLeast)
			if err != nil {
				return Step{}, err
			}
			max, err := optionalStarlarkInt("at_most", atMost)
			if err != nil {
				return Step{}, err
			}
			return Step{Verb: VerbAssertCount, Selector: sel, Count: &CountAssertion{Equals: eq, AtLeast: min, AtMost: max}}, nil
		}, false),
		"assert_url": action(VerbAssertURL, func(a starlark.Tuple, kw []starlark.Tuple) (Step, error) {
			var eq, contains, rx string
			err := starlark.UnpackArgs("assert_url", a, kw, "equals?", &eq, "contains?", &contains, "regex?", &rx)
			return Step{Verb: VerbAssertURL, Assert: &Assertion{Equals: eq, Contains: contains, Regex: rx}}, err
		}, false),
		"emit": starlark.NewBuiltin("emit", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kw []starlark.Tuple) (starlark.Value, error) {
			var obj starlark.Value
			if err := starlark.UnpackArgs("emit", args, kw, "obj", &obj); err != nil {
				return nil, err
			}
			m, err := starlarkToMap(obj)
			if err != nil {
				return nil, err
			}
			e.emit(m)
			return starlark.None, nil
		}),
	}
}

func paramsDict(vars map[string]string) *starlark.Dict {
	d := starlark.NewDict(len(vars))
	for k, v := range vars {
		_ = d.SetKey(starlark.String(k), starlark.String(v))
	}
	return d
}

func optionalStarlarkInt(name string, v starlark.Value) (*int, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}
	i, ok := v.(starlark.Int)
	if !ok {
		return nil, fmt.Errorf("%s must be an int, got %s", name, v.Type())
	}
	n64, ok := i.Int64()
	if !ok {
		return nil, fmt.Errorf("%s is outside int64 range", name)
	}
	n := int(n64)
	if int64(n) != n64 {
		return nil, fmt.Errorf("%s is outside int range", name)
	}
	return &n, nil
}

// starlarkToMap converts an emit() argument (expected dict) to a Go map.
func starlarkToMap(v starlark.Value) (map[string]any, error) {
	d, ok := v.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("emit() expects a dict, got %s", v.Type())
	}
	out := make(map[string]any, d.Len())
	for _, item := range d.Items() {
		key, ok := starlark.AsString(item[0])
		if !ok {
			key = item[0].String()
		}
		out[key] = starlarkToGo(item[1])
	}
	return out, nil
}

func starlarkToGo(v starlark.Value) any {
	switch t := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(t)
	case starlark.Int:
		if i, ok := t.Int64(); ok {
			return i
		}
		return t.String()
	case starlark.Float:
		return float64(t)
	case starlark.String:
		return string(t)
	case *starlark.List:
		out := make([]any, 0, t.Len())
		for i := 0; i < t.Len(); i++ {
			out = append(out, starlarkToGo(t.Index(i)))
		}
		return out
	case *starlark.Dict:
		out := make(map[string]any, t.Len())
		for _, item := range t.Items() {
			key, ok := starlark.AsString(item[0])
			if !ok {
				key = item[0].String()
			}
			out[key] = starlarkToGo(item[1])
		}
		return out
	default:
		return v.String()
	}
}
