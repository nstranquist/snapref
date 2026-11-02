// Package browsercontract defines the stable, dependency-light operation
// contract shared by the browser CLI, MCP bridge, script engine, and telemetry.
// It deliberately imports no product adapters, keeping the operation contract
// reusable by the CLI, scripts, and MCP server.
package browsercontract

import (
	"fmt"
	"sort"
	"strings"
)

type OperationID string
type Permission string
type TimeoutClass string
type ResultKind string

const (
	PermissionRead             Permission = "read"
	PermissionDrive            Permission = "drive"
	PermissionConditionalDrive Permission = "conditional-drive"
	PermissionConditionalWrite Permission = "conditional-host-write"
	PermissionHostRead         Permission = "host-read"

	TimeoutNone    TimeoutClass = "none"
	TimeoutDefault TimeoutClass = "default"
	TimeoutCapture TimeoutClass = "capture"
	TimeoutSettle  TimeoutClass = "settle"

	ResultNone       ResultKind = "none"
	ResultText       ResultKind = "text"
	ResultJSON       ResultKind = "json"
	ResultArtifact   ResultKind = "artifact"
	ResultCollection ResultKind = "collection"
)

const (
	List          OperationID = "list"
	Navigate      OperationID = "navigate"
	Click         OperationID = "click"
	ClickText     OperationID = "click_text"
	ClickXPath    OperationID = "click_xpath"
	Type          OperationID = "type"
	Select        OperationID = "select"
	Press         OperationID = "press"
	Wait          OperationID = "wait"
	WaitText      OperationID = "wait_text"
	WaitXPath     OperationID = "wait_xpath"
	Text          OperationID = "text"
	Eval          OperationID = "eval"
	Fetch         OperationID = "fetch"
	Screenshot    OperationID = "screenshot"
	Shot          OperationID = "shot"
	Record        OperationID = "record"
	Console       OperationID = "console"
	Snapshot      OperationID = "snapshot"
	Viewport      OperationID = "viewport"
	AssertVisible OperationID = "assert_visible"
	AssertCount   OperationID = "assert_count"
	AssertText    OperationID = "assert_text"
	AssertURL     OperationID = "assert_url"
	ScriptRun     OperationID = "script_run"
)

// Operation is the cross-surface contract for one generic capability.
// MCPGrant names the MCP permission that grants the script operation when the
// operation does not have a dedicated MCP tool (click_text/click_xpath).
type Operation struct {
	ID         OperationID
	CLIVerb    string
	MCPTool    string
	MCPGrant   string
	ScriptVerb string
	Permission Permission
	Timeout    TimeoutClass
	Telemetry  string
	Result     ResultKind
}

var operations = []Operation{
	{List, "list", "browser_list", "browser_list", "", PermissionRead, TimeoutNone, "browser.list", ResultCollection},
	{Navigate, "navigate", "browser_navigate", "browser_navigate", "navigate", PermissionDrive, TimeoutDefault, "browser.navigate", ResultNone},
	{Click, "click", "browser_click", "browser_click", "click", PermissionDrive, TimeoutDefault, "browser.click", ResultNone},
	{ClickText, "", "", "browser_click", "click_text", PermissionDrive, TimeoutDefault, "browser.click_text", ResultNone},
	{ClickXPath, "", "", "browser_click", "click_xpath", PermissionDrive, TimeoutDefault, "browser.click_xpath", ResultNone},
	{Type, "type", "browser_type", "browser_type", "type", PermissionDrive, TimeoutDefault, "browser.type", ResultNone},
	{Select, "select", "browser_select", "browser_select", "select", PermissionDrive, TimeoutDefault, "browser.select", ResultNone},
	{Press, "press", "browser_press", "browser_press", "press", PermissionDrive, TimeoutDefault, "browser.press", ResultNone},
	{Wait, "wait", "browser_wait", "browser_wait", "wait", PermissionRead, TimeoutDefault, "browser.wait", ResultNone},
	{WaitText, "", "", "browser_wait", "wait_text", PermissionRead, TimeoutDefault, "browser.wait_text", ResultNone},
	{WaitXPath, "", "", "browser_wait", "wait_xpath", PermissionRead, TimeoutDefault, "browser.wait_xpath", ResultNone},
	{Text, "text", "browser_text", "browser_text", "text", PermissionRead, TimeoutDefault, "browser.text", ResultText},
	{Eval, "eval", "browser_eval", "browser_eval", "eval", PermissionDrive, TimeoutDefault, "browser.eval", ResultJSON},
	{Fetch, "fetch", "browser_fetch", "browser_fetch", "fetch", PermissionDrive, TimeoutDefault, "browser.fetch", ResultText},
	{Screenshot, "screenshot", "browser_screenshot", "browser_screenshot", "screenshot", PermissionConditionalWrite, TimeoutCapture, "browser.screenshot", ResultArtifact},
	{Shot, "shot", "browser_shot", "browser_shot", "", PermissionConditionalWrite, TimeoutCapture, "browser.shot", ResultArtifact},
	{Record, "record", "browser_record", "browser_record", "", PermissionConditionalWrite, TimeoutCapture, "browser.record", ResultArtifact},
	{Console, "console", "browser_console", "browser_console", "console", PermissionConditionalDrive, TimeoutSettle, "browser.console", ResultCollection},
	{Snapshot, "snapshot", "browser_snapshot", "browser_snapshot", "snapshot", PermissionRead, TimeoutDefault, "browser.snapshot", ResultCollection},
	{Viewport, "viewport", "", "", "viewport", PermissionDrive, TimeoutDefault, "browser.viewport", ResultNone},
	{AssertVisible, "assert", "", "browser_wait", "assert_visible", PermissionRead, TimeoutDefault, "browser.assert_visible", ResultJSON},
	{AssertCount, "assert", "", "browser_eval", "assert_count", PermissionRead, TimeoutDefault, "browser.assert_count", ResultJSON},
	{AssertText, "assert", "", "browser_text", "assert_text", PermissionRead, TimeoutDefault, "browser.assert_text", ResultJSON},
	{AssertURL, "", "", "browser_eval", "assert_url", PermissionRead, TimeoutDefault, "browser.assert_url", ResultJSON},
	{ScriptRun, "script run", "browser_script_run", "browser_script_run", "", PermissionHostRead, TimeoutDefault, "browser.script_run", ResultJSON},
}

func All() []Operation { return append([]Operation(nil), operations...) }

func ByID(id OperationID) (Operation, bool) {
	for _, operation := range operations {
		if operation.ID == id {
			return operation, true
		}
	}
	return Operation{}, false
}

func ByMCP(tool string) (Operation, bool) {
	for _, operation := range operations {
		if operation.MCPTool == tool {
			return operation, true
		}
	}
	return Operation{}, false
}

func ByScript(verb string) (Operation, bool) {
	for _, operation := range operations {
		if operation.ScriptVerb == verb {
			return operation, true
		}
	}
	return Operation{}, false
}

func ByCLIVerb(verb string) (Operation, bool) {
	if verb == "screenshot-current" {
		return ByID(Screenshot)
	}
	if verb == "shot" {
		return ByID(Shot)
	}
	if verb == "record" {
		return ByID(Record)
	}
	for _, operation := range operations {
		if operation.CLIVerb == verb {
			return operation, true
		}
	}
	return Operation{}, false
}

func ScriptOperations() []Operation {
	out := make([]Operation, 0, len(operations))
	for _, operation := range operations {
		if operation.ScriptVerb != "" {
			out = append(out, operation)
		}
	}
	return out
}

func ScriptOperationsGrantedBy(tool string) []Operation {
	out := make([]Operation, 0, 3)
	for _, operation := range operations {
		if operation.ScriptVerb != "" && operation.MCPGrant == tool {
			out = append(out, operation)
		}
	}
	return out
}

func Validate() error {
	ids := map[OperationID]bool{}
	mcp := map[string]bool{}
	script := map[string]bool{}
	telemetry := map[string]bool{}
	for i, operation := range operations {
		if operation.ID == "" || operation.Permission == "" || operation.Timeout == "" || operation.Telemetry == "" || operation.Result == "" {
			return fmt.Errorf("operation %d has an incomplete contract: %+v", i, operation)
		}
		if ids[operation.ID] {
			return fmt.Errorf("duplicate operation id %q", operation.ID)
		}
		ids[operation.ID] = true
		if operation.MCPTool != "" {
			if mcp[operation.MCPTool] {
				return fmt.Errorf("duplicate MCP tool %q", operation.MCPTool)
			}
			mcp[operation.MCPTool] = true
		}
		if operation.ScriptVerb != "" {
			if script[operation.ScriptVerb] {
				return fmt.Errorf("duplicate script verb %q", operation.ScriptVerb)
			}
			script[operation.ScriptVerb] = true
		}
		if telemetry[operation.Telemetry] {
			return fmt.Errorf("duplicate telemetry name %q", operation.Telemetry)
		}
		telemetry[operation.Telemetry] = true
		if !strings.HasPrefix(operation.Telemetry, "browser.") {
			return fmt.Errorf("operation %q telemetry must start with browser.", operation.ID)
		}
	}
	return nil
}

func MCPTools() []string {
	var out []string
	for _, operation := range operations {
		if operation.MCPTool != "" {
			out = append(out, operation.MCPTool)
		}
	}
	sort.Strings(out)
	return out
}
