// Package skills provides a runtime-loaded browser-skill harness for
// `pageskein`. Skills live on disk under ~/.pageskein/skills/
// (see SkillsRoot) so agents and humans can edit selectors, extractors,
// and hint docs without rebuilding the binary.
//
// Layout (mirrors browser-use/browser-harness's agent-workspace/domain-skills/):
//
//	~/.pageskein/skills/
//	├── _helpers/                 # shared JS helpers (future)
//	└── <domain>/
//	    ├── README.md             # agent-readable hints (selectors, traps)
//	    ├── selectors.yaml        # drift-check registry
//	    └── extract-<name>.js     # JS extractors run via Page.Eval
//
// Domain names follow browser-harness convention: the first DNS label
// of the hostname after stripping the leading "www." (so linkedin.com,
// www.linkedin.com, and m.linkedin.com all map to "linkedin").
package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Page is the minimum interface a skill needs. Implementations live in
// page_impls.go (StatelessPage, SessionPage). JS must be an expression;
// wrap statements as `(()=>{ ...; return r; })()`.
type Page interface {
	Navigate(ctx context.Context, url string) error
	Eval(ctx context.Context, js string, out any) error
}

// Domain identifies the skill that handles a URL. Values match the
// directory name under SkillsRoot.Path (e.g. "linkedin", "tiktok").
type Domain string

// Convenience constants for the domains we ship default skills for.
// Other domains are still discovered at runtime by directory presence;
// these are just well-known names callers can compare against.
const (
	DomainLinkedIn   Domain = "linkedin"
	DomainTikTok     Domain = "tiktok"
	DomainYouTube    Domain = "youtube"
	DomainInstagram  Domain = "instagram"
	DomainJobPosting Domain = "job-posting"
)

// hostAliases maps URL hosts to canonical domain dir names for non-obvious
// cases. Plain "<label>.<tld>" hosts are resolved by stripping www. and
// taking the first label, no entry needed.
var hostAliases = map[string]Domain{
	"youtu.be":                 DomainYouTube,
	"boards.greenhouse.io":     DomainJobPosting,
	"job-boards.greenhouse.io": DomainJobPosting,
	"jobs.lever.co":            DomainJobPosting,
	"jobs.eu.lever.co":         DomainJobPosting,
	"jobs.ashbyhq.com":         DomainJobPosting,
}

// SelectorPair is one (field, selector) entry, loaded from selectors.yaml.
type SelectorPair struct {
	Field    string `yaml:"field" json:"field"`
	Selector string `yaml:"selector" json:"selector"`
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"`
}

// SelectorSet is the parsed selectors.yaml for one domain.
type SelectorSet struct {
	Domain Domain         `json:"domain"`
	Pairs  []SelectorPair `yaml:"selectors" json:"pairs"`
}

// SelectorCheck is the per-selector probe result emitted by CheckSelectors.
type SelectorCheck struct {
	Field    string `json:"field"`
	Selector string `json:"selector"`
	Matched  int    `json:"matched"`
	Sample   string `json:"sample,omitempty"`
	Error    string `json:"error,omitempty"`
}

// DriftReport aggregates SelectorChecks for one URL.
type DriftReport struct {
	Domain         Domain          `json:"domain"`
	Host           string          `json:"host"`
	URL            string          `json:"url"`
	TotalSelectors int             `json:"total_selectors"`
	DeadSelectors  int             `json:"dead_selectors"`
	DriftPct       int             `json:"drift_pct"`
	Results        []SelectorCheck `json:"results"`
}

// ErrNoSkillForDomain is returned when a URL has no matching skill dir.
// Callers like felix-drift-check + linkedin-batch surface this as a
// "skipped" row.
var ErrNoSkillForDomain = errors.New("no skill for this URL's domain")

// ErrNoExtractor is returned when a domain dir exists but has no
// matching extract-<name>.js file.
var ErrNoExtractor = errors.New("no extractor with that name")

// SkillsRoot is a filesystem-rooted skills directory. Use DefaultRoot()
// to resolve the canonical user location, or construct a SkillsRoot with
// an explicit Path (e.g. t.TempDir() in tests).
type SkillsRoot struct {
	Path string
}

// DefaultRoot resolves ~/.pageskein/skills/, ensures it exists, and
// seeds any missing files from the embedded defaults tree. Idempotent;
// never overwrites user edits.
//
// Override via $PAGESKEIN_SKILLS for tests / split installs.
func DefaultRoot() (SkillsRoot, error) {
	path := strings.TrimSpace(os.Getenv("PAGESKEIN_SKILLS"))
	if path == "" {
		path = strings.TrimSpace(os.Getenv("NDEV_BROWSER_SKILLS"))
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return SkillsRoot{}, fmt.Errorf("resolve home: %w", err)
		}
		path = filepath.Join(home, ".pageskein", "skills")
	}
	r := SkillsRoot{Path: path}
	if err := r.EnsureSeeded(); err != nil {
		return SkillsRoot{}, err
	}
	return r, nil
}

// Domains returns the list of skill directory names under root, in
// stable alphabetical order. Hidden dirs (leading "_" or ".") are
// excluded — `_helpers/` is reserved for shared JS helpers.
func (r SkillsRoot) Domains() ([]Domain, error) {
	entries, err := os.ReadDir(r.Path)
	if err != nil {
		return nil, fmt.Errorf("read skills root %q: %w", r.Path, err)
	}
	out := make([]Domain, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, Domain(name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// AllDomains is a back-compat alias for Domains() that returns the
// well-known domains when the SkillsRoot can't be resolved (so callers
// like the help text still print a useful list). Prefer Domains().
func AllDomains() []Domain {
	r, err := DefaultRoot()
	if err == nil {
		ds, derr := r.Domains()
		if derr == nil && len(ds) > 0 {
			return ds
		}
	}
	return []Domain{DomainLinkedIn, DomainTikTok, DomainYouTube, DomainInstagram, DomainJobPosting}
}

// DomainOf returns the canonical Domain for a URL by matching the
// hostname's first DNS label (post www-strip) against the directory
// names under root. Returns "" if no skill dir matches.
//
// Aliases: youtu.be → youtube. Other host quirks (m.facebook.com etc.)
// are handled by suffix-strip, since the first label after stripping
// "m." is "facebook" which would match domain-skills/facebook/.
func (r SkillsRoot) DomainOf(rawURL string) Domain {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	if alias, ok := hostAliases[host]; ok {
		return alias
	}
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	domains, err := r.Domains()
	if err != nil {
		return ""
	}
	for _, d := range domains {
		s := string(d)
		if host == s+".com" || host == s+".net" || host == s+".org" || host == s+".io" || host == s+".co" {
			return d
		}
		if strings.HasPrefix(host, s+".") {
			return d
		}
		if strings.HasSuffix(host, "."+s+".com") || strings.HasSuffix(host, "."+s+".net") {
			return d
		}
	}
	return ""
}

// DomainOf is a back-compat package-level shortcut that resolves the
// default root. New code should call SkillsRoot.DomainOf directly with
// an explicit root.
func DomainOf(rawURL string) Domain {
	r, err := DefaultRoot()
	if err != nil {
		return ""
	}
	return r.DomainOf(rawURL)
}

// Hints returns the list of *.md filenames under <root>/<domain>/, in
// stable alphabetical order. Mirrors browser-harness's goto_url
// `domain_skills` return shape — these are agent-readable file names,
// not file contents.
func (r SkillsRoot) Hints(domain Domain) ([]string, error) {
	dir := filepath.Join(r.Path, string(domain))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read domain dir %q: %w", dir, err)
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Selectors loads <root>/<domain>/selectors.yaml. Returns a SelectorSet
// with the domain field populated. Returns ErrNoSkillForDomain if the
// domain dir is missing; returns os.ErrNotExist (wrapped) if the dir
// exists but has no selectors.yaml.
func (r SkillsRoot) Selectors(domain Domain) (SelectorSet, error) {
	dir := filepath.Join(r.Path, string(domain))
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return SelectorSet{}, ErrNoSkillForDomain
	}
	path := filepath.Join(dir, "selectors.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return SelectorSet{}, fmt.Errorf("read %q: %w", path, err)
	}
	var set SelectorSet
	if err := yaml.Unmarshal(data, &set); err != nil {
		return SelectorSet{}, fmt.Errorf("parse %q: %w", path, err)
	}
	set.Domain = domain
	return set, nil
}

// ExtractorSpec is the loaded JS source plus identifying metadata.
type ExtractorSpec struct {
	Domain Domain `json:"domain"`
	Name   string `json:"name"`
	JS     string `json:"-"`
	Path   string `json:"path"`
}

// Extractor loads <root>/<domain>/extract-<name>.js. The default name
// is "extract" (so a domain with one extractor can ship `extract.js`).
func (r SkillsRoot) Extractor(domain Domain, name string) (ExtractorSpec, error) {
	if name == "" {
		name = "extract"
	}
	dir := filepath.Join(r.Path, string(domain))
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ExtractorSpec{}, ErrNoSkillForDomain
	}
	candidates := []string{
		filepath.Join(dir, "extract-"+name+".js"),
		filepath.Join(dir, name+".js"),
	}
	if name == "extract" {
		candidates = []string{
			filepath.Join(dir, "extract.js"),
			filepath.Join(dir, "extract-extract.js"),
		}
	}
	for _, c := range candidates {
		data, err := os.ReadFile(c)
		if err == nil {
			return ExtractorSpec{Domain: domain, Name: name, JS: string(data), Path: c}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ExtractorSpec{}, fmt.Errorf("read %q: %w", c, err)
		}
	}
	return ExtractorSpec{}, fmt.Errorf("%w: domain=%s name=%s", ErrNoExtractor, domain, name)
}

// Extractors lists the extractor names under <root>/<domain>/. Names
// strip the "extract-" prefix and ".js" suffix. A bare "extract.js"
// surfaces as the name "extract".
func (r SkillsRoot) Extractors(domain Domain) ([]string, error) {
	dir := filepath.Join(r.Path, string(domain))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read domain dir %q: %w", dir, err)
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".js") {
			continue
		}
		base := strings.TrimSuffix(n, ".js")
		base = strings.TrimPrefix(base, "extract-")
		if base == "" {
			base = "extract"
		}
		out = append(out, base)
	}
	sort.Strings(out)
	return out, nil
}

// RunExtractor runs <domain>/extract-<name>.js against page and returns
// its raw JSON output. The shape is the script's contract; callers
// wanting a typed result should json.Unmarshal into their own struct.
//
// Helper auto-prepend: any `*.js` under <root>/_helpers/ is concatenated
// (alphabetical) into a wrapping IIFE so its top-level declarations
// (functions, const, let) are visible to the extractor body. This
// mirrors browser-harness's `agent_helpers.py` injection — agents can
// drop `_helpers/firstText.js` once and call `firstText(...)` from
// every domain's `extract-*.js`.
func (r SkillsRoot) RunExtractor(ctx context.Context, page Page, domain Domain, name string) (json.RawMessage, error) {
	spec, err := r.Extractor(domain, name)
	if err != nil {
		return nil, err
	}
	helpers, err := r.helperPrelude()
	if err != nil {
		return nil, fmt.Errorf("load _helpers for %s/%s: %w", domain, name, err)
	}
	js := strings.TrimSpace(spec.JS)
	if helpers != "" {
		// Wrap as: (function(){ <helpers>; return <extractor expression>; })()
		// The extractor file is by convention a single expression
		// (typically `(()=>{ … })()`), so `return <expr>` evaluates and
		// returns the same value.
		js = "(function(){\n" + helpers + "\nreturn " + js + ";\n})()"
	}
	var raw json.RawMessage
	if err := page.Eval(ctx, js, &raw); err != nil {
		return nil, fmt.Errorf("eval extractor %s/%s: %w", domain, name, err)
	}
	return raw, nil
}

// helperPrelude returns the concatenated bodies of every *.js file
// under <root>/_helpers/, in stable alphabetical order. Returns "" when
// the directory is missing or has no .js files. Read on every call —
// agents edit helpers at runtime and the next extractor run picks up
// the change without restart.
func (r SkillsRoot) helperPrelude() (string, error) {
	dir := filepath.Join(r.Path, "_helpers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".js") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		body, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", fmt.Errorf("read _helpers/%s: %w", n, err)
		}
		parts = append(parts, strings.TrimSpace(string(body)))
	}
	return strings.Join(parts, "\n;\n"), nil
}

// CheckSelectors runs the SelectorSet against page and returns a
// DriftReport. URL is captured from the caller (we don't trust the
// page's location for batch flows where Navigate may have failed).
func CheckSelectors(ctx context.Context, page Page, set SelectorSet, pageURL string) (DriftReport, error) {
	if len(set.Pairs) == 0 {
		return DriftReport{}, fmt.Errorf("no selectors for domain %q", set.Domain)
	}
	probeJS := fmt.Sprintf(`(()=>{
  const pairs = %s;
  return pairs.map(({field, selector}) => {
    try {
      const els = document.querySelectorAll(selector);
      const matched = els.length;
      let sample = '';
      if (matched > 0) {
        const first = els[0];
        sample = (first.textContent || first.getAttribute('content') || '').trim().slice(0, 120);
      }
      return { field, selector, matched, sample };
    } catch (err) {
      return { field, selector, matched: 0, error: err.message };
    }
  });
})()`, pairsToJSON(set.Pairs))

	var results []SelectorCheck
	if err := page.Eval(ctx, probeJS, &results); err != nil {
		return DriftReport{}, fmt.Errorf("eval selectors: %w", err)
	}

	host := ""
	if u, err := url.Parse(pageURL); err == nil {
		host = u.Host
	}
	dead := 0
	for _, r := range results {
		if r.Matched == 0 {
			dead++
		}
	}
	total := len(results)
	pct := 0
	if total > 0 {
		pct = (dead * 100) / total
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Field < results[j].Field })
	return DriftReport{
		Domain:         set.Domain,
		Host:           host,
		URL:            pageURL,
		TotalSelectors: total,
		DeadSelectors:  dead,
		DriftPct:       pct,
		Results:        results,
	}, nil
}

// CheckSelectorsForDomain is a convenience that loads selectors.yaml
// for the domain and runs CheckSelectors.
func (r SkillsRoot) CheckSelectorsForDomain(ctx context.Context, page Page, domain Domain, pageURL string) (DriftReport, error) {
	set, err := r.Selectors(domain)
	if err != nil {
		return DriftReport{}, err
	}
	return CheckSelectors(ctx, page, set, pageURL)
}

func pairsToJSON(pairs []SelectorPair) string {
	parts := []string{}
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf(`{field: %q, selector: %q}`, p.Field, p.Selector))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
