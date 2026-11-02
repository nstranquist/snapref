package browsermcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nstranquist/snapref/browser"
)

const PolicyWorkflowSchema = "snapref.demo.mcp-policy.v1"

const (
	policySession     = "demo"
	policyAllowedURL  = "https://example.com/proof"
	policyBlockedURL  = "https://blocked.example/proof"
	policySnapshotRef = "sr-0123456789ab:e1"
)

// PolicyStep is one audited MCP tool call in the policy demo.
type PolicyStep struct {
	Tool     string `json:"tool"`
	URL      string `json:"url,omitempty"`
	Allowed  bool   `json:"allowed"`
	OK       bool   `json:"ok"`
	Audited  bool   `json:"audited"`
	Detail   string `json:"detail,omitempty"`
	Response string `json:"response,omitempty"`
}

// PolicyWorkflowReceipt is the operator-visible proof of MCP grants.
type PolicyWorkflowReceipt struct {
	OK              bool       `json:"ok"`
	Schema          string     `json:"schema"`
	Workflow        string     `json:"workflow"`
	Snapshot        PolicyStep `json:"snapshot"`
	ClickDenied     PolicyStep `json:"click_denied"`
	NavigateDenied  PolicyStep `json:"navigate_denied"`
	NavigateGranted PolicyStep `json:"navigate_granted"`
	OriginBlocked   PolicyStep `json:"origin_blocked"`
}

type policyFakeDriver struct {
	navigated []string
	clicked   []string
	snapshots int
}

func (f *policyFakeDriver) List() ([]SessionInfo, error) {
	return []SessionInfo{{Name: policySession, Port: 9222, Alive: true}}, nil
}
func (f *policyFakeDriver) Navigate(_ context.Context, session, pageURL string, _ ...browser.NavigateOptions) error {
	f.navigated = append(f.navigated, session+"|"+pageURL)
	return nil
}
func (f *policyFakeDriver) Click(_ context.Context, _, target string) error {
	f.clicked = append(f.clicked, target)
	return nil
}
func (f *policyFakeDriver) ClickText(context.Context, string, string) error    { return nil }
func (f *policyFakeDriver) ClickXPath(context.Context, string, string) error   { return nil }
func (f *policyFakeDriver) Type(context.Context, string, string, string) error { return nil }
func (f *policyFakeDriver) Select(context.Context, string, string, string) error {
	return nil
}
func (f *policyFakeDriver) Press(context.Context, string, string) error       { return nil }
func (f *policyFakeDriver) WaitVisible(context.Context, string, string) error { return nil }
func (f *policyFakeDriver) WaitText(context.Context, string, string) error    { return nil }
func (f *policyFakeDriver) WaitXPath(context.Context, string, string) error   { return nil }
func (f *policyFakeDriver) Text(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *policyFakeDriver) Eval(context.Context, string, string) (string, error) { return "", nil }
func (f *policyFakeDriver) Fetch(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *policyFakeDriver) Screenshot(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *policyFakeDriver) Console(context.Context, string, browser.ConsoleCaptureOptions) ([]browser.ConsoleEntry, error) {
	return nil, nil
}
func (f *policyFakeDriver) Snapshot(context.Context, string, browser.SnapshotOptions) (*browser.PageSnapshot, error) {
	f.snapshots++
	return &browser.PageSnapshot{
		URL: policyAllowedURL, Title: "Proof", Revision: "sr-0123456789ab",
		Matched: 1, Returned: 1, Counts: map[string]int{"button": 1},
		Elements: []browser.SnapshotElement{{
			Ref: policySnapshotRef, Role: "button", Tag: "button",
			Name: "Proof", Selector: "button", Visible: true,
		}},
	}, nil
}
func (f *policyFakeDriver) Viewport(context.Context, string, int, int) error { return nil }
func (f *policyFakeDriver) ResolveTarget(_ context.Context, _, target string) (string, error) {
	return target, nil
}

// RunPolicyWorkflow proves snapshot-ungated / drive-deny / drive-grant /
// origin-block through the shipped MCP stdio server. It does not launch Chrome.
func RunPolicyWorkflow() (PolicyWorkflowReceipt, error) {
	fixed := func() time.Time { return time.Unix(1, 0).UTC() }
	driver := &policyFakeDriver{}
	receipt := PolicyWorkflowReceipt{Schema: PolicyWorkflowSchema, Workflow: "mcp-policy"}
	off := Options{AllowDrive: false, Now: fixed}
	on := Options{AllowDrive: true, AllowOrigins: []string{"example.com"}, Now: fixed}

	var err error
	receipt.Snapshot, err = invokeMCPTool(driver, off, "browser_snapshot", map[string]any{"session": policySession})
	if err != nil {
		return receipt, err
	}
	if !receipt.Snapshot.OK || receipt.Snapshot.Allowed == false || driver.snapshots != 1 {
		return receipt, fmt.Errorf("mcp-policy: snapshot must run without SNAPREF_MCP_ALLOW_DRIVE")
	}

	receipt.ClickDenied, err = invokeMCPTool(driver, off, "browser_click", map[string]any{
		"session": policySession, "ref": policySnapshotRef,
	})
	if err != nil {
		return receipt, err
	}
	if receipt.ClickDenied.OK || receipt.ClickDenied.Allowed || len(driver.clicked) != 0 || !receipt.ClickDenied.Audited {
		return receipt, fmt.Errorf("mcp-policy: click without drive must fail closed and stay audited")
	}
	if !strings.Contains(receipt.ClickDenied.Response, "disabled") {
		return receipt, fmt.Errorf("mcp-policy: click deny must name the drive gate, got %q", receipt.ClickDenied.Response)
	}

	receipt.NavigateDenied, err = invokeMCPTool(driver, off, "browser_navigate", map[string]any{
		"session": policySession, "url": policyAllowedURL,
	})
	if err != nil {
		return receipt, err
	}
	if receipt.NavigateDenied.OK || receipt.NavigateDenied.Allowed || len(driver.navigated) != 0 || !receipt.NavigateDenied.Audited {
		return receipt, fmt.Errorf("mcp-policy: navigate without drive must fail closed and stay audited")
	}

	receipt.NavigateGranted, err = invokeMCPTool(driver, on, "browser_navigate", map[string]any{
		"session": policySession, "url": policyAllowedURL,
	})
	if err != nil {
		return receipt, err
	}
	if !receipt.NavigateGranted.Allowed || !receipt.NavigateGranted.OK || !receipt.NavigateGranted.Audited || len(driver.navigated) != 1 {
		return receipt, fmt.Errorf("mcp-policy: granted drive must reach the driver and be audited")
	}

	before := len(driver.navigated)
	receipt.OriginBlocked, err = invokeMCPTool(driver, on, "browser_navigate", map[string]any{
		"session": policySession, "url": policyBlockedURL,
	})
	if err != nil {
		return receipt, err
	}
	if receipt.OriginBlocked.Allowed || receipt.OriginBlocked.OK || !receipt.OriginBlocked.Audited || len(driver.navigated) != before {
		return receipt, fmt.Errorf("mcp-policy: blocked origin must fail closed and stay audited")
	}
	if !strings.Contains(receipt.OriginBlocked.Detail, "origin not allowlisted") &&
		!strings.Contains(receipt.OriginBlocked.Response, "allowlist") {
		return receipt, fmt.Errorf("mcp-policy: origin block must name the allowlist, got %+v", receipt.OriginBlocked)
	}

	receipt.OK = true
	return receipt, nil
}

func invokeMCPTool(driver Driver, opts Options, tool string, args map[string]any) (PolicyStep, error) {
	var audit bytes.Buffer
	opts.Driver = driver
	opts.Audit = &audit
	if opts.Now == nil {
		opts.Now = time.Now
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return PolicyStep{}, err
	}
	var out bytes.Buffer
	if err := Serve(bytes.NewReader(append(body, '\n')), &out, opts); err != nil {
		return PolicyStep{}, err
	}
	text, isErr, err := decodeMCPToolResult(out.Bytes())
	if err != nil {
		return PolicyStep{}, err
	}
	step := PolicyStep{Tool: tool, OK: !isErr, Response: text, Allowed: !isErr}
	if url, _ := args["url"].(string); url != "" {
		step.URL = url
	}
	line := strings.TrimSpace(audit.String())
	if line == "" {
		return step, nil
	}
	var rec map[string]any
	if json.Unmarshal([]byte(line), &rec) != nil {
		return step, nil
	}
	step.Audited = true
	if allowed, ok := rec["allowed"].(bool); ok {
		step.Allowed = allowed
	}
	if detail, ok := rec["detail"].(string); ok {
		step.Detail = detail
	}
	return step, nil
}

func decodeMCPToolResult(raw []byte) (string, bool, error) {
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", true, fmt.Errorf("mcp-policy: decode stdio response: %w", err)
	}
	if resp.Error != nil {
		return resp.Error.Message, true, nil
	}
	encoded, err := json.Marshal(resp.Result)
	if err != nil {
		return "", true, err
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return "", true, fmt.Errorf("mcp-policy: decode tool result: %w", err)
	}
	text := ""
	if len(result.Content) > 0 {
		text = result.Content[0].Text
	}
	return text, result.IsError, nil
}
