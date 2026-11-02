package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionMatchesReleaseFile(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != version {
		t.Fatalf("VERSION = %q, CLI = %q", got, version)
	}
}

func TestSnapshotOptionContract(t *testing.T) {
	opts, err := parseSnapshotOptions([]string{"--wait-for-selector", "#ready", "--settle", "750ms", "--role", "button,link", "--rects"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.WaitForSelector != "#ready" || opts.Settle.String() != "750ms" || len(opts.Roles) != 2 || !opts.IncludeRect {
		t.Fatalf("options = %+v", opts)
	}
}

func TestSelfTestWaitsForHydrationHook(t *testing.T) {
	opts := selfTestSnapshotOptions()
	if opts.WaitForSelector != "#hydrated" || opts.Settle != time.Second {
		t.Fatalf("self-test snapshot options = %+v", opts)
	}
}

func TestSelfTestFixtureHasHydrationShadowAndFrame(t *testing.T) {
	page := selfTestPageHTML()
	frame := selfTestFrameHTML()
	for _, want := range []string{"attachShadow", "setTimeout", "child-frame", `id="hydrated"`, `id="plan"`, "Team plan"} {
		if !strings.Contains(page, want) {
			t.Fatalf("self-test page missing %q:\n%s", want, page)
		}
	}
	if !strings.Contains(frame, `id="frame-action"`) {
		t.Fatalf("self-test frame missing frame-action:\n%s", frame)
	}
}

func TestReadmeTaggedInstallDoesNotAdvertiseDemo(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fence := range readmeShellFences(string(body)) {
		if !strings.Contains(fence, "go install") || !strings.Contains(fence, "@v") {
			continue
		}
		if strings.Contains(fence, "demo") {
			t.Fatalf("tagged go install fence advertises demo (not in v0.2.0-rc.1):\n%s", fence)
		}
	}
}

func readmeShellFences(src string) []string {
	var out []string
	rest := src
	for {
		i := strings.Index(rest, "```sh")
		if i < 0 {
			return out
		}
		rest = rest[i+len("```sh"):]
		rest = strings.TrimPrefix(rest, "\n")
		j := strings.Index(rest, "```")
		if j < 0 {
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j+len("```"):]
	}
}

func TestExampleWorkflowDocsNameShippedCommands(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{filepath.Join("..", "..", "examples", "mcp-policy", "README.md"), "snapref demo mcp-policy"},
		{filepath.Join("..", "..", "examples", "ref-integrity", "README.md"), "snapref demo ref-integrity"},
	}
	for _, tc := range cases {
		body, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), tc.want) {
			t.Fatalf("%s missing %q", tc.path, tc.want)
		}
	}
}

func TestVerifyPathsDoNotLaunchChrome(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	ci, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"go test -mod=readonly -short ./...",
		"go test -mod=readonly -race -short ./...",
	} {
		if !strings.Contains(string(makefile), want) {
			t.Fatalf("Makefile missing %q:\n%s", want, makefile)
		}
	}
	if !strings.Contains(string(ci), "go test -mod=readonly -count=1 -short ./...") {
		t.Fatalf("CI go job missing -short:\n%s", ci)
	}
	if !strings.Contains(string(ci), "snapref self-test --runtime chrome-for-testing") {
		t.Fatal("CI self-test job lost the live self-test command")
	}
}

func TestScriptValidateAcceptsExamplePlaybook(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "open-and-snapshot.yaml")
	if err := scriptCommand([]string{"validate", path}); err != nil {
		t.Fatal(err)
	}
}

func TestScriptRunRequiresPlaybook(t *testing.T) {
	err := scriptCommand([]string{"run"})
	if err == nil || !strings.Contains(err.Error(), "snapref script run") {
		t.Fatalf("script run usage error = %v", err)
	}
}

func TestDemoMCPPolicyReceipt(t *testing.T) {
	stdout, restore := captureStdout(t)
	err := demoCommand([]string{"mcp-policy"})
	restore()
	if err != nil {
		t.Fatal(err)
	}
	out := stdout()
	for _, want := range []string{
		`"workflow": "mcp-policy"`,
		`"ok": true`,
		`"tool": "browser_snapshot"`,
		`"tool": "browser_click"`,
		`"driving disabled"`,
		`"origin not allowlisted`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("demo mcp-policy receipt missing %q:\n%s", want, out)
		}
	}
}

func TestDemoUnknownWorkflow(t *testing.T) {
	err := demoCommand([]string{"unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown workflow") {
		t.Fatalf("demo unknown error = %v", err)
	}
}

func TestHelpExposesGenericSkillsWithoutNicosHandoff(t *testing.T) {
	for _, want := range []string{"snapref skill <url>", "snapref job-posting", "snapref script run", "snapref exec <session> select", "snapref demo mcp-policy", "snapref demo ref-integrity"} {
		if !strings.Contains(helpText, want) {
			t.Fatalf("help missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(helpText), "jobkit") {
		t.Fatal("standalone help leaked the Nicos JobKit adapter")
	}
}

func TestManagedSessionDefaultsAndDurationValidation(t *testing.T) {
	t.Setenv("SNAPREF_BROWSER_IDLE_TIMEOUT", "")
	got, err := configuredSnapRefIdleTimeout()
	if err != nil || got != 10*time.Minute {
		t.Fatalf("default timeout = %s, %v", got, err)
	}
	t.Setenv("SNAPREF_BROWSER_IDLE_TIMEOUT", "45m")
	got, err = configuredSnapRefIdleTimeout()
	if err != nil || got != 45*time.Minute {
		t.Fatalf("configured timeout = %s, %v", got, err)
	}
	for _, raw := range []string{"0", "0s", "-1s", "-1d", "999999999999999999999w"} {
		if _, err := parseSnapRefDuration(raw); err == nil {
			t.Fatalf("parseSnapRefDuration(%q) accepted an unsafe duration", raw)
		}
	}
}

func TestOpenRejectsConflictingLeaseFlagsBeforeLaunch(t *testing.T) {
	err := openSession([]string{"conflict", "--keep-alive", "--idle-timeout", "45m"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("conflicting lease flags error = %v", err)
	}
}

func TestOpenRejectsLeaseShorterThanOperationBudgetBeforeLaunch(t *testing.T) {
	err := openSession([]string{"too-short", "--idle-timeout", "1s"})
	if err == nil || !strings.Contains(err.Error(), "at least 1m") {
		t.Fatalf("short lease error = %v", err)
	}
}

func TestHelpExposesManagedLifecycleAndGC(t *testing.T) {
	for _, want := range []string{"--idle-timeout 10m|--keep-alive", "snapref gc", "SNAPREF_BROWSER_IDLE_TIMEOUT"} {
		if !strings.Contains(helpText, want) {
			t.Fatalf("help missing %q", want)
		}
	}
}

func TestRuntimeBenchmarkIsStandaloneDiagnosticOnly(t *testing.T) {
	for _, args := range [][]string{{"--record"}, {"--consumer-evidence", "/tmp/consumer.json"}, {"--consumer-evidence=/tmp/consumer.json"}} {
		if _, err := parseRuntimeBenchmarkFlags(args); err == nil || !strings.Contains(err.Error(), "standalone") {
			t.Fatalf("flags %v error = %v, want standalone authority error", args, err)
		}
	}
	samples, err := parseRuntimeBenchmarkFlags([]string{"--samples", "31"})
	if err != nil || samples != 31 {
		t.Fatalf("parsed samples=%d err=%v", samples, err)
	}
	if strings.Contains(runtimeHelpText, "--record") || strings.Contains(runtimeHelpText, "--consumer-evidence") {
		t.Fatalf("standalone help advertises unreachable recording authority:\n%s", runtimeHelpText)
	}
}

func TestProductionOpenersUseManagedWrapper(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "idle.go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		browserAliases := map[string]bool{}
		for _, imported := range file.Imports {
			if strings.Trim(imported.Path.Value, `"`) != "github.com/nstranquist/snapref/browser" {
				continue
			}
			alias := "browser"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			browserAliases[alias] = true
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Open" {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && browserAliases[pkg.Name] {
				t.Errorf("%s calls browser.Open directly at %s; use openManagedSnapRefSession", name, fset.Position(call.Pos()))
			}
			return true
		})
	}
}

func captureStdout(t *testing.T) (func() string, func()) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, reader)
		done <- buf.String()
	}()
	restore := func() {
		_ = writer.Close()
		os.Stdout = old
	}
	read := func() string {
		return <-done
	}
	return read, restore
}
