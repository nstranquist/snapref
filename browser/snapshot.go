package browser

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Snapshot — the read-only "agent eyes on the page" surface. It returns a
// structured inventory of the page's interactive elements (buttons, links,
// inputs, role=button, contenteditable, …) with, for each, an accessible name,
// a role, and a best-effort UNIQUE CSS selector the agent can hand straight to
// `click` / `type`. It closes the discovery gap that previously forced agents
// to hand-roll `eval(querySelectorAll(...))` for every unfamiliar page: one
// call yields the snapshot→selector→act loop without writing JS.
//
// It is read-only by construction — the DOM-walking expression only READS the
// page (no clicks, no navigation), so the MCP bridge exposes it UNGATED next to
// list/text/wait/managed screenshots, while navigation, action, caller-supplied
// code, and host paths stay gated.

// defaultSnapshotMax bounds the elements returned to keep the payload (and the
// agent's token budget) small. Override per-call with SnapshotOptions.Max.
const defaultSnapshotMax = 200

// defaultSnapshotEmptyWait closes the common navigate->snapshot hydration race
// without imposing a fixed delay on already-ready pages. A zero-value snapshot
// retries only while its visible interactive inventory is empty, up to this
// budget. Callers that need a stronger readiness contract can set Settle and/or
// WaitForSelector.
const defaultSnapshotEmptyWait = 750 * time.Millisecond

const snapshotSettlePollInterval = 100 * time.Millisecond

// snapshotHardCap bounds the elements the in-page JS collects before Go-side
// filtering, so a pathological page can never return an unbounded payload.
const snapshotHardCap = 1500

// SnapshotElement is one interactive element in the page inventory.
type SnapshotElement struct {
	Ref           string         `json:"ref"`                      // executable revision-bound handle: sr-…:e1
	Role          string         `json:"role"`                     // button | link | textbox | checkbox | radio | tab | combobox | label | …
	Tag           string         `json:"tag"`                      // lowercased element tag (button | a | input | …)
	Name          string         `json:"name"`                     // accessible name (visible text / aria-label / value / placeholder)
	Selector      string         `json:"selector"`                 // best-effort UNIQUE css selector to act on
	Context       []SnapshotRoot `json:"context,omitempty"`        // shadow/frame roots crossed before applying Selector
	Type          string         `json:"type,omitempty"`           // input type, when present
	Value         string         `json:"value,omitempty"`          // current non-sensitive value (inputs/textareas/selects)
	ValueRedacted bool           `json:"value_redacted,omitempty"` // true when a credential-like value was deliberately omitted
	Placeholder   string         `json:"placeholder,omitempty"`    // input placeholder
	Href          string         `json:"href,omitempty"`           // anchor href
	TestID        string         `json:"test_id,omitempty"`        // data-test / data-testid, when present
	Disabled      bool           `json:"disabled,omitempty"`
	Visible       bool           `json:"visible"`
	Fingerprint   string         `json:"fingerprint"`    // non-secret semantic identity used by executable refs
	Rect          *Rect          `json:"rect,omitempty"` // viewport-relative bounds (only when SnapshotOptions.IncludeRect)
}

// SnapshotRoot is one boundary crossed to reach an element from the top-level
// document. Selectors are evaluated relative to the preceding root.
type SnapshotRoot struct {
	Kind     SnapshotRootKind `json:"kind"`
	Selector string           `json:"selector"`
}

// SnapshotRootKind identifies the supported DOM boundary types.
type SnapshotRootKind string

const (
	SnapshotRootShadow SnapshotRootKind = "shadow"
	SnapshotRootFrame  SnapshotRootKind = "frame"
)

// Rect is a viewport-relative bounding box (CSS pixels).
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// PageSnapshot is the full interactive inventory of a page at one instant.
type PageSnapshot struct {
	URL               string            `json:"url"`
	Title             string            `json:"title"`
	Revision          string            `json:"revision"`  // binds executable refs to this inventory
	Matched           int               `json:"matched"`   // interactive elements that passed the filters
	Returned          int               `json:"returned"`  // elements actually in Elements (after the Max cap)
	Truncated         bool              `json:"truncated"` // Matched > Returned
	Counts            map[string]int    `json:"counts"`    // role -> count, over Elements
	Elements          []SnapshotElement `json:"elements"`
	ShadowRoots       int               `json:"shadow_roots,omitempty"`
	Frames            int               `json:"frames,omitempty"`
	CrossOriginFrames int               `json:"cross_origin_frames,omitempty"`
	Attempts          int               `json:"attempts"`          // DOM inventories sampled before returning
	ReadyBy           string            `json:"ready_by"`          // immediate | non_empty | stable | deadline
	ReadinessWaitMS   int64             `json:"readiness_wait_ms"` // time spent waiting after the first sample
	DurationMS        int64             `json:"duration_ms"`
}

// SnapshotOptions tunes a single Snapshot call. The zero value is a sensible
// default: visible interactive elements only, capped at defaultSnapshotMax, no
// rects.
type SnapshotOptions struct {
	Max             int           // cap returned elements (<=0 → defaultSnapshotMax)
	Roles           []string      // when non-empty, keep only these roles
	Filter          string        // when non-empty, keep only elements whose name contains this (case-insensitive)
	IncludeHidden   bool          // include non-visible elements (default: visible only)
	IncludeRect     bool          // attach viewport-relative bounds to each element
	WaitForSelector string        // when non-empty, wait for this top-level selector to become visible first
	Settle          time.Duration // when >0, require two equal non-empty inventories within this budget
}

// rawSnapshot is the shape the in-page JS returns; Go post-processes it.
type rawSnapshot struct {
	URL               string            `json:"url"`
	Title             string            `json:"title"`
	Elements          []SnapshotElement `json:"elements"`
	ShadowRoots       int               `json:"shadow_roots"`
	Frames            int               `json:"frames"`
	CrossOriginFrames int               `json:"cross_origin_frames"`
}

// snapshotJSExpr renders the DOM-walking expression. hardCap bounds the in-page
// collection; includeRect toggles bounding-box capture (kept off by default to
// keep payloads small). The expression is self-invoking and returns a value by
// design (Runtime.evaluate runs with returnByValue=true).
func snapshotJSExpr(hardCap int, includeRect bool) string {
	if hardCap <= 0 {
		hardCap = snapshotHardCap
	}
	return fmt.Sprintf(snapshotJSTemplate, hardCap, boolToJS(includeRect))
}

func boolToJS(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// snapshotJSTemplate is the in-page inventory walker. %d = hard cap, %s = include-rect bool.
const snapshotJSTemplate = `(() => {
  const HARD_CAP = %d, INCLUDE_RECT = %s;
  const SEL = [
    'a[href]', 'button', 'input:not([type=hidden])', 'select', 'textarea',
    'summary', 'label[for]', '[contenteditable=""]', '[contenteditable=true]',
    '[onclick]', '[role=button]', '[role=link]', '[role=checkbox]',
    '[role=radio]', '[role=switch]', '[role=tab]', '[role=menuitem]',
    '[role=menuitemcheckbox]', '[role=menuitemradio]', '[role=option]',
    '[role=combobox]', '[role=textbox]', '[role=searchbox]', '[role=slider]'
  ].join(',');
  const cssEsc = (s) => (window.CSS && CSS.escape) ? CSS.escape(s) : String(s).replace(/[^a-zA-Z0-9_-]/g, '\\$&');
  const norm = (s) => (s || '').replace(/\s+/g, ' ').trim();
  // Hidden from the accessibility tree: aria-hidden="true" or [inert] on the
  // element or any ancestor. Such nodes aren't real interactive targets (e.g. a
  // decorative marquee doubled for a seamless loop), so they're excluded from the
  // default inventory but still surface under --all (IncludeHidden).
  const inAriaHidden = (el) => {
    for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
      if (n.getAttribute('aria-hidden') === 'true' || n.hasAttribute('inert')) return true;
    }
    return false;
  };
  const isLocallyVisible = (el) => {
	const rects = el.getClientRects();
	if (!rects || rects.length === 0) return false;
	const view = el.ownerDocument && el.ownerDocument.defaultView;
	const st = view ? view.getComputedStyle(el) : getComputedStyle(el);
	if (st.visibility === 'hidden' || st.display === 'none' || parseFloat(st.opacity || '1') <= 0.01) return false;
	return !inAriaHidden(el);
  };
  const isVisible = (el, containers) => isLocallyVisible(el) && containers.every(isLocallyVisible);
  const roleOf = (el) => {
    const r = el.getAttribute('role');
    if (r) return r.trim().toLowerCase();
    const tag = el.tagName.toLowerCase();
    if (tag === 'a') return 'link';
    if (tag === 'button' || tag === 'summary') return 'button';
    if (tag === 'select') return 'combobox';
    if (tag === 'textarea') return 'textbox';
    if (tag === 'label') return 'label';
    if (tag === 'input') {
      const t = (el.getAttribute('type') || 'text').toLowerCase();
      if (t === 'button' || t === 'submit' || t === 'reset' || t === 'image') return 'button';
      if (t === 'checkbox') return 'checkbox';
      if (t === 'radio') return 'radio';
      return 'textbox';
    }
    if (el.isContentEditable) return 'textbox';
    return 'button';
  };
  const accName = (el) => {
    const aria = el.getAttribute('aria-label');
    if (aria && aria.trim()) return norm(aria).slice(0, 160);
    const by = el.getAttribute('aria-labelledby');
    if (by) {
      const doc = el.ownerDocument || document;
      const t = by.split(/\s+/).map((id) => { const n = doc.getElementById(id); return n ? n.innerText : ''; }).join(' ');
      if (norm(t)) return norm(t).slice(0, 160);
    }
    const tag = el.tagName.toLowerCase();
    if (tag === 'input' || tag === 'textarea' || tag === 'select') {
	  const labels = el.labels ? Array.from(el.labels).map((label) => label.innerText || label.textContent || '').join(' ') : '';
	  if (norm(labels)) return norm(labels).slice(0, 160);
	  if (tag === 'input') {
      const t = (el.getAttribute('type') || 'text').toLowerCase();
      if (t === 'submit' || t === 'button' || t === 'reset') return norm(el.value) || t;
	  }
      const ph = el.getAttribute('placeholder'); if (ph && ph.trim()) return norm(ph).slice(0, 160);
	  for (const a of ['title', 'name']) { const v = el.getAttribute(a); if (v && v.trim()) return norm(v).slice(0, 160); }
	  // Form values and option/body text are data, not accessible names. Do not
	  // copy them into discovery output when a control has no explicit name.
	  return '';
    }
    const txt = norm(el.innerText || el.textContent || '');
    if (txt) return txt.slice(0, 160);
    for (const a of ['title', 'alt', 'name', 'placeholder']) { const v = el.getAttribute(a); if (v && v.trim()) return norm(v).slice(0, 160); }
    return '';
  };
  const cssPath = (el, root) => {
	if (!el || el.nodeType !== 1) return '';
	for (const a of ['data-test', 'data-testid', 'data-test-id']) {
	  const v = el.getAttribute(a);
	  if (v) { const s = '[' + a + '="' + cssEsc(v) + '"]'; try { if (root.querySelectorAll(s).length === 1) return s; } catch (e) {} }
	}
	if (el.id) { const s = '#' + cssEsc(el.id); try { if (root.querySelectorAll(s).length === 1) return s; } catch (e) {} }
    const parts = [];
    let node = el;
    while (node && node.nodeType === 1 && node.tagName.toLowerCase() !== 'html') {
      if (node.id) { parts.unshift('#' + cssEsc(node.id)); break; }
      let part = node.tagName.toLowerCase();
      const parent = node.parentNode;
      if (parent && parent.children) {
        const sib = [...parent.children].filter((c) => c.tagName === node.tagName);
        if (sib.length > 1) part += ':nth-of-type(' + (sib.indexOf(node) + 1) + ')';
      }
      parts.unshift(part);
	  node = parent;
	  if (node === root) break;
	}
	return parts.join(' > ');
  };
  const testIdOf = (el) => {
    for (const a of ['data-test', 'data-testid', 'data-test-id']) { const v = el.getAttribute(a); if (v) return v; }
    return '';
  };
  // Snapshot output is commonly copied into agent transcripts and audit logs.
  // Credential-like form controls therefore expose only the fact that their
  // value was redacted, never the value itself.
  const hasSensitiveValue = (el) => {
    const type = norm(el.getAttribute('type')).toLowerCase();
    if (type === 'password' || type === 'hidden') return true;
    const autocomplete = norm(el.getAttribute('autocomplete')).toLowerCase();
    if (['current-password', 'new-password', 'one-time-code', 'cc-number', 'cc-csc'].includes(autocomplete)) return true;
    const hint = [el.getAttribute('name'), el.id, el.getAttribute('data-test'), el.getAttribute('data-testid'), el.getAttribute('aria-label'), el.getAttribute('placeholder')]
      .map((v) => norm(v).toLowerCase()).join(' ');
    return /(^|[^a-z0-9])(password|passwd|passcode|pwd|secret|otp|cvc|cvv)([^a-z0-9]|$)/.test(hint) ||
      /(api[-_ ]?key|client[-_ ]?secret|access[-_ ]?token|refresh[-_ ]?token|auth[-_ ]?token|bearer[-_ ]?token|card[-_ ]?number)/.test(hint);
  };
  const seen = new Set(), elements = [];
  let shadowRoots = 0, frames = 0, crossOriginFrames = 0;
  const collect = (root, context, containers) => {
	if (!root || elements.length >= HARD_CAP) return;
	for (const el of root.querySelectorAll(SEL)) {
	  if (seen.has(el)) continue;
	  seen.add(el);
	  const tag = el.tagName.toLowerCase();
	  const e = {
		role: roleOf(el), tag, name: accName(el), selector: cssPath(el, root),
		visible: isVisible(el, containers), disabled: !!(el.disabled || el.getAttribute('aria-disabled') === 'true')
	  };
	  if (context.length) e.context = context.map((hop) => ({kind: hop.kind, selector: hop.selector}));
	  const type = el.getAttribute('type'); if (type) e.type = type.toLowerCase();
	  if (tag === 'a') { const h = el.getAttribute('href'); if (h) e.href = h; }
	  if (tag === 'input' || tag === 'textarea' || tag === 'select') {
		if (hasSensitiveValue(el)) e.value_redacted = true;
		else if (el.value) e.value = String(el.value).slice(0, 160);
		const ph = el.getAttribute('placeholder'); if (ph) e.placeholder = ph;
	  }
	  const tid = testIdOf(el); if (tid) e.test_id = tid;
	  if (INCLUDE_RECT) {
		const r = el.getBoundingClientRect(); let x = r.x, y = r.y;
		for (const container of containers) { if (container.tagName && /^(IFRAME|FRAME)$/.test(container.tagName)) { const cr = container.getBoundingClientRect(); x += cr.x; y += cr.y; } }
		e.rect = { x: Math.round(x), y: Math.round(y), w: Math.round(r.width), h: Math.round(r.height) };
	  }
	  elements.push(e);
	  if (elements.length >= HARD_CAP) return;
	}
	for (const node of root.querySelectorAll('*')) {
	  if (elements.length >= HARD_CAP) return;
	  const selector = cssPath(node, root);
	  if (node.shadowRoot && selector) {
		shadowRoots++;
		collect(node.shadowRoot, context.concat([{kind: 'shadow', selector}]), containers.concat([node]));
	  }
	  if (node.tagName && /^(IFRAME|FRAME)$/.test(node.tagName) && selector) {
		frames++;
		let child = null;
		try { child = node.contentDocument; } catch (err) {}
		if (child) collect(child, context.concat([{kind: 'frame', selector}]), containers.concat([node]));
		else crossOriginFrames++;
	  }
	}
  };
  collect(document, [], []);
  return { url: location.href, title: document.title || '', elements, shadow_roots: shadowRoots, frames, cross_origin_frames: crossOriginFrames };
})()`

// postProcessSnapshot applies the Go-side filters (visibility, role allow-list,
// name substring), caps the result, recomputes per-role counts, and assigns
// contiguous revision-bound refs. It is pure and deterministic — the unit/bench target.
func postProcessSnapshot(raw rawSnapshot, opts SnapshotOptions, durationMS int64) *PageSnapshot {
	max := opts.Max
	if max <= 0 {
		max = defaultSnapshotMax
	}
	roleSet := map[string]bool{}
	for _, r := range opts.Roles {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" {
			roleSet[r] = true
		}
	}
	filter := strings.ToLower(strings.TrimSpace(opts.Filter))

	filtered := make([]SnapshotElement, 0, len(raw.Elements))
	for _, e := range raw.Elements {
		redactSnapshotElementValue(&e)
		if !opts.IncludeHidden && !e.Visible {
			continue
		}
		if len(roleSet) > 0 && !roleSet[e.Role] {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(e.Name), filter) {
			continue
		}
		if !opts.IncludeRect {
			e.Rect = nil
		}
		filtered = append(filtered, e)
	}
	matched := len(filtered)
	truncated := false
	if matched > max {
		filtered = filtered[:max]
		truncated = true
	}
	counts := make(map[string]int, 8)
	for i := range filtered {
		filtered[i].Fingerprint = snapshotElementFingerprint(filtered[i])
	}
	revision := snapshotRevision(raw.URL, raw.Title, filtered)
	for i := range filtered {
		filtered[i].Ref = fmt.Sprintf("%s:e%d", revision, i+1)
		role := filtered[i].Role
		if role == "" {
			role = "other"
		}
		counts[role]++
	}
	return &PageSnapshot{
		URL:               raw.URL,
		Title:             raw.Title,
		Revision:          revision,
		Matched:           matched,
		Returned:          len(filtered),
		Truncated:         truncated,
		Counts:            counts,
		Elements:          filtered,
		ShadowRoots:       raw.ShadowRoots,
		Frames:            raw.Frames,
		CrossOriginFrames: raw.CrossOriginFrames,
		DurationMS:        durationMS,
	}
}

// redactSnapshotElementValue is a second, Go-side safety boundary. The DOM
// extractor performs the primary classification, but callers may deserialize
// rawSnapshot values from another source or an older browser expression. This
// prevents an extractor regression from turning into a credential disclosure.
func redactSnapshotElementValue(e *SnapshotElement) {
	if e == nil || (e.Tag != "input" && e.Tag != "textarea" && e.Tag != "select") {
		return
	}
	if e.ValueRedacted || snapshotValueLooksSensitive(*e) {
		e.Value = ""
		e.ValueRedacted = true
	}
}

func snapshotValueLooksSensitive(e SnapshotElement) bool {
	typeName := strings.ToLower(strings.TrimSpace(e.Type))
	if typeName == "password" || typeName == "hidden" {
		return true
	}
	hint := strings.ToLower(strings.Join([]string{e.Name, e.Selector, e.TestID, e.Placeholder}, " "))
	for _, marker := range []string{
		"password", "passwd", "passcode", "[type=password]", "[type=hidden]",
		"api-key", "api_key", "apikey", "client-secret", "client_secret",
		"access-token", "access_token", "refresh-token", "refresh_token",
		"auth-token", "auth_token", "bearer-token", "bearer_token",
		"one-time-code", "card-number", "card_number",
	} {
		if strings.Contains(hint, marker) {
			return true
		}
	}
	for _, token := range strings.FieldsFunc(hint, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		switch token {
		case "pwd", "secret", "otp", "cvc", "cvv":
			return true
		}
	}
	return false
}

// RemoteSnapshot captures the interactive-element inventory of the session's
// current page. It is read-only: the in-page expression only reads the DOM.
func RemoteSnapshot(ctx context.Context, s *Session, opts SnapshotOptions, timeout time.Duration) (*PageSnapshot, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	start := time.Now()
	if selector := strings.TrimSpace(opts.WaitForSelector); selector != "" {
		if err := RemoteWaitVisible(ctx, s, selector, timeout); err != nil {
			return nil, fmt.Errorf("snapshot readiness selector %q: %w", selector, err)
		}
	}

	capture := func() (*PageSnapshot, error) {
		var raw rawSnapshot
		// url="" → evaluate against the current page (no navigation), like
		// RemoteEval's capture-current convention, so snapshot preserves
		// multi-step session state.
		if err := RemoteEval(ctx, s, "", snapshotJSExpr(snapshotHardCap, opts.IncludeRect), &raw, timeout); err != nil {
			return nil, err
		}
		return postProcessSnapshot(raw, opts, 0), nil
	}

	firstSampleAt := time.Now()
	snap, err := capture()
	if err != nil {
		return nil, err
	}
	attempts := 1
	readyBy := "immediate"

	settleBudget := opts.Settle
	adaptiveEmptyWait := settleBudget <= 0 && snap.Returned == 0
	if adaptiveEmptyWait {
		settleBudget = defaultSnapshotEmptyWait
	}
	if settleBudget > 0 {
		deadline := time.Now().Add(settleBudget)
		previousRevision := snap.Revision
		for time.Now().Before(deadline) {
			wait := snapshotSettlePollInterval
			if remaining := time.Until(deadline); remaining < wait {
				wait = remaining
			}
			if wait <= 0 {
				break
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}

			next, captureErr := capture()
			if captureErr != nil {
				return nil, captureErr
			}
			attempts++
			snap = next
			if adaptiveEmptyWait {
				if snap.Returned > 0 {
					readyBy = "non_empty"
					break
				}
			} else if snap.Returned > 0 && snap.Revision == previousRevision {
				readyBy = "stable"
				break
			}
			previousRevision = snap.Revision
		}
		if readyBy == "immediate" {
			readyBy = "deadline"
		}
	}

	snap.Attempts = attempts
	snap.ReadyBy = readyBy
	snap.ReadinessWaitMS = time.Since(firstSampleAt).Milliseconds()
	snap.DurationMS = time.Since(start).Milliseconds()
	if err := SaveSnapshotRefs(s.Name, snap); err != nil {
		return nil, fmt.Errorf("persist executable snapshot refs: %w", err)
	}
	_ = AppendSnapshotEvent(s.Name, snap)
	return snap, nil
}

func snapshotElementFingerprint(element SnapshotElement) string {
	payload := struct {
		Role     string         `json:"role"`
		Tag      string         `json:"tag"`
		Name     string         `json:"name"`
		Selector string         `json:"selector"`
		Type     string         `json:"type"`
		TestID   string         `json:"test_id"`
		Context  []SnapshotRoot `json:"context,omitempty"`
	}{element.Role, element.Tag, element.Name, element.Selector, element.Type, element.TestID, element.Context}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:8])
}

func snapshotRevision(url, title string, elements []SnapshotElement) string {
	payload := struct {
		URL      string            `json:"url"`
		Title    string            `json:"title"`
		Elements []SnapshotElement `json:"elements"`
	}{url, title, elements}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("sr-%x", sum[:6])
}

// SnapshotEvent is one telemetry row for the self-eval loop: every snapshot
// records the page, how many interactive elements it found, what it returned,
// and how long it took. Appended to snapshot-events.jsonl under the browser home.
type SnapshotEvent struct {
	SchemaVersion int       `json:"schema_version,omitempty"`
	BuildVersion  string    `json:"build_version,omitempty"`
	Timestamp     time.Time `json:"ts"`
	Session       string    `json:"session"`
	URL           string    `json:"url"`
	Matched       int       `json:"matched"`
	Returned      int       `json:"returned"`
	Truncated     bool      `json:"truncated"`
	DurationMS    int64     `json:"duration_ms"`
	Revision      string    `json:"revision,omitempty"`
	Attempts      int       `json:"attempts,omitempty"`
	ReadyBy       string    `json:"ready_by,omitempty"`
}

// SnapshotEventLogPath is the per-browser-home JSONL snapshot telemetry log.
func SnapshotEventLogPath() (string, error) {
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "snapshot-events.jsonl"), nil
}

// AppendSnapshotEvent records one snapshot's metrics. Best-effort: telemetry
// failure never fails the snapshot.
func AppendSnapshotEvent(session string, snap *PageSnapshot) error {
	if snap == nil {
		return nil
	}
	path, err := SnapshotEventLogPath()
	if err != nil {
		return err
	}
	body, err := json.Marshal(SnapshotEvent{
		SchemaVersion: BrowserTelemetrySchemaVersion,
		BuildVersion:  BrowserBuildVersion(),
		Timestamp:     time.Now().UTC(),
		Session:       session,
		URL:           snap.URL,
		Matched:       snap.Matched,
		Returned:      snap.Returned,
		Truncated:     snap.Truncated,
		DurationMS:    snap.DurationMS,
		Revision:      snap.Revision,
		Attempts:      snap.Attempts,
		ReadyBy:       snap.ReadyBy,
	})
	if err != nil {
		return err
	}
	return appendAndRotateJSONL(path, body, 0o600)
}
