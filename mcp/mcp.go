package browsermcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/nstranquist/snapref/browser"
	"github.com/nstranquist/snapref/contract"
	"github.com/nstranquist/snapref/script"
)

// mcpProtocolVersion tracks the MCP stdio baseline (matches the gtm server).
const mcpProtocolVersion = "2024-11-05"

// Options configures the server. Driver and Now are injectable for tests.
type Options struct {
	Driver         Driver
	AllowDrive     bool // enable ALL actuation tools (navigate/click/type/press/eval/fetch)
	AllowHostRead  bool // allow browser_script_run to read a caller-selected host file
	AllowHostWrite bool // allow browser_screenshot to write to a caller-selected host path
	// AllowedTools is a finer gate than AllowDrive: a set of drive-tool names
	// (e.g. "browser_navigate") that are individually enabled. A drive tool runs
	// iff AllowDrive is true OR it is in this set. Ignored when AllowDrive is true.
	AllowedTools map[string]bool
	// AllowOrigins, when non-empty, restricts URL-bearing tools (navigate/fetch)
	// to these origins (host exact or subdomain suffix; scheme/port ignored).
	// Empty = no restriction (operator opted into driving).
	AllowOrigins []string
	Audit        io.Writer // nil = no audit log; every actuation attempt is recorded
	Now          func() time.Time
}

// driveAllowed reports whether a tool may run. Read-only tools always may; a
// drive tool needs AllowDrive (full) or per-verb membership in AllowedTools.
func (s *server) driveAllowed(tool string, args map[string]any) bool {
	operation, ok := browsercontract.ByMCP(tool)
	if !ok {
		return true
	}
	requiresDrive := operation.Permission == browsercontract.PermissionDrive
	if operation.Permission == browsercontract.PermissionConditionalDrive {
		navigate, _ := args["navigate"].(string)
		reload, _ := args["reload"].(bool)
		evalJS, _ := args["eval"].(string)
		requiresDrive = strings.TrimSpace(navigate) != "" || reload || strings.TrimSpace(evalJS) != ""
	}
	return !requiresDrive || s.opts.AllowDrive || s.opts.AllowedTools[tool]
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type server struct {
	opts Options
}

// Serve runs the newline-delimited JSON-RPC 2.0 stdio server until in closes.
func Serve(in io.Reader, out io.Writer, opts Options) error {
	if opts.Driver == nil {
		opts.Driver = NewDriver()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &server{opts: opts}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue // ignore malformed frames
		}
		resp, respond := s.handleRPC(req)
		if respond {
			if err := enc.Encode(resp); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func (s *server) handleRPC(req rpcRequest) (rpcResponse, bool) {
	if len(req.ID) == 0 {
		return rpcResponse{}, false // notification; no reply
	}
	base := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		base.Result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "snapref", "version": browser.BrowserBuildVersion()},
		}
	case "ping":
		base.Result = map[string]any{}
	case "tools/list":
		base.Result = map[string]any{"tools": s.tools()}
	case "tools/call":
		text, isErr := s.callTool(req.Params)
		base.Result = map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}
	default:
		base.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return base, true
}

func sessionSchema(extra map[string]any, required ...string) map[string]any {
	props := map[string]any{
		"session": map[string]any{"type": "string", "description": "Name of an open session (snapref open <name>)"},
	}
	for k, v := range extra {
		props[k] = v
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           props,
		"required":             append([]string{"session"}, required...),
	}
}

func (s *server) tools() []map[string]any {
	driveNote := " (actuation — disabled unless SNAPREF_MCP_ALLOW_DRIVE=1)"
	return []map[string]any{
		{
			"name":        "browser_list",
			"description": "List the open browser sessions (read-only).",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		},
		{
			"name":        "browser_text",
			"description": "Read element text by CSS selector or executable snapshot ref (read-only).",
			"inputSchema": sessionSchema(targetSchema()),
		},
		{
			"name":        "browser_screenshot",
			"description": "Capture the session's current page to a unique managed temp file and return its path (read-only; never inline bytes). A caller-selected path requires SNAPREF_MCP_ALLOW_HOST_WRITE=1 / --allow-host-write.",
			"inputSchema": sessionSchema(map[string]any{"path": map[string]any{"type": "string", "description": "Optional caller-selected output path; disabled unless host-write authority is explicitly enabled"}}),
		},
		{
			"name":        "browser_shot",
			"description": "Package the session's current page as publish-ready product screenshots (GitHub README / Product Hunt / OG). Writes a directory pack. A caller-selected path requires SNAPREF_MCP_ALLOW_HOST_WRITE=1 / --allow-host-write.",
			"inputSchema": sessionSchema(map[string]any{
				"preset":         map[string]any{"type": "string", "description": "github-readme, product-hunt, og, twitter, raw, or all"},
				"title":          map[string]any{"type": "string"},
				"caption":        map[string]any{"type": "string"},
				"theme":          map[string]any{"type": "string", "description": "light or dark"},
				"include_mobile": map[string]any{"type": "boolean"},
				"path":           map[string]any{"type": "string", "description": "Optional output directory; disabled unless host-write authority is explicitly enabled"},
			}),
		},
		{
			"name":        "browser_record",
			"description": "Start or stop a session screen recording and package MP4/GIF for GitHub README or Product Hunt. Stop writes a directory pack. A caller-selected path requires SNAPREF_MCP_ALLOW_HOST_WRITE=1 / --allow-host-write.",
			"inputSchema": sessionSchema(map[string]any{
				"action": map[string]any{"type": "string", "description": "start or stop"},
				"preset": map[string]any{"type": "string"},
				"title":  map[string]any{"type": "string"},
				"path":   map[string]any{"type": "string"},
			}, "action"),
		},
		{
			"name":        "browser_navigate",
			"description": "Navigate the session to a URL" + driveNote + ".",
			"inputSchema": sessionSchema(map[string]any{
				"url":          map[string]any{"type": "string"},
				"reload":       map[string]any{"type": "boolean", "description": "Reload after navigation settles (useful for SPA hash routers)"},
				"bypass_cache": map[string]any{"type": "boolean", "description": "Disable the page HTTP cache for this navigation/reload"},
			}, "url"),
		},
		{
			"name":        "browser_click",
			"description": "Click an element by CSS selector or executable snapshot ref after auto-waiting visible, stable, enabled, and unoccluded" + driveNote + ".",
			"inputSchema": sessionSchema(targetSchema()),
		},
		{
			"name":        "browser_type",
			"description": "Type text into an element by CSS selector or executable snapshot ref after the same actionability auto-wait" + driveNote + ".",
			"inputSchema": sessionSchema(mergeSchema(targetSchema(), map[string]any{"text": map[string]any{"type": "string"}}), "text"),
		},
		{
			"name":        "browser_press",
			"description": "Press a keyboard key (e.g. Enter, Tab) after the focused element passes actionability, if one is focused" + driveNote + ".",
			"inputSchema": sessionSchema(map[string]any{"key": map[string]any{"type": "string"}}, "key"),
		},
		{
			"name":        "browser_wait",
			"description": "Wait for an element matching a CSS selector or executable snapshot ref to become visible (read-only).",
			"inputSchema": sessionSchema(targetSchema()),
		},
		{
			"name": "browser_snapshot",
			"description": "Eyes on the page (read-only): return a structured inventory of the session's interactive elements — " +
				"buttons, links, inputs, role=button, contenteditable — each with an accessible name, role, and a UNIQUE css " +
				"executable revision-bound `ref` you can hand straight to browser_click / browser_type (selectors remain diagnostic). Use this to DISCOVER what's on an unfamiliar " +
				"page instead of hand-writing eval(querySelectorAll(...)); then act with the gated drive tools. " +
				"Returns {url,title,matched,returned,truncated,counts,elements[{ref,role,tag,name,selector,...}]}.",
			"inputSchema": sessionSchema(map[string]any{
				"max":               map[string]any{"type": "number", "description": "Cap returned elements (default 200)"},
				"role":              map[string]any{"type": "string", "description": "Keep only this role (e.g. button, link, textbox); comma-separate for several"},
				"filter":            map[string]any{"type": "string", "description": "Keep only elements whose accessible name contains this (case-insensitive)"},
				"include_hidden":    map[string]any{"type": "boolean", "description": "Include non-visible elements (default: visible only)"},
				"include_rect":      map[string]any{"type": "boolean", "description": "Attach viewport-relative bounds {x,y,w,h} to each element"},
				"wait_for_selector": map[string]any{"type": "string", "description": "Wait for this top-level CSS selector to become visible before inventory"},
				"settle":            map[string]any{"type": "string", "description": "Require two equal non-empty inventories within this duration (for example 1s)"},
			}),
		},
		{
			"name":        "browser_eval",
			"description": "Evaluate JavaScript in the session's current page and return the JSON result" + driveNote + ". (Arbitrary JS — the most powerful actuation; gated.)",
			"inputSchema": sessionSchema(map[string]any{"js": map[string]any{"type": "string"}}, "js"),
		},
		{
			"name":        "browser_fetch",
			"description": "Fetch a URL's HTML through the session and return it" + driveNote + ". (Subject to the navigation allowlist when configured.)",
			"inputSchema": sessionSchema(map[string]any{"url": map[string]any{"type": "string"}}, "url"),
		},
		{
			"name": "browser_script_run",
			"description": "Run a browserscript playbook file (.yaml/.json/.star) as ONE unit against an open session — the repeatable-workflow surface. Reading the caller-selected playbook requires SNAPREF_MCP_ALLOW_HOST_READ=1 / --allow-host-read. Read-only playbooks then run without drive authority; page actuation and caller-supplied page code require this bridge's actuation gate" + driveNote +
				". Audited + written to the per-run manifest like `snapref script run`. Returns {ok,run_id,steps[],outputs}.",
			"inputSchema": sessionSchema(map[string]any{
				"file":      map[string]any{"type": "string", "description": "Path to the playbook (.yaml/.json/.star)"},
				"vars":      map[string]any{"type": "object", "description": "Parameters (${k} interpolation / params[k]) — values stringified"},
				"max_steps": map[string]any{"type": "number", "description": "Cap executed steps (0 = all)"},
			}, "file"),
		},
		{
			"name": "browser_console",
			"description": "Capture client-side console.*, uncaught exceptions, and Log-domain entries via CDP — assert on JS errors without injecting window.onerror" + driveNote +
				". Provide at most one trigger (navigate / reload / eval) to drive the page before capturing; omit all three to passively read the current console. Returns {count,errors,warnings,entries[]}.",
			"inputSchema": sessionSchema(map[string]any{
				"navigate":    map[string]any{"type": "string", "description": "Navigate to this URL first, then capture the load (the deterministic mode)"},
				"reload":      map[string]any{"type": "boolean", "description": "Reload the current URL before capturing"},
				"eval":        map[string]any{"type": "string", "description": "Run this JS (drive an interaction), then capture its console output"},
				"duration":    map[string]any{"type": "string", "description": "Settle window to listen, e.g. 1.5s (default 1.5s)"},
				"level":       map[string]any{"type": "string", "enum": []string{"all", "warning", "error"}, "description": "Minimum level to keep (default all)"},
				"kind":        map[string]any{"type": "string", "enum": []string{"all", "js", "log"}, "description": "all | js (console+exception only, drops network/resource Log noise) | log (Log-domain only)"},
				"failOnError": map[string]any{"type": "boolean", "description": "Return an error result if any error-level entry is captured (assert no console errors)"},
			}),
		},
	}
}

func targetSchema() map[string]any {
	return map[string]any{
		"selector": map[string]any{"type": "string", "description": "CSS selector (compatibility input)"},
		"ref":      map[string]any{"type": "string", "description": "Executable ref from the latest browser_snapshot, e.g. sr-abc123def456:e4"},
	}
}

func mergeSchema(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, part := range parts {
		for key, value := range part {
			out[key] = value
		}
	}
	return out
}

func targetArgument(args map[string]any) (string, error) {
	selector, _ := args["selector"].(string)
	ref, _ := args["ref"].(string)
	selector = strings.TrimSpace(selector)
	ref = strings.TrimSpace(ref)
	if selector != "" && ref != "" {
		return "", errors.New("provide exactly one of selector or ref")
	}
	if ref != "" {
		if err := browser.ValidateExecutableSnapshotRef(ref); err != nil {
			return "", err
		}
		return ref, nil
	}
	if selector != "" {
		return selector, nil
	}
	return "", errors.New("provide exactly one of selector or ref")
}

// callTool dispatches a tools/call. Returns (text, isError). Actuation tools are
// gated by AllowDrive and audited; read tools run freely.
func (s *server) callTool(params json.RawMessage) (string, bool) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "invalid params: " + err.Error(), true
	}
	str := func(k string) string { v, _ := p.Arguments[k].(string); return v }
	boolArg := func(k string) bool { v, _ := p.Arguments[k].(bool); return v }
	session := str("session")
	target := func() (string, bool) {
		value, err := targetArgument(p.Arguments)
		if err != nil {
			return err.Error(), false
		}
		return value, true
	}

	if !s.driveAllowed(p.Name, p.Arguments) {
		s.audit(p.Name, session, false, false, "driving disabled")
		return p.Name + " is disabled on this bridge. Enable all actuation with SNAPREF_MCP_ALLOW_DRIVE=1 / --allow-drive, or this verb only with --allow-tools (SNAPREF_MCP_ALLOW_TOOLS).", true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	d := s.opts.Driver

	switch p.Name {
	case "browser_list":
		sessions, err := d.List()
		if err != nil {
			return err.Error(), true
		}
		b, _ := json.MarshalIndent(map[string]any{"sessions": sessions}, "", "  ")
		return string(b), false
	case "browser_text":
		value, ok := target()
		if !ok {
			return value, true
		}
		text, err := d.Text(ctx, session, value)
		if err != nil {
			return err.Error(), true
		}
		return text, false
	case "browser_wait":
		value, ok := target()
		if !ok {
			return value, true
		}
		if err := d.WaitVisible(ctx, session, value); err != nil {
			return err.Error(), true
		}
		return "ok: visible " + value, false
	case "browser_snapshot":
		return s.runSnapshot(ctx, p.Arguments)
	case "browser_screenshot":
		requestedPath := strings.TrimSpace(str("path"))
		if requestedPath != "" && !s.opts.AllowHostWrite {
			s.audit(p.Name, session, false, false, "host write disabled")
			return "browser_screenshot path is disabled on this bridge. Omit path for a unique managed temp file, or explicitly enable host writes with SNAPREF_MCP_ALLOW_HOST_WRITE=1 / --allow-host-write.", true
		}
		path, err := d.Screenshot(ctx, session, requestedPath)
		if err != nil {
			s.audit(p.Name, session, requestedPath != "", false, err.Error())
			return err.Error(), true
		}
		if requestedPath != "" {
			s.audit(p.Name, session, true, true, "path="+requestedPath)
		}
		return "screenshot saved: " + path, false
	case "browser_shot":
		return s.runProductShot(ctx, session, str("path"), str("preset"), str("title"), str("caption"), str("theme"), boolArg("include_mobile"))
	case "browser_record":
		return s.runProductRecord(session, str("action"), str("path"), str("preset"), str("title"))
	case "browser_navigate":
		if blocked := s.checkOrigin(p.Name, session, str("url")); blocked != "" {
			return blocked, true
		}
		opts := browser.NavigateOptions{Reload: boolArg("reload"), BypassCache: boolArg("bypass_cache")}
		detail := "url=" + str("url")
		if opts.Reload {
			detail += " reload=true"
		}
		if opts.BypassCache {
			detail += " bypass_cache=true"
		}
		return s.runDrive(p.Name, session, detail, func() error { return d.Navigate(ctx, session, str("url"), opts) })
	case "browser_click":
		value, ok := target()
		if !ok {
			return value, true
		}
		return s.runDrive(p.Name, session, "target="+value, func() error { return d.Click(ctx, session, value) })
	case "browser_type":
		value, ok := target()
		if !ok {
			return value, true
		}
		return s.runDrive(p.Name, session, "target="+value, func() error { return d.Type(ctx, session, value, str("text")) })
	case "browser_press":
		return s.runDrive(p.Name, session, "key="+str("key"), func() error { return d.Press(ctx, session, str("key")) })
	case "browser_eval":
		out, err := d.Eval(ctx, session, str("js"))
		if err != nil {
			s.audit(p.Name, session, true, false, err.Error())
			return err.Error(), true
		}
		s.audit(p.Name, session, true, true, "eval")
		return out, false
	case "browser_fetch":
		if blocked := s.checkOrigin(p.Name, session, str("url")); blocked != "" {
			return blocked, true
		}
		html, err := d.Fetch(ctx, session, str("url"))
		if err != nil {
			s.audit(p.Name, session, true, false, err.Error())
			return err.Error(), true
		}
		s.audit(p.Name, session, true, true, "url="+str("url"))
		return html, false
	case "browser_script_run":
		return s.runScript(ctx, p.Arguments)
	case "browser_console":
		return s.runConsole(ctx, p.Name, session, p.Arguments)
	default:
		return "unknown tool: " + p.Name, true
	}
}

// runConsole handles the gated browser_console tool: it validates the trigger /
// level / kind arguments, enforces the navigation allowlist for the navigate
// trigger, captures via the driver, audits the outcome, and returns a JSON
// {count,errors,warnings,entries} payload. With failOnError set, a capture that
// found error-level entries is surfaced as an error result (the payload still
// carries the entries so the agent sees what failed).
// runScript executes a browserscript playbook against an open session. The
// bridge's AllowDrive flows into the engine, so mutating verbs in the playbook
// respect the same gate as the individual drive tools. The run is audited and
// written to the per-run manifest (browserscript.FileTelemetry).
func (s *server) runScript(ctx context.Context, args map[string]any) (string, bool) {
	file, _ := args["file"].(string)
	if strings.TrimSpace(file) == "" {
		return "browser_script_run: file is required", true
	}
	session, _ := args["session"].(string)
	if !s.opts.AllowHostRead {
		s.audit("browser_script_run", session, false, false, "host read disabled")
		return "browser_script_run host file reads are disabled on this bridge. Enable explicitly with SNAPREF_MCP_ALLOW_HOST_READ=1 / --allow-host-read.", true
	}
	vars := map[string]string{}
	if m, ok := args["vars"].(map[string]any); ok {
		for k, v := range m {
			vars[k] = fmt.Sprintf("%v", v)
		}
	}
	maxSteps := 0
	if n, ok := args["max_steps"].(float64); ok {
		maxSteps = int(n)
	}

	opts := browserscript.RunOptions{
		AllowDrive:     s.opts.AllowDrive,
		AllowHostWrite: s.opts.AllowHostWrite,
		AllowedVerbs:   s.allowedScriptVerbs(),
		MaxSteps:       maxSteps,
		Session:        session,
		Vars:           vars,
		Telemetry:      browserscript.NewFileTelemetry(),
		Now:            s.opts.Now,
	}
	driver := scriptPolicyDriver{Driver: s.opts.Driver, server: s}

	var (
		rep    *browserscript.RunReport
		runErr error
	)
	if strings.EqualFold(filepath.Ext(file), ".star") {
		rep, runErr = browserscript.RunStarlarkFile(ctx, driver, file, opts)
	} else {
		plan, lerr := browserscript.LoadFile(file, vars)
		if lerr != nil {
			s.audit("browser_script_run", session, s.opts.AllowDrive, false, "load: "+lerr.Error())
			return "browser_script_run load: " + lerr.Error(), true
		}
		if session != "" {
			plan.Session = session
		}
		opts.Source = browserscript.SourceForPath(file)
		rep, runErr = browserscript.NewRunner(driver).Run(ctx, plan, opts)
	}
	if rep == nil {
		msg := "browser_script_run: failed"
		if runErr != nil {
			msg = "browser_script_run: " + runErr.Error()
		}
		s.audit("browser_script_run", session, s.opts.AllowDrive, false, msg)
		return msg, true
	}

	steps := make([]map[string]any, 0, len(rep.Steps))
	for _, st := range rep.Steps {
		m := map[string]any{"index": st.Index, "verb": st.Verb, "ok": st.OK}
		if st.Error != "" {
			m["error"] = st.Error
		}
		if len(st.Captured) > 0 {
			m["captured"] = st.Captured
		}
		steps = append(steps, m)
	}
	out := map[string]any{
		"ok": rep.OK && runErr == nil, "run_id": rep.RunID, "plan": rep.Plan,
		"source": rep.Source, "steps_total": len(rep.Steps), "steps": steps,
	}
	if len(rep.Outputs) > 0 {
		out["outputs"] = rep.Outputs
	}
	if runErr != nil {
		out["error"] = runErr.Error()
	}
	b, _ := json.Marshal(out)
	ok := rep.OK && runErr == nil
	privileged := s.opts.AllowHostRead || s.opts.AllowDrive || s.opts.AllowHostWrite || len(opts.AllowedVerbs) > 0
	s.audit("browser_script_run", rep.Session, privileged, ok, fmt.Sprintf("file=%s plan=%s ok=%v steps=%d", file, rep.Plan, ok, len(rep.Steps)))
	return string(b), !ok
}

func (s *server) allowedScriptVerbs() map[browserscript.Verb]bool {
	out := map[browserscript.Verb]bool{}
	for tool, allowed := range s.opts.AllowedTools {
		if !allowed {
			continue
		}
		for _, operation := range browsercontract.ScriptOperationsGrantedBy(tool) {
			out[browserscript.Verb(operation.ScriptVerb)] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scriptPolicyDriver keeps URL allowlists effective inside YAML/JSON/Starlark
// playbooks. Without this wrapper, a script could call the shared Driver
// directly and bypass the MCP tool-level origin checks.
type scriptPolicyDriver struct {
	Driver
	server *server
}

func (d scriptPolicyDriver) Navigate(ctx context.Context, session, rawURL string, opts ...browser.NavigateOptions) error {
	if !d.server.originAllowed(rawURL) {
		return fmt.Errorf("blocked: %s is not in the navigation allowlist (SNAPREF_MCP_ALLOW_ORIGINS)", rawURL)
	}
	return d.Driver.Navigate(ctx, session, rawURL, opts...)
}

func (d scriptPolicyDriver) Fetch(ctx context.Context, session, rawURL string) (string, error) {
	if !d.server.originAllowed(rawURL) {
		return "", fmt.Errorf("blocked: %s is not in the navigation allowlist (SNAPREF_MCP_ALLOW_ORIGINS)", rawURL)
	}
	return d.Driver.Fetch(ctx, session, rawURL)
}

func (d scriptPolicyDriver) Console(ctx context.Context, session string, opts browser.ConsoleCaptureOptions) ([]browser.ConsoleEntry, error) {
	if opts.Navigate != "" && !d.server.originAllowed(opts.Navigate) {
		return nil, fmt.Errorf("blocked: %s is not in the navigation allowlist (SNAPREF_MCP_ALLOW_ORIGINS)", opts.Navigate)
	}
	return d.Driver.Console(ctx, session, opts)
}

// runSnapshot handles the read-only browser_snapshot tool: it parses the
// element-inventory options, captures via the driver, and returns the JSON
// PageSnapshot. No actuation, so it is ungated — but a snapshot still reads the
// page, so we leave the audit log to the driver-level snapshot telemetry.
func (s *server) runSnapshot(ctx context.Context, args map[string]any) (string, bool) {
	str := func(k string) string { v, _ := args[k].(string); return v }
	boolArg := func(k string) bool { v, _ := args[k].(bool); return v }
	opts := browser.SnapshotOptions{
		Filter:          str("filter"),
		IncludeHidden:   boolArg("include_hidden"),
		IncludeRect:     boolArg("include_rect"),
		WaitForSelector: str("wait_for_selector"),
	}
	if rawSettle := strings.TrimSpace(str("settle")); rawSettle != "" {
		settle, err := time.ParseDuration(rawSettle)
		if err != nil || settle <= 0 {
			return fmt.Sprintf("bad settle %q (must be a positive duration)", rawSettle), true
		}
		opts.Settle = settle
	}
	if n, ok := args["max"].(float64); ok {
		opts.Max = int(n)
	}
	if role := strings.TrimSpace(str("role")); role != "" {
		for _, r := range strings.Split(role, ",") {
			if r = strings.TrimSpace(r); r != "" {
				opts.Roles = append(opts.Roles, r)
			}
		}
	}
	snap, err := s.opts.Driver.Snapshot(ctx, str("session"), opts)
	if err != nil {
		return err.Error(), true
	}
	b, _ := json.MarshalIndent(snap, "", "  ")
	return string(b), false
}

func (s *server) runConsole(ctx context.Context, tool, session string, args map[string]any) (string, bool) {
	str := func(k string) string { v, _ := args[k].(string); return v }
	boolArg := func(k string) bool { v, _ := args[k].(bool); return v }

	opts := browser.ConsoleCaptureOptions{
		Navigate: str("navigate"),
		Reload:   boolArg("reload"),
		Eval:     str("eval"),
		Level:    str("level"),
		Kind:     str("kind"),
	}
	// Argument validation below (duration / conflicting triggers / level / kind)
	// returns client errors without auditing — the audit log records gate
	// decisions and actuation outcomes, not malformed requests.
	if d := str("duration"); d != "" {
		parsed, err := time.ParseDuration(d)
		if err != nil {
			return "invalid duration " + d + ": " + err.Error(), true
		}
		opts.Window = parsed
	}

	triggers := 0
	if opts.Navigate != "" {
		triggers++
	}
	if opts.Reload {
		triggers++
	}
	if opts.Eval != "" {
		triggers++
	}
	if triggers > 1 {
		return "use at most one of navigate / reload / eval", true
	}
	switch opts.Level {
	case "", "all", "warning", "error":
	default:
		return "level must be all|warning|error", true
	}
	switch opts.Kind {
	case "", "all", "js", "log":
	default:
		return "kind must be all|js|log", true
	}
	if opts.Navigate != "" {
		if blocked := s.checkOrigin(tool, session, opts.Navigate); blocked != "" {
			return blocked, true
		}
	}

	entries, err := s.opts.Driver.Console(ctx, session, opts)
	if err != nil {
		s.audit(tool, session, true, false, err.Error())
		return err.Error(), true
	}

	errorCount, warnCount := 0, 0
	for _, e := range entries {
		switch e.Level {
		case "error":
			errorCount++
		case "warning", "warn":
			warnCount++
		}
	}
	detail := fmt.Sprintf("errors=%d warnings=%d", errorCount, warnCount)
	switch {
	case opts.Navigate != "":
		detail = "navigate=" + opts.Navigate + " " + detail
	case opts.Reload:
		detail = "reload " + detail
	case opts.Eval != "":
		detail = "eval " + detail
	}
	s.audit(tool, session, true, true, detail)

	if entries == nil {
		entries = []browser.ConsoleEntry{}
	}
	b, _ := json.MarshalIndent(map[string]any{
		"count":    len(entries),
		"errors":   errorCount,
		"warnings": warnCount,
		"entries":  entries,
	}, "", "  ")
	return string(b), boolArg("failOnError") && errorCount > 0
}

// checkOrigin enforces the optional navigation allowlist for URL-bearing tools.
// Returns "" when allowed, or an error message (and audits the refusal) when not.
func (s *server) checkOrigin(tool, session, rawurl string) string {
	if s.originAllowed(rawurl) {
		return ""
	}
	s.audit(tool, session, false, false, "origin not allowlisted: "+rawurl)
	return "blocked: " + rawurl + " is not in the navigation allowlist (SNAPREF_MCP_ALLOW_ORIGINS)"
}

// originAllowed reports whether rawurl's host matches the configured allowlist.
// An empty allowlist allows everything. Matching is host exact or subdomain
// suffix; scheme and port are ignored.
func (s *server) originAllowed(rawurl string) bool {
	if len(s.opts.AllowOrigins) == 0 {
		return true
	}
	u, err := url.Parse(rawurl)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	for _, a := range s.opts.AllowOrigins {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if i := strings.Index(a, "://"); i >= 0 {
			a = a[i+3:]
		}
		a = strings.TrimSuffix(a, "/")
		if h, _, splitErr := net.SplitHostPort(a); splitErr == nil {
			a = h
		}
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// runDrive executes a gated actuation, auditing the outcome.
func (s *server) runDrive(tool, session, detail string, fn func() error) (string, bool) {
	if err := fn(); err != nil {
		s.audit(tool, session, true, false, err.Error())
		return err.Error(), true
	}
	s.audit(tool, session, true, true, detail)
	return fmt.Sprintf("ok: %s on %q (%s)", tool, session, detail), false
}

func (s *server) audit(tool, session string, allowed, ok bool, detail string) {
	if s.opts.Audit == nil {
		return
	}
	rec := map[string]any{
		"ts": s.opts.Now().UTC().Format(time.RFC3339), "tool": tool,
		"session": session, "allowed": allowed, "ok": ok,
	}
	if detail != "" {
		rec["detail"] = detail
	}
	b, _ := json.Marshal(rec)
	_, _ = s.opts.Audit.Write(append(b, '\n'))
}
