package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/nstranquist/snapref/browser"
	browsermcp "github.com/nstranquist/snapref/mcp"
)

const (
	refIntegritySchema = "snapref.demo.ref-integrity.v1"
	demoHelpText       = `Usage:
  snapref demo mcp-policy
  snapref demo ref-integrity [--runtime auto|headless-shell|chrome-for-testing|external]

mcp-policy proves snapshot is ungated, click/navigate are denied without
SNAPREF_MCP_ALLOW_DRIVE, and origin allowlists fail closed. It does not launch
Chrome.

ref-integrity snapshots a local fixture, clicks the live sr-… ref, snapshots
again, then proves the old ref fails closed. It needs a managed runtime.
`
)

type refIntegrityReceipt struct {
	OK             bool   `json:"ok"`
	Schema         string `json:"schema"`
	Workflow       string `json:"workflow"`
	RuntimeKind    string `json:"runtime_kind,omitempty"`
	FirstRef       string `json:"first_ref,omitempty"`
	FirstRevision  string `json:"first_revision,omitempty"`
	SecondRevision string `json:"second_revision,omitempty"`
	Clicked        bool   `json:"clicked"`
	StaleRejected  bool   `json:"stale_rejected"`
	StaleError     string `json:"stale_error,omitempty"`
	DurationMS     int64  `json:"duration_ms"`
}

func demoCommand(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(demoHelpText)
		return nil
	}
	switch args[0] {
	case "mcp-policy":
		if len(args) > 1 {
			return fmt.Errorf("demo mcp-policy: unexpected argument %q", args[1])
		}
		receipt, err := browsermcp.RunPolicyWorkflow()
		if err != nil {
			return err
		}
		return emit(receipt)
	case "ref-integrity":
		kind, err := parseDemoRuntime(args[1:])
		if err != nil {
			return err
		}
		receipt, err := runRefIntegrityDemo(kind)
		if emitErr := emit(receipt); emitErr != nil {
			return emitErr
		}
		return err
	default:
		return fmt.Errorf("demo: unknown workflow %q\n\n%s", args[0], demoHelpText)
	}
}

func parseDemoRuntime(args []string) (browser.RuntimeKind, error) {
	kind := browser.RuntimeAuto
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--runtime" && i+1 < len(args):
			i++
			parsed, err := browser.ParseRuntimeKind(args[i])
			if err != nil {
				return "", fmt.Errorf("demo ref-integrity: %w", err)
			}
			kind = parsed
		case strings.HasPrefix(arg, "--runtime="):
			parsed, err := browser.ParseRuntimeKind(strings.TrimPrefix(arg, "--runtime="))
			if err != nil {
				return "", fmt.Errorf("demo ref-integrity: %w", err)
			}
			kind = parsed
		default:
			return "", fmt.Errorf("demo ref-integrity: unknown argument %q", arg)
		}
	}
	return kind, nil
}

func refIntegrityFixtureHTML() string {
	return `<!doctype html><title>ref-integrity</title><button id="target" onclick="this.dataset.clicked='yes'; this.textContent='Clicked'">Original</button>`
}

func runRefIntegrityDemo(runtimeKind browser.RuntimeKind) (refIntegrityReceipt, error) {
	started := time.Now()
	receipt := refIntegrityReceipt{Schema: refIntegritySchema, Workflow: "ref-integrity"}
	idleTimeout, err := configuredSnapRefIdleTimeout()
	if err != nil {
		return receipt, err
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, refIntegrityFixtureHTML())
	}))
	defer fixture.Close()

	name := fmt.Sprintf("demo-ref-%d", time.Now().UnixNano())
	session, err := openManagedSnapRefSession(browser.OpenOptions{
		Name: name, Runtime: runtimeKind, ReadyTimeout: 30 * time.Second,
	}, idleTimeout)
	if err != nil {
		return receipt, err
	}
	defer browser.Close(name, true)
	receipt.RuntimeKind = string(session.RuntimeKind)

	ctx := context.Background()
	if err := browser.RemoteNavigate(ctx, session, fixture.URL, 30*time.Second); err != nil {
		return receipt, err
	}
	first, err := browser.RemoteSnapshot(ctx, session, browser.SnapshotOptions{WaitForSelector: "#target"}, 30*time.Second)
	if err != nil {
		return receipt, err
	}
	if len(first.Elements) == 0 {
		return receipt, errors.New("demo ref-integrity: snapshot returned no elements")
	}
	ref := first.Elements[0].Ref
	receipt.FirstRef = ref
	receipt.FirstRevision = first.Revision
	driver := browser.NewRemoteDriver()
	if err := driver.Click(ctx, session.Name, ref); err != nil {
		return receipt, fmt.Errorf("demo ref-integrity: click live ref: %w", err)
	}
	var clicked string
	if err := browser.RemoteEval(ctx, session, "", `document.querySelector('#target').dataset.clicked`, &clicked, 30*time.Second); err != nil {
		return receipt, err
	}
	if clicked != "yes" {
		return receipt, fmt.Errorf("demo ref-integrity: live ref did not actuate, got %q", clicked)
	}
	receipt.Clicked = true

	second, err := browser.RemoteSnapshot(ctx, session, browser.SnapshotOptions{WaitForSelector: "#target"}, 30*time.Second)
	if err != nil {
		return receipt, err
	}
	receipt.SecondRevision = second.Revision
	if second.Revision == "" || second.Revision == first.Revision {
		return receipt, fmt.Errorf("demo ref-integrity: second snapshot kept revision %q", second.Revision)
	}

	err = driver.Click(ctx, session.Name, ref)
	if !errors.Is(err, browser.ErrStaleSnapshotRef) {
		if err == nil {
			return receipt, errors.New("demo ref-integrity: superseded ref was reused instead of failing closed")
		}
		return receipt, fmt.Errorf("demo ref-integrity: stale click error %v, want ErrStaleSnapshotRef", err)
	}
	receipt.StaleRejected = true
	receipt.StaleError = err.Error()
	receipt.OK = true
	receipt.DurationMS = time.Since(started).Milliseconds()
	return receipt, nil
}
