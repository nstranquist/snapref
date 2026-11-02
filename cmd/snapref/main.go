package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/snapref/browser"
	"github.com/nstranquist/snapref/browser/skills"
	"github.com/nstranquist/snapref/mcp"
)

const version = "0.2.0-rc.1"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "snapref:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(helpText)
		return nil
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "doctor":
		return doctorRuntime(args[1:])
	case "runtime":
		return runtimeCommand(args[1:])
	case "open":
		return openSession(args[1:])
	case "close":
		return closeSession(args[1:])
	case "gc":
		return gcSessionState(args[1:])
	case "list":
		driver := browser.NewRemoteDriver()
		sessions, err := driver.List()
		if err != nil {
			return err
		}
		return emit(sessions)
	case "exec":
		return execute(args[1:])
	case "script":
		return scriptCommand(args[1:])
	case "skill":
		return extractSkill(args[1:], false)
	case "job-posting":
		return extractSkill(args[1:], true)
	case "self-test":
		return selfTest(args[1:])
	case "demo":
		return demoCommand(args[1:])
	case "mcp":
		return serveMCP()
	case "__watch-session":
		return watchSnapRefSession(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], helpText)
	}
}

func extractSkill(args []string, jobPostingOnly bool) error {
	positionals := make([]string, 0, 1)
	runtimeKind := browser.RuntimeAuto
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--json" {
			continue // SnapRef commands are JSON-native; accepted for CLI parity.
		}
		if arg == "--runtime" && i+1 < len(args) {
			i++
			parsed, err := browser.ParseRuntimeKind(args[i])
			if err != nil {
				return fmt.Errorf("skill: %w", err)
			}
			runtimeKind = parsed
			continue
		}
		if strings.HasPrefix(arg, "--runtime=") {
			parsed, err := browser.ParseRuntimeKind(strings.TrimPrefix(arg, "--runtime="))
			if err != nil {
				return fmt.Errorf("skill: %w", err)
			}
			runtimeKind = parsed
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("skill: unknown flag %q", arg)
		}
		positionals = append(positionals, arg)
	}
	if len(positionals) != 1 {
		return errors.New("skill requires exactly one URL")
	}
	pageURL := positionals[0]
	root, err := skills.DefaultRoot()
	if err != nil {
		return err
	}
	domain := root.DomainOf(pageURL)
	if domain == "" {
		return fmt.Errorf("no SnapRef skill for URL %q", pageURL)
	}
	if jobPostingOnly && domain != skills.DomainJobPosting {
		return fmt.Errorf("job-posting does not support URL %q", pageURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	page := skills.NewStatelessPage(browser.LaunchOptions{Runtime: runtimeKind})
	if err := page.Navigate(ctx, pageURL); err != nil {
		return err
	}
	switch domain {
	case skills.DomainJobPosting:
		result, err := skills.ExtractJobPosting(ctx, page, root, pageURL)
		if err != nil {
			return err
		}
		return emit(result)
	case skills.DomainLinkedIn:
		result, err := skills.ExtractLinkedIn(ctx, page, root, pageURL)
		if err != nil {
			return err
		}
		return emit(result)
	default:
		result, err := skills.ExtractSocial(ctx, page, root, domain, pageURL)
		if err != nil {
			return err
		}
		return emit(result)
	}
}

func openSession(args []string) error {
	var (
		opts           browser.OpenOptions
		idleTimeout    time.Duration
		idleTimeoutSet bool
		keepAlive      bool
		positionals    []string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--headful":
			opts.Headful = true
		case arg == "--runtime" && i+1 < len(args):
			i++
			kind, err := browser.ParseRuntimeKind(args[i])
			if err != nil {
				return fmt.Errorf("open: %w", err)
			}
			opts.Runtime = kind
		case strings.HasPrefix(arg, "--runtime="):
			kind, err := browser.ParseRuntimeKind(strings.TrimPrefix(arg, "--runtime="))
			if err != nil {
				return fmt.Errorf("open: %w", err)
			}
			opts.Runtime = kind
		case arg == "--keep-alive":
			keepAlive = true
		case arg == "--idle-timeout" && i+1 < len(args):
			i++
			parsed, err := parseSnapRefDuration(args[i])
			if err != nil {
				return fmt.Errorf("open: bad --idle-timeout: %w", err)
			}
			if err := browser.ValidateManagedIdleTimeout(parsed); err != nil {
				return fmt.Errorf("open: bad --idle-timeout: %w", err)
			}
			idleTimeout = parsed
			idleTimeoutSet = true
		case strings.HasPrefix(arg, "--idle-timeout="):
			parsed, err := parseSnapRefDuration(strings.TrimPrefix(arg, "--idle-timeout="))
			if err != nil {
				return fmt.Errorf("open: bad --idle-timeout: %w", err)
			}
			if err := browser.ValidateManagedIdleTimeout(parsed); err != nil {
				return fmt.Errorf("open: bad --idle-timeout: %w", err)
			}
			idleTimeout = parsed
			idleTimeoutSet = true
		case arg == "--help" || arg == "-h":
			fmt.Print(openHelpText)
			return nil
		case strings.HasPrefix(arg, "--"):
			return fmt.Errorf("open: unknown or incomplete flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return errors.New("open requires exactly one session name")
	}
	if keepAlive && idleTimeoutSet {
		return errors.New("open: --keep-alive and --idle-timeout are mutually exclusive")
	}
	opts.Name = positionals[0]
	if keepAlive {
		idleTimeout = 0
	} else if !idleTimeoutSet {
		configured, err := configuredSnapRefIdleTimeout()
		if err != nil {
			return err
		}
		idleTimeout = configured
	}
	session, err := openManagedSnapRefSession(opts, idleTimeout)
	if err != nil {
		return err
	}
	return emit(session)
}

func doctorRuntime(args []string) error {
	kind := browser.RuntimeAuto
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
		case arg == "--runtime" && i+1 < len(args):
			i++
			parsed, err := browser.ParseRuntimeKind(args[i])
			if err != nil {
				return err
			}
			kind = parsed
		case strings.HasPrefix(arg, "--runtime="):
			parsed, err := browser.ParseRuntimeKind(strings.TrimPrefix(arg, "--runtime="))
			if err != nil {
				return err
			}
			kind = parsed
		default:
			return fmt.Errorf("doctor: unknown or incomplete flag %q", arg)
		}
	}
	return emit(browser.DoctorRuntime(kind))
}

type runtimeEnvelope struct {
	SchemaVersion int                             `json:"schema_version"`
	Operation     string                          `json:"operation"`
	OK            bool                            `json:"ok"`
	Runtimes      []browser.RuntimeRecord         `json:"runtimes"`
	Prune         *browser.RuntimePruneReport     `json:"prune,omitempty"`
	Benchmark     *browser.RuntimeBenchmarkResult `json:"benchmark,omitempty"`
	Errors        []string                        `json:"errors"`
}

func runtimeCommand(args []string) (returnErr error) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(runtimeHelpText)
		return nil
	}
	operation := args[0]
	rest := args[1:]
	emitted := false
	defer func() {
		if returnErr != nil && !emitted {
			_ = emit(runtimeEnvelope{SchemaVersion: 1, Operation: operation, OK: false, Runtimes: []browser.RuntimeRecord{}, Errors: []string{returnErr.Error()}})
		}
	}()
	switch operation {
	case "install":
		kind, channel, version, err := parseRuntimeInstallFlags(rest)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		result, err := browser.InstallRuntime(ctx, browser.RuntimeInstallOptions{Kind: kind, Channel: channel, Version: version})
		if err != nil {
			return err
		}
		emitted = true
		return emit(runtimeEnvelope{SchemaVersion: 1, Operation: operation, OK: true, Runtimes: []browser.RuntimeRecord{result.Runtime}, Errors: []string{}})
	case "list":
		if err := onlyJSONFlag(rest); err != nil {
			return err
		}
		records, err := browser.ListRuntimeRecords()
		if err != nil {
			return err
		}
		emitted = true
		return emit(runtimeEnvelope{SchemaVersion: 1, Operation: operation, OK: true, Runtimes: records, Errors: []string{}})
	case "verify":
		kind, err := parseRuntimeFilterFlags(rest, true)
		if err != nil {
			return err
		}
		records, err := browser.VerifyRuntimes(kind)
		if err != nil {
			return err
		}
		ok := len(records) > 0
		for _, record := range records {
			ok = ok && record.Verified
		}
		if !ok {
			emitted = true
			_ = emit(runtimeEnvelope{SchemaVersion: 1, Operation: operation, OK: false, Runtimes: records, Errors: []string{fmt.Sprintf("runtime verification failed for %d installation(s)", len(records))}})
			return fmt.Errorf("runtime verification failed for %d installation(s)", len(records))
		}
		emitted = true
		return emit(runtimeEnvelope{SchemaVersion: 1, Operation: operation, OK: true, Runtimes: records, Errors: []string{}})
	case "prune":
		opts, err := parseRuntimePruneFlags(rest)
		if err != nil {
			return err
		}
		report, err := browser.PruneRuntimes(opts)
		if err != nil {
			return err
		}
		emitted = true
		return emit(runtimeEnvelope{SchemaVersion: 1, Operation: operation, OK: true, Runtimes: []browser.RuntimeRecord{}, Prune: &report, Errors: []string{}})
	case "benchmark":
		samples, err := parseRuntimeBenchmarkFlags(rest)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		result, err := browser.BenchmarkRuntimes(ctx, browser.RuntimeBenchmarkOptions{
			Samples: samples,
		})
		if err != nil {
			return err
		}
		emitted = true
		return emit(runtimeEnvelope{SchemaVersion: 1, Operation: operation, OK: true, Runtimes: []browser.RuntimeRecord{}, Benchmark: &result, Errors: []string{}})
	default:
		return fmt.Errorf("runtime: unknown operation %q", operation)
	}
}

func parseRuntimeBenchmarkFlags(args []string) (int, error) {
	samples := 30
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
		case args[i] == "--record":
			return 0, errors.New("runtime benchmark: standalone recording is unavailable; acceptance recording is not part of this module")
		case args[i] == "--consumer-evidence" || strings.HasPrefix(args[i], "--consumer-evidence="):
			return 0, errors.New("runtime benchmark: standalone consumer evidence is unavailable; acceptance recording is not part of this module")
		case args[i] == "--samples" && i+1 < len(args):
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return 0, errors.New("runtime benchmark: --samples must be an integer")
			}
			samples = value
		case strings.HasPrefix(args[i], "--samples="):
			value, err := strconv.Atoi(strings.TrimPrefix(args[i], "--samples="))
			if err != nil {
				return 0, errors.New("runtime benchmark: --samples must be an integer")
			}
			samples = value
		default:
			return 0, fmt.Errorf("runtime benchmark: unknown or incomplete flag %q", args[i])
		}
	}
	return samples, nil
}

func parseRuntimeInstallFlags(args []string) (browser.RuntimeKind, string, string, error) {
	var kind browser.RuntimeKind
	var channel, version string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func(name string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("runtime install: %s requires a value", name)
			}
			i++
			return args[i], nil
		}
		switch {
		case arg == "--json":
		case arg == "--runtime":
			raw, err := value(arg)
			if err != nil {
				return "", "", "", err
			}
			kind, err = browser.ParseRuntimeKind(raw)
			if err != nil {
				return "", "", "", err
			}
		case strings.HasPrefix(arg, "--runtime="):
			var err error
			kind, err = browser.ParseRuntimeKind(strings.TrimPrefix(arg, "--runtime="))
			if err != nil {
				return "", "", "", err
			}
		case arg == "--channel":
			var err error
			channel, err = value(arg)
			if err != nil {
				return "", "", "", err
			}
		case strings.HasPrefix(arg, "--channel="):
			channel = strings.TrimPrefix(arg, "--channel=")
		case arg == "--version":
			var err error
			version, err = value(arg)
			if err != nil {
				return "", "", "", err
			}
		case strings.HasPrefix(arg, "--version="):
			version = strings.TrimPrefix(arg, "--version=")
		default:
			return "", "", "", fmt.Errorf("runtime install: unknown flag %q", arg)
		}
	}
	return kind, channel, version, nil
}

func parseRuntimeFilterFlags(args []string, allowAll bool) (browser.RuntimeKind, error) {
	kind := browser.RuntimeAuto
	all := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
		case args[i] == "--all" && allowAll:
			all = true
		case args[i] == "--runtime" && i+1 < len(args):
			i++
			parsed, err := browser.ParseRuntimeKind(args[i])
			if err != nil {
				return "", err
			}
			kind = parsed
		case strings.HasPrefix(args[i], "--runtime="):
			parsed, err := browser.ParseRuntimeKind(strings.TrimPrefix(args[i], "--runtime="))
			if err != nil {
				return "", err
			}
			kind = parsed
		default:
			return "", fmt.Errorf("runtime: unknown or incomplete flag %q", args[i])
		}
	}
	if all && kind != browser.RuntimeAuto {
		return "", errors.New("runtime: --all and --runtime are mutually exclusive")
	}
	return kind, nil
}

func parseRuntimePruneFlags(args []string) (browser.RuntimePruneOptions, error) {
	opts := browser.RuntimePruneOptions{Keep: 2}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
		case args[i] == "--apply":
			opts.Apply = true
		case args[i] == "--keep" && i+1 < len(args):
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 {
				return opts, errors.New("runtime prune: --keep must be a positive integer")
			}
			opts.Keep = value
		case strings.HasPrefix(args[i], "--keep="):
			value, err := strconv.Atoi(strings.TrimPrefix(args[i], "--keep="))
			if err != nil || value <= 0 {
				return opts, errors.New("runtime prune: --keep must be a positive integer")
			}
			opts.Keep = value
		default:
			filtered = append(filtered, args[i])
		}
	}
	kind, err := parseRuntimeFilterFlags(filtered, false)
	opts.Kind = kind
	return opts, err
}

func onlyJSONFlag(args []string) error {
	for _, arg := range args {
		if arg != "--json" {
			return fmt.Errorf("runtime: unknown flag %q", arg)
		}
	}
	return nil
}

func closeSession(args []string) error {
	if len(args) == 0 {
		return errors.New("close requires a session name")
	}
	purge := false
	for _, arg := range args[1:] {
		if arg != "--purge-profile" {
			return fmt.Errorf("close: unknown flag %q", arg)
		}
		purge = true
	}
	if err := browser.Close(args[0], purge); err != nil {
		return err
	}
	return emit(map[string]any{"ok": true, "session": args[0], "purged_profile": purge})
}

func execute(args []string) error {
	if len(args) < 2 {
		return errors.New("exec requires <session> <verb>")
	}
	session, verb, rest := args[0], args[1], args[2:]
	driver := browser.NewRemoteDriver()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require := func(n int, usage string) error {
		if len(rest) != n {
			return errors.New(usage)
		}
		return nil
	}
	switch verb {
	case "navigate":
		if err := require(1, "exec <session> navigate <url>"); err != nil {
			return err
		}
		return driver.Navigate(ctx, session, rest[0])
	case "click":
		if err := require(1, "exec <session> click <selector-or-ref>"); err != nil {
			return err
		}
		return driver.Click(ctx, session, rest[0])
	case "type":
		if err := require(2, "exec <session> type <selector-or-ref> <text>"); err != nil {
			return err
		}
		return driver.Type(ctx, session, rest[0], rest[1])
	case "select":
		if err := require(2, "exec <session> select <selector-or-ref> <visible-option-label>"); err != nil {
			return err
		}
		return driver.Select(ctx, session, rest[0], rest[1])
	case "press":
		if err := require(1, "exec <session> press <key>"); err != nil {
			return err
		}
		return driver.Press(ctx, session, rest[0])
	case "wait":
		if err := require(1, "exec <session> wait <selector-or-ref>"); err != nil {
			return err
		}
		return driver.WaitVisible(ctx, session, rest[0])
	case "text":
		if err := require(1, "exec <session> text <selector-or-ref>"); err != nil {
			return err
		}
		text, err := driver.Text(ctx, session, rest[0])
		if err != nil {
			return err
		}
		return emit(map[string]any{"text": text})
	case "screenshot":
		if len(rest) > 1 {
			return errors.New("exec <session> screenshot [path]")
		}
		path := ""
		if len(rest) == 1 {
			path = rest[0]
		}
		written, err := driver.Screenshot(ctx, session, path)
		if err != nil {
			return err
		}
		return emit(map[string]any{"path": written})
	case "snapshot":
		opts, err := parseSnapshotOptions(rest)
		if err != nil {
			return err
		}
		snapshot, err := driver.Snapshot(ctx, session, opts)
		if err != nil {
			return err
		}
		return emit(snapshot)
	default:
		return fmt.Errorf("exec: unknown verb %q", verb)
	}
}

func parseSnapshotOptions(args []string) (browser.SnapshotOptions, error) {
	var opts browser.SnapshotOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("snapshot: %s requires a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--all":
			opts.IncludeHidden = true
		case "--rects":
			opts.IncludeRect = true
		case "--filter", "--role", "--max", "--wait-for-selector", "--settle":
			value, err := next()
			if err != nil {
				return opts, err
			}
			switch arg {
			case "--filter":
				opts.Filter = value
			case "--role":
				opts.Roles = strings.Split(value, ",")
			case "--max":
				opts.Max, err = strconv.Atoi(value)
				if err != nil || opts.Max < 0 {
					return opts, fmt.Errorf("snapshot: invalid max %q", value)
				}
			case "--wait-for-selector":
				opts.WaitForSelector = value
			case "--settle":
				opts.Settle, err = time.ParseDuration(value)
				if err != nil || opts.Settle <= 0 {
					return opts, fmt.Errorf("snapshot: invalid settle %q", value)
				}
			}
		default:
			return opts, fmt.Errorf("snapshot: unknown flag %q", arg)
		}
	}
	return opts, nil
}

type selfTestReport struct {
	OK           bool                `json:"ok"`
	Version      string              `json:"version"`
	RuntimeKind  browser.RuntimeKind `json:"runtime_kind"`
	Elements     int                 `json:"elements"`
	ShadowRoots  int                 `json:"shadow_roots"`
	Frames       int                 `json:"frames"`
	ReadyBy      string              `json:"ready_by"`
	Selected     string              `json:"selected"`
	SelectInput  int                 `json:"select_input_events"`
	SelectChange int                 `json:"select_change_events"`
	Screenshot   int                 `json:"screenshot_bytes"`
	DurationMS   int64               `json:"duration_ms"`
}

func selfTest(args []string) error {
	runtimeKind := browser.RuntimeAuto
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--runtime" && i+1 < len(args):
			i++
			parsed, err := browser.ParseRuntimeKind(args[i])
			if err != nil {
				return fmt.Errorf("self-test: %w", err)
			}
			runtimeKind = parsed
		case strings.HasPrefix(args[i], "--runtime="):
			parsed, err := browser.ParseRuntimeKind(strings.TrimPrefix(args[i], "--runtime="))
			if err != nil {
				return fmt.Errorf("self-test: %w", err)
			}
			runtimeKind = parsed
		case args[i] == "--json":
		default:
			return fmt.Errorf("self-test: unknown or incomplete flag %q", args[i])
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/frame" {
			fmt.Fprint(w, selfTestFrameHTML())
			return
		}
		fmt.Fprint(w, selfTestPageHTML())
	}))
	defer server.Close()

	started := time.Now()
	idleTimeout, err := configuredSnapRefIdleTimeout()
	if err != nil {
		return err
	}
	sessionName := fmt.Sprintf("self-test-%d", time.Now().UnixNano())
	session, err := openManagedSnapRefSession(browser.OpenOptions{Name: sessionName, Runtime: runtimeKind}, idleTimeout)
	if err != nil {
		return err
	}
	defer browser.Close(sessionName, true)
	ctx := context.Background()
	if err := browser.RemoteNavigate(ctx, session, server.URL, 20*time.Second); err != nil {
		return err
	}
	snapshot, err := browser.RemoteSnapshot(ctx, session, selfTestSnapshotOptions(), 20*time.Second)
	if err != nil {
		return err
	}
	if snapshot.Returned != 4 || snapshot.ShadowRoots != 1 || snapshot.Frames != 1 {
		return fmt.Errorf("unexpected snapshot: elements=%d shadows=%d frames=%d", snapshot.Returned, snapshot.ShadowRoots, snapshot.Frames)
	}
	byName := map[string]string{}
	planRef := ""
	for _, element := range snapshot.Elements {
		byName[element.Name] = element.Ref
		if element.Tag == "select" && element.Name == "Plan" {
			planRef = element.Ref
		}
	}
	driver := browser.NewRemoteDriver()
	for _, name := range []string{"Hydrated action", "Shadow action", "Frame action"} {
		if byName[name] == "" {
			return fmt.Errorf("snapshot missing %q", name)
		}
		if err := driver.Click(ctx, session.Name, byName[name]); err != nil {
			return fmt.Errorf("click %s: %w", name, err)
		}
	}
	if planRef == "" {
		return errors.New("snapshot missing \"Plan\"")
	}
	if err := driver.Select(ctx, session.Name, planRef, "Team plan"); err != nil {
		return fmt.Errorf("select Plan: %w", err)
	}
	var selectState struct {
		Selected string `json:"selected"`
		Input    int    `json:"input"`
		Change   int    `json:"change"`
	}
	if err := browser.RemoteEval(ctx, session, "", `(() => ({
selected: document.querySelector('#plan').selectedOptions[0].label,
input: window.selfTestSelect.input,
change: window.selfTestSelect.change
}))()`, &selectState, 20*time.Second); err != nil {
		return fmt.Errorf("read selected plan: %w", err)
	}
	if selectState.Selected != "Team plan" || selectState.Input != 1 || selectState.Change != 1 {
		return fmt.Errorf("selected plan receipt = %+v, want Team plan with one input and change event", selectState)
	}
	png, err := browser.RemoteScreenshotCurrent(session, true)
	if err != nil {
		return err
	}
	return emit(selfTestReport{
		OK: true, Version: version, RuntimeKind: session.RuntimeKind, Elements: snapshot.Returned,
		ShadowRoots: snapshot.ShadowRoots, Frames: snapshot.Frames,
		ReadyBy: snapshot.ReadyBy, Selected: selectState.Selected,
		SelectInput: selectState.Input, SelectChange: selectState.Change, Screenshot: len(png),
		DurationMS: time.Since(started).Milliseconds(),
	})
}

func selfTestSnapshotOptions() browser.SnapshotOptions {
	return browser.SnapshotOptions{
		// The frame exists in the initial HTML, before the delayed hydrated
		// control. Waiting on the actual hydration hook keeps the self-test from
		// declaring an earlier two-element inventory stable.
		WaitForSelector: "#hydrated",
		Settle:          time.Second,
	}
}

func selfTestFrameHTML() string {
	return `<!doctype html><button id="frame-action" onclick="this.dataset.clicked='yes'">Frame action</button>`
}

func selfTestPageHTML() string {
	return `<!doctype html><title>SnapRef self-test</title><main id="app"></main><select id="plan" aria-label="Plan"><option>Hobby plan</option><option>Team plan</option></select><div id="shadow-host"></div><iframe id="child-frame" src="/frame"></iframe><script>
const root = document.querySelector('#shadow-host').attachShadow({mode:'open'});
root.innerHTML = '<button id="shadow-action">Shadow action</button>';
root.querySelector('button').onclick = event => event.currentTarget.dataset.clicked = 'yes';
window.selfTestSelect = {input: 0, change: 0};
document.querySelector('#plan').addEventListener('input', () => window.selfTestSelect.input++);
document.querySelector('#plan').addEventListener('change', () => window.selfTestSelect.change++);
setTimeout(() => { document.querySelector('#app').innerHTML = '<button id="hydrated">Hydrated action</button>'; }, 200);
</script>`
}

func serveMCP() error {
	return browsermcp.Serve(os.Stdin, os.Stdout, browsermcp.Options{
		AllowDrive:     truthy(os.Getenv("SNAPREF_MCP_ALLOW_DRIVE")),
		AllowHostRead:  truthy(os.Getenv("SNAPREF_MCP_ALLOW_HOST_READ")),
		AllowHostWrite: truthy(os.Getenv("SNAPREF_MCP_ALLOW_HOST_WRITE")),
		AllowOrigins:   splitCSV(os.Getenv("SNAPREF_MCP_ALLOW_ORIGINS")),
		Audit:          os.Stderr,
	})
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func emit(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

const helpText = `SnapRef - local-first browser automation for agents

Usage:
  snapref doctor [--runtime auto|headless-shell|chrome-for-testing|external]
  snapref runtime install|list|verify|benchmark|prune
  snapref open <session> [--runtime <kind>] [--headful] [--idle-timeout 10m|--keep-alive]
  snapref close <session> [--purge-profile]
  snapref gc [--older-than 24h|14d] [--dry-run|--apply]
  snapref list
  snapref exec <session> navigate <url>
  snapref exec <session> snapshot [--wait-for-selector <css>] [--settle 1s]
  snapref exec <session> click|wait|text <selector-or-ref>
  snapref exec <session> type <selector-or-ref> <text>
  snapref exec <session> select <selector-or-ref> <visible-option-label>
  snapref exec <session> press <key>
  snapref exec <session> screenshot [path]
  snapref script run <file.yaml|.json|.star> [--session <name>] [--open] [--allow-drive]
  snapref skill <url> [--runtime <kind>] [--json]
  snapref job-posting <greenhouse|lever|ashby-url> [--runtime <kind>] [--json]
  snapref self-test [--runtime <kind>]
  snapref demo mcp-policy
  snapref demo ref-integrity [--runtime <kind>]
  snapref mcp
  snapref version

Headless is the default. Named sessions auto-close after 10m without browser
operations; override with --idle-timeout or SNAPREF_BROWSER_IDLE_TIMEOUT, or
use explicit --keep-alive. Profiles are retained for reuse until close
--purge-profile or gc --apply. MCP drive, host read, host write, and origin
grants use separate SNAPREF_MCP_* environment variables. Snapshot before
acting and use the latest executable ref.
`

const openHelpText = `Usage: snapref open <session> [--runtime <kind>] [--headful] [--idle-timeout <duration>|--keep-alive]
  --runtime: select auto, headless-shell, chrome-for-testing, or external.
  --idle-timeout: auto-close after no browser operations (default 10m; env SNAPREF_BROWSER_IDLE_TIMEOUT).
  --keep-alive: explicitly disable automatic idle cleanup for this session.
`

const runtimeHelpText = `Usage:
  snapref runtime install --runtime <headless-shell|chrome-for-testing> (--channel stable|--version <version>)
  snapref runtime list
  snapref runtime verify (--all|--runtime <managed-kind>)
  snapref runtime benchmark [--samples 30]
  snapref runtime prune [--runtime <managed-kind>] [--keep 2] [--apply]

Install is the only operation that downloads. Standalone benchmark is always
diagnostic and non-mutating. Acceptance recording is not part of this module.
Prune mutates only when --apply is present.
`
