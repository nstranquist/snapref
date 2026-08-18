package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nstranquist/snapref/browser"
	browsermcp "github.com/nstranquist/snapref/mcp"
	"github.com/nstranquist/processtree"
)

const secretMarker = "snapref-bench-canary"

type providerResult struct {
	Provider             string `json:"provider"`
	Version              string `json:"version,omitempty"`
	SnapshotBytes        int    `json:"snapshot_bytes,omitempty"`
	ApproxSnapshotTokens int    `json:"approx_snapshot_tokens,omitempty"`
	DeterministicReplay  *bool  `json:"deterministic_replay,omitempty"`
	SecretLeaked         *bool  `json:"secret_leaked,omitempty"`
	StaleRefRejected     *bool  `json:"stale_ref_rejected,omitempty"`
	AuditReceipt         *bool  `json:"audit_receipt,omitempty"`
	Error                string `json:"error,omitempty"`
	Notes                string `json:"notes,omitempty"`
}

type report struct {
	SchemaVersion int              `json:"schema_version"`
	CapturedAt    string           `json:"captured_at"`
	Fixture       string           `json:"fixture"`
	Method        string           `json:"method"`
	Providers     []providerResult `json:"providers"`
}

func main() {
	out := flag.String("out", "", "write the JSON report to this path")
	flag.Parse()
	server := fixtureServer()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	rep := report{
		SchemaVersion: 1,
		CapturedAt:    time.Now().UTC().Format(time.RFC3339),
		Fixture:       "local password + mutable button",
		Method:        "same local fixture; snapshot payload bytes/4 is an explicit token approximation; replay compares unchanged snapshots; stale-ref test changes the button accessible name before using the old ref",
		Providers: []providerResult{
			benchmarkSnapRef(ctx, server.URL),
			benchmarkAgentBrowser(ctx, server.URL),
			benchmarkPlaywrightMCP(ctx, server.URL),
		},
	}
	body, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fatal(err)
	}
	body = append(body, '\n')
	if *out != "" {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(*out, body, 0o644); err != nil {
			fatal(err)
		}
	}
	_, _ = os.Stdout.Write(body)
}

func fixtureServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Browser benchmark</title>
<label for="password">Password</label><input id="password" type="password" value="%s">
<button id="target">Benchmark action</button><div id="ready">ready</div>`, secretMarker)
	}))
}

func benchmarkSnapRef(ctx context.Context, rawURL string) providerResult {
	result := providerResult{Provider: "SnapRef", Version: "0.2.0-rc.1"}
	home, err := os.MkdirTemp("", "snapref-benchmark-*")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer os.RemoveAll(home)
	old, had := os.LookupEnv("SNAPREF_HOME")
	if err := os.Setenv("SNAPREF_HOME", home); err != nil {
		result.Error = err.Error()
		return result
	}
	defer restoreEnv("SNAPREF_HOME", old, had)

	session, err := browser.Open(browser.OpenOptions{Name: "benchmark"})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer browser.Close(session.Name, true)
	if err := browser.RemoteNavigate(ctx, session, rawURL, 30*time.Second); err != nil {
		result.Error = err.Error()
		return result
	}
	first, err := browser.RemoteSnapshot(ctx, session, browser.SnapshotOptions{WaitForSelector: "#ready", Settle: 500 * time.Millisecond}, 30*time.Second)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	second, err := browser.RemoteSnapshot(ctx, session, browser.SnapshotOptions{WaitForSelector: "#ready", Settle: 500 * time.Millisecond}, 30*time.Second)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	payload, _ := json.Marshal(first)
	result.SnapshotBytes = len(payload)
	result.ApproxSnapshotTokens = approximateTokens(len(payload))
	result.DeterministicReplay = boolPtr(first.Revision == second.Revision)
	result.SecretLeaked = boolPtr(bytes.Contains(payload, []byte(secretMarker)))

	var targetRef string
	for _, element := range first.Elements {
		if element.Name == "Benchmark action" {
			targetRef = element.Ref
			break
		}
	}
	if targetRef == "" {
		result.Error = "SnapRef snapshot did not return the benchmark target"
		return result
	}
	var ignored any
	if err := browser.RemoteEval(ctx, session, "", `document.querySelector('#target').setAttribute('aria-label','Changed benchmark action')`, &ignored, 30*time.Second); err != nil {
		result.Error = err.Error()
		return result
	}
	request := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"browser_click\",\"arguments\":{\"session\":\"%s\",\"ref\":\"%s\"}}}\n", session.Name, targetRef)
	var response, audit bytes.Buffer
	serveErr := browsermcp.Serve(strings.NewReader(request), &response, browsermcp.Options{
		Driver: browsermcp.NewDriver(), AllowDrive: true, Audit: &audit,
	})
	if serveErr != nil {
		result.Error = serveErr.Error()
		return result
	}
	result.StaleRefRejected = boolPtr(strings.Contains(strings.ToLower(response.String()), "stale"))
	result.AuditReceipt = boolPtr(strings.Contains(audit.String(), "browser_click") && strings.Contains(audit.String(), "allowed"))
	result.Notes = "Live Go API + MCP audit path; snapshot values are checked against the exact marker."
	return result
}

func benchmarkAgentBrowser(ctx context.Context, rawURL string) providerResult {
	result := providerResult{Provider: "agent-browser"}
	version, err := external(ctx, "npx", "--yes", "agent-browser", "--version")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Version = strings.TrimSpace(version)
	session := fmt.Sprintf("snapref-bench-%d", time.Now().UnixNano())
	defer external(context.Background(), "npx", "--yes", "agent-browser", "--session", session, "close")
	if _, err := external(ctx, "npx", "--yes", "agent-browser", "--session", session, "open", rawURL); err != nil {
		result.Error = err.Error()
		return result
	}
	first, err := external(ctx, "npx", "--yes", "agent-browser", "--session", session, "snapshot")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	second, err := external(ctx, "npx", "--yes", "agent-browser", "--session", session, "snapshot")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.SnapshotBytes = len(first)
	result.ApproxSnapshotTokens = approximateTokens(len(first))
	result.DeterministicReplay = boolPtr(strings.TrimSpace(first) == strings.TrimSpace(second))
	result.SecretLeaked = boolPtr(strings.Contains(first, secretMarker))
	ref := refForLabel(first, "Benchmark action")
	if ref == "" {
		result.Notes = "No ref could be parsed from the current snapshot text; stale-ref and audit checks are unmeasured."
		return result
	}
	if _, err := external(ctx, "npx", "--yes", "agent-browser", "--session", session, "eval", `document.querySelector('#target').setAttribute('aria-label','Changed benchmark action')`); err != nil {
		result.Error = "mutate benchmark target: " + err.Error()
		return result
	}
	_, clickErr := external(ctx, "npx", "--yes", "agent-browser", "--session", session, "click", ref)
	result.StaleRefRejected = boolPtr(clickErr != nil)
	result.AuditReceipt = boolPtr(false)
	result.Notes = "Live current npx CLI. Audit=false means no per-command audit receipt was found in the invoked CLI output; it is not a claim about optional external logging integrations."
	return result
}

func benchmarkPlaywrightMCP(ctx context.Context, rawURL string) providerResult {
	result := providerResult{Provider: "@playwright/mcp"}
	client, err := newMCPClient(ctx, "npx", "--yes", "@playwright/mcp@latest", "--headless", "--isolated")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer client.close()
	initialize, err := client.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "snapref-benchmark", "version": "1"},
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	var initialized struct {
		Result struct {
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initialize, &initialized); err != nil {
		result.Error = "decode MCP initialize response: " + err.Error()
		return result
	}
	result.Version = initialized.Result.ServerInfo.Version
	if err := client.notify("notifications/initialized", map[string]any{}); err != nil {
		result.Error = err.Error()
		return result
	}
	if _, err := client.tool("browser_navigate", map[string]any{"url": rawURL}); err != nil {
		result.Error = err.Error()
		return result
	}
	firstRaw, err := client.tool("browser_snapshot", map[string]any{})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	secondRaw, err := client.tool("browser_snapshot", map[string]any{})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	first := toolText(firstRaw)
	second := toolText(secondRaw)
	result.SnapshotBytes = len(first)
	result.ApproxSnapshotTokens = approximateTokens(len(first))
	result.DeterministicReplay = boolPtr(strings.TrimSpace(first) == strings.TrimSpace(second))
	result.SecretLeaked = boolPtr(strings.Contains(first, secretMarker))
	ref := refForLabel(first, "Benchmark action")
	if ref == "" {
		result.Notes = "No ref could be parsed from the current snapshot text; stale-ref and audit checks are unmeasured."
		return result
	}
	if _, err := client.tool("browser_evaluate", map[string]any{"function": `() => document.querySelector('#target').setAttribute('aria-label','Changed benchmark action')`}); err != nil {
		result.Error = "mutate benchmark target: " + err.Error()
		return result
	}
	clickRaw, clickErr := client.tool("browser_click", map[string]any{"element": "Benchmark action", "ref": strings.TrimPrefix(ref, "@")})
	result.StaleRefRejected = boolPtr(clickErr != nil || strings.Contains(strings.ToLower(string(clickRaw)), "error"))
	result.AuditReceipt = boolPtr(false)
	result.Notes = "Live current npx MCP. Audit=false means the stdio response did not include a durable per-command audit receipt; it is not a claim about wrapper-level logging."
	return result
}

var refPattern = regexp.MustCompile(`(?:@|ref=)([A-Za-z0-9_-]+)`)

func refForLabel(snapshot, label string) string {
	for _, line := range strings.Split(snapshot, "\n") {
		if !strings.Contains(line, label) {
			continue
		}
		match := refPattern.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		if strings.HasPrefix(match[0], "@") {
			return "@" + match[1]
		}
		return match[1]
	}
	return ""
}

func toolText(raw []byte) string {
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return string(raw)
	}
	var parts []string
	for _, content := range response.Result.Content {
		parts = append(parts, content.Text)
	}
	return strings.Join(parts, "\n")
}

type mcpClient struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	reader  *bufio.Reader
	stderr  bytes.Buffer
	nextID  int
}

func newMCPClient(ctx context.Context, name string, args ...string) (*mcpClient, error) {
	command := processtree.CommandContext(ctx, name, args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	client := &mcpClient{command: command, stdin: stdin, reader: bufio.NewReader(stdout)}
	command.Stderr = &client.stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	return client, nil
}

func (c *mcpClient) call(method string, params any) ([]byte, error) {
	c.nextID++
	id := c.nextID
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := json.NewEncoder(c.stdin).Encode(request); err != nil {
		return nil, err
	}
	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read MCP response: %w (%s)", err, strings.TrimSpace(c.stderr.String()))
		}
		var envelope struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(line, &envelope) == nil && envelope.ID == id {
			return line, nil
		}
	}
}

func (c *mcpClient) notify(method string, params any) error {
	return json.NewEncoder(c.stdin).Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *mcpClient) tool(name string, arguments map[string]any) ([]byte, error) {
	raw, err := c.call("tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return nil, err
	}
	var response struct {
		Error  any `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) == nil && (response.Error != nil || response.Result.IsError) {
		return raw, errors.New("MCP tool returned an error")
	}
	return raw, nil
}

func (c *mcpClient) close() {
	_ = c.stdin.Close()
	_ = c.command.Wait()
}

func external(ctx context.Context, name string, args ...string) (string, error) {
	command := processtree.CommandContext(ctx, name, args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return output.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

func restoreEnv(key, value string, had bool) {
	if had {
		_ = os.Setenv(key, value)
	} else {
		_ = os.Unsetenv(key)
	}
}

func approximateTokens(bytes int) int { return (bytes + 3) / 4 }
func boolPtr(value bool) *bool        { return &value }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "snapref-benchmark:", err)
	os.Exit(1)
}
