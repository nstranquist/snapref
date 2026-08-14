package main

import (
	"go/ast"
	"go/parser"
	"go/token"
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

func TestHelpExposesGenericSkillsWithoutNicosHandoff(t *testing.T) {
	for _, want := range []string{"pageskein skill <url>", "pageskein job-posting"} {
		if !strings.Contains(helpText, want) {
			t.Fatalf("help missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(helpText), "jobkit") {
		t.Fatal("standalone help leaked the Nicos JobKit adapter")
	}
}

func TestManagedSessionDefaultsAndDurationValidation(t *testing.T) {
	t.Setenv("PAGESKEIN_BROWSER_IDLE_TIMEOUT", "")
	got, err := configuredPageSkeinIdleTimeout()
	if err != nil || got != 10*time.Minute {
		t.Fatalf("default timeout = %s, %v", got, err)
	}
	t.Setenv("PAGESKEIN_BROWSER_IDLE_TIMEOUT", "45m")
	got, err = configuredPageSkeinIdleTimeout()
	if err != nil || got != 45*time.Minute {
		t.Fatalf("configured timeout = %s, %v", got, err)
	}
	for _, raw := range []string{"0", "0s", "-1s", "-1d", "999999999999999999999w"} {
		if _, err := parsePageSkeinDuration(raw); err == nil {
			t.Fatalf("parsePageSkeinDuration(%q) accepted an unsafe duration", raw)
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
	for _, want := range []string{"--idle-timeout 10m|--keep-alive", "pageskein gc", "PAGESKEIN_BROWSER_IDLE_TIMEOUT"} {
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
			if strings.Trim(imported.Path.Value, `"`) != "github.com/nstranquist/pageskein/browser" {
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
				t.Errorf("%s calls browser.Open directly at %s; use openManagedPageSkeinSession", name, fset.Position(call.Pos()))
			}
			return true
		})
	}
}
