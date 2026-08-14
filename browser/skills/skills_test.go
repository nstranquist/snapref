package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePage is a Page that returns canned results from a map keyed by a
// substring of the JS body. Lets us test skill assembly logic without a
// real Chromium.
type fakePage struct {
	current string
	answers map[string]any
}

func (p *fakePage) Navigate(_ context.Context, url string) error { p.current = url; return nil }

func (p *fakePage) Eval(_ context.Context, js string, out any) error {
	for needle, ans := range p.answers {
		if strings.Contains(js, needle) {
			return assignJSON(ans, out)
		}
	}
	return errors.New("fakePage: no canned answer for js: " + js[:min(80, len(js))])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func assignJSON(val, dst any) error { return jsonRoundTrip(val, dst) }

// seedTestRoot returns a SkillsRoot rooted at t.TempDir() with the
// embedded defaults seeded — same starting state as a fresh user
// install. Tests that need a synthetic skill layout can mutate the
// returned root's files directly.
func seedTestRoot(t *testing.T) SkillsRoot {
	t.Helper()
	root, err := SeedDefaultsInto(t.TempDir())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return root
}

func TestSkillsRootDomainOf(t *testing.T) {
	root := seedTestRoot(t)
	cases := map[string]Domain{
		"https://www.linkedin.com/in/jane":      DomainLinkedIn,
		"https://linkedin.com/company/acme":     DomainLinkedIn,
		"https://www.tiktok.com/@user/video/1":  DomainTikTok,
		"https://youtu.be/abc123":               DomainYouTube,
		"https://www.youtube.com/watch?v=abc":   DomainYouTube,
		"https://www.instagram.com/p/abc":       DomainInstagram,
		"https://jobs.lever.co/acme/abc":        DomainJobPosting,
		"https://job-boards.greenhouse.io/acme": DomainJobPosting,
		"https://jobs.ashbyhq.com/Acme/abc":     DomainJobPosting,
		"https://example.com":                   Domain(""),
	}
	for in, want := range cases {
		if got := root.DomainOf(in); got != want {
			t.Errorf("DomainOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkillsRootDomainsListsSeededDirs(t *testing.T) {
	root := seedTestRoot(t)
	domains, err := root.Domains()
	if err != nil {
		t.Fatal(err)
	}
	// Defaults shipped: linkedin/tiktok/youtube/instagram (the originals)
	// + github (lifted 2026-05-04 from browser-use/browser-harness)
	// + job-posting (generic Greenhouse/Lever/Ashby extraction).
	want := map[Domain]bool{
		DomainLinkedIn:   true,
		DomainTikTok:     true,
		DomainYouTube:    true,
		DomainInstagram:  true,
		DomainJobPosting: true,
		Domain("github"): true,
	}
	for _, d := range domains {
		if !want[d] {
			t.Errorf("unexpected domain %q in defaults", d)
		}
		delete(want, d)
	}
	for d := range want {
		t.Errorf("missing domain %q after seed", d)
	}
}

func TestSkillsRootSelectorsLoaded(t *testing.T) {
	root := seedTestRoot(t)
	for _, d := range []Domain{DomainLinkedIn, DomainTikTok, DomainYouTube, DomainInstagram, DomainJobPosting, Domain("github")} {
		set, err := root.Selectors(d)
		if err != nil {
			t.Fatalf("Selectors(%q): %v", d, err)
		}
		if len(set.Pairs) == 0 {
			t.Errorf("domain %q has no seeded selectors", d)
		}
		if set.Domain != d {
			t.Errorf("set.Domain = %q, want %q", set.Domain, d)
		}
	}
}

func TestSkillsRootExtractor(t *testing.T) {
	root := seedTestRoot(t)
	cases := []struct {
		domain Domain
		name   string
	}{
		{DomainLinkedIn, "person"},
		{DomainLinkedIn, "company"},
		{DomainTikTok, "extract"},
		{DomainYouTube, "extract"},
		{DomainInstagram, "extract"},
		{DomainJobPosting, "extract"},
	}
	for _, c := range cases {
		spec, err := root.Extractor(c.domain, c.name)
		if err != nil {
			t.Errorf("Extractor(%q, %q): %v", c.domain, c.name, err)
			continue
		}
		if !strings.Contains(spec.JS, "(()=>") && !strings.Contains(spec.JS, "function") {
			t.Errorf("%s/%s body doesn't look like a JS expression: %s…", c.domain, c.name, spec.JS[:min(60, len(spec.JS))])
		}
		if filepath.Base(spec.Path) == "" {
			t.Errorf("%s/%s spec.Path empty", c.domain, c.name)
		}
	}
}

func TestSeededExtractorsStartWithExpression(t *testing.T) {
	root := seedTestRoot(t)
	domains, err := root.Domains()
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range domains {
		names, err := root.Extractors(domain)
		if err != nil {
			t.Fatalf("Extractors(%q): %v", domain, err)
		}
		for _, name := range names {
			spec, err := root.Extractor(domain, name)
			if err != nil {
				t.Fatalf("Extractor(%q, %q): %v", domain, name, err)
			}
			trimmed := strings.TrimSpace(spec.JS)
			if strings.HasPrefix(trimmed, "//") {
				t.Fatalf("%s/%s starts with a line comment; helper injection wraps extractors as `return <expr>`", domain, name)
			}
		}
	}
}

func TestSkillsRootExtractorMissingReturnsErr(t *testing.T) {
	root := seedTestRoot(t)
	if _, err := root.Extractor(DomainLinkedIn, "nonexistent"); !errors.Is(err, ErrNoExtractor) {
		t.Errorf("missing extractor err = %v, want ErrNoExtractor", err)
	}
	if _, err := root.Extractor(Domain("ghost"), "extract"); !errors.Is(err, ErrNoSkillForDomain) {
		t.Errorf("missing domain err = %v, want ErrNoSkillForDomain", err)
	}
}

func TestRunExtractorAgainstFakePage(t *testing.T) {
	root := seedTestRoot(t)
	page := &fakePage{
		answers: map[string]any{
			"data.handle": map[string]any{"handle": "user", "likes": 100, "comments": 5, "shares": nil},
		},
	}
	raw, err := root.RunExtractor(context.Background(), page, DomainTikTok, "extract")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"handle":"user"`) {
		t.Errorf("raw = %s, expected to contain handle:user", string(raw))
	}
}

func TestCheckSelectorsAggregates(t *testing.T) {
	page := &fakePage{
		answers: map[string]any{
			"const pairs": []map[string]any{
				{"field": "likes", "selector": `[data-e2e="like-count"]`, "matched": 1, "sample": "12.3K"},
				{"field": "shares", "selector": `[data-e2e="share-count"]`, "matched": 0, "sample": ""},
				{"field": "username", "selector": `[data-e2e="browse-username"]`, "matched": 1, "sample": "@user"},
			},
		},
	}
	set := SelectorSet{
		Domain: DomainTikTok,
		Pairs: []SelectorPair{
			{Field: "likes", Selector: `[data-e2e="like-count"]`},
			{Field: "shares", Selector: `[data-e2e="share-count"]`},
			{Field: "username", Selector: `[data-e2e="browse-username"]`},
		},
	}
	got, err := CheckSelectors(context.Background(), page, set, "https://www.tiktok.com/@user/video/1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalSelectors != 3 || got.DeadSelectors != 1 || got.DriftPct != 33 {
		t.Fatalf("aggregation wrong: %+v", got)
	}
	if got.Host != "www.tiktok.com" {
		t.Errorf("host = %q", got.Host)
	}
	wantFields := []string{"likes", "shares", "username"}
	for i, f := range wantFields {
		if got.Results[i].Field != f {
			t.Errorf("results[%d].Field = %q, want %q", i, got.Results[i].Field, f)
		}
	}
}

func TestExtractLinkedInPersonURLDispatch(t *testing.T) {
	root := seedTestRoot(t)
	page := &fakePage{
		answers: map[string]any{
			"firstText": map[string]any{
				"full_name":    "Jane Doe",
				"headline":     "Director of Engineering",
				"job_title":    "Director of Engineering",
				"company_name": "Acme Corp",
				"location":     "San Francisco, California",
				"about":        "Eng leader.",
			},
		},
	}
	got, err := ExtractLinkedIn(context.Background(), page, root, "https://www.linkedin.com/in/jane-doe/")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "person" {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Identifier != "jane-doe" {
		t.Errorf("identifier = %q", got.Identifier)
	}
	if got.Person == nil || got.Person.FullName != "Jane Doe" || got.Person.FirstName != "Jane" || got.Person.LastName != "Doe" {
		t.Errorf("person = %+v", got.Person)
	}
}

func TestExtractLinkedInCompanyURLDispatch(t *testing.T) {
	root := seedTestRoot(t)
	page := &fakePage{
		answers: map[string]any{
			"firstText": map[string]any{
				"company_name":   "Acme",
				"tagline":        "We make things",
				"description":    strings.Repeat("desc ", 30),
				"industry":       "Software",
				"company_size":   "200 employees",
				"location":       "SF",
				"founded":        "2010",
				"specialties":    []any{"saas", "platform"},
				"website":        "https://acme.com",
				"company_domain": "acme.com",
			},
		},
	}
	got, err := ExtractLinkedIn(context.Background(), page, root, "https://www.linkedin.com/company/acme/")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "company" || got.Identifier != "acme" {
		t.Fatalf("got = %+v", got)
	}
	if got.Company == nil || got.Company.Name != "Acme" || got.Company.Domain != "acme.com" {
		t.Errorf("company = %+v", got.Company)
	}
}

func TestExtractJobPostingURLDispatch(t *testing.T) {
	root := seedTestRoot(t)
	page := &fakePage{
		answers: map[string]any{
			"job-posting extractor": map[string]any{
				"provider":    "lever",
				"title":       "Senior Backend Engineer",
				"company":     "Acme",
				"location":    "Remote US",
				"url":         "https://jobs.lever.co/acme/123",
				"apply_url":   "https://jobs.lever.co/acme/123/apply",
				"description": "Requirements\nGo\nKubernetes",
			},
		},
	}
	got, err := ExtractJobPosting(context.Background(), page, root, "https://jobs.lever.co/acme/123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "lever" || got.Title != "Senior Backend Engineer" || got.Company != "Acme" {
		t.Fatalf("posting = %+v", got)
	}
	for _, want := range []string{"Title: Senior Backend Engineer", "Company: Acme", "Requirements", "Go"} {
		if !strings.Contains(got.JDText, want) {
			t.Fatalf("JDText missing %q:\n%s", want, got.JDText)
		}
	}
}

func TestExtractLinkedInUnknownPath(t *testing.T) {
	root := seedTestRoot(t)
	page := &fakePage{}
	got, err := ExtractLinkedIn(context.Background(), page, root, "https://www.linkedin.com/feed/")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "unknown" {
		t.Fatalf("type = %q", got.Type)
	}
}

func TestExtractSocialDispatch(t *testing.T) {
	root := seedTestRoot(t)
	page := &fakePage{
		answers: map[string]any{
			"data.handle":     map[string]any{"handle": "user", "likes": 100, "comments": 5, "shares": nil},
			"data.title":      map[string]any{"title": "vid", "views": 99, "channel": "abc"},
			"data.content_id": map[string]any{"content_id": "123", "title": "post", "handle": "user", "likes": 50, "comments": 1},
		},
	}
	for _, dom := range []Domain{DomainTikTok, DomainYouTube, DomainInstagram} {
		got, err := ExtractSocial(context.Background(), page, root, dom, "https://example/url")
		if err != nil {
			t.Errorf("dom=%s: %v", dom, err)
			continue
		}
		if got.Domain != dom || len(got.Fields) == 0 {
			t.Errorf("dom=%s: got %+v", dom, got)
		}
	}
	if _, err := ExtractSocial(context.Background(), page, root, DomainLinkedIn, ""); err == nil {
		t.Error("ExtractSocial(linkedin) should refuse")
	}
	if _, err := ExtractSocial(context.Background(), page, root, Domain(""), ""); !errors.Is(err, ErrNoSkillForDomain) {
		t.Errorf("empty domain err = %v, want ErrNoSkillForDomain", err)
	}
}

// recordingPage captures every JS body passed to Eval so tests can
// assert on the exact string sent to chromedp. Returns a canned answer
// that matches the substring needles in canned.
type recordingPage struct {
	current string
	canned  map[string]any
	seen    []string
}

func (p *recordingPage) Navigate(_ context.Context, url string) error { p.current = url; return nil }

func (p *recordingPage) Eval(_ context.Context, js string, out any) error {
	p.seen = append(p.seen, js)
	for needle, ans := range p.canned {
		if strings.Contains(js, needle) {
			return jsonRoundTrip(ans, out)
		}
	}
	return errors.New("recordingPage: no canned answer for js")
}

func TestRunExtractorPrependsHelpers(t *testing.T) {
	root := seedTestRoot(t)
	// Add a sentinel helper. Auto-prepend should make `function helperPing`
	// visible to the extractor body sent to Eval.
	mustWriteFile(t, filepath.Join(root.Path, "_helpers", "ping.js"),
		"function helperPing() { return 'pong'; }\n")
	// Add a tiny custom domain that calls the helper.
	mustWriteFile(t, filepath.Join(root.Path, "ghostsite", "extract.js"),
		"(()=>({ pinged: helperPing() }))()\n")
	mustWriteFile(t, filepath.Join(root.Path, "ghostsite", "selectors.yaml"),
		"selectors:\n  - field: x\n    selector: 'body'\n")

	page := &recordingPage{canned: map[string]any{
		"helperPing": map[string]any{"pinged": "pong"}, // needle in helper
		"((={pinged": map[string]any{"pinged": "pong"}, // belt-and-braces
	}}
	raw, err := root.RunExtractor(context.Background(), page, Domain("ghostsite"), "extract")
	if err != nil {
		t.Fatalf("RunExtractor: %v", err)
	}
	if !strings.Contains(string(raw), `"pinged":"pong"`) {
		t.Errorf("raw = %s, want pinged:pong", string(raw))
	}
	if len(page.seen) != 1 {
		t.Fatalf("expected 1 eval call, got %d", len(page.seen))
	}
	body := page.seen[0]
	if !strings.Contains(body, "function helperPing") {
		t.Errorf("eval body missing helper; got:\n%s", body)
	}
	if !strings.Contains(body, "helperPing()") {
		t.Errorf("eval body missing extractor's call site; got:\n%s", body)
	}
	if !strings.HasPrefix(body, "(function(){") {
		t.Errorf("eval body missing IIFE wrapper; got prefix %q", body[:min(40, len(body))])
	}
}

func TestRunExtractorWithoutHelpersUnwrapped(t *testing.T) {
	dir := t.TempDir()
	root := SkillsRoot{Path: dir}
	// No _helpers/ dir at all → extractor JS passes through unwrapped.
	mustWriteFile(t, filepath.Join(dir, "ghostsite", "extract.js"),
		"(()=>({ ok: true }))()\n")
	page := &recordingPage{canned: map[string]any{"ok: true": map[string]any{"ok": true}}}
	if _, err := root.RunExtractor(context.Background(), page, Domain("ghostsite"), "extract"); err != nil {
		t.Fatalf("RunExtractor: %v", err)
	}
	if len(page.seen) != 1 {
		t.Fatalf("expected 1 eval, got %d", len(page.seen))
	}
	body := page.seen[0]
	if strings.HasPrefix(body, "(function(){") {
		t.Errorf("no _helpers should mean no IIFE wrapper; got:\n%s", body)
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestSeededIncludesUnderscoredHelpersDir guards against the
// //go:embed `all:` regression: Go's default embed rules silently
// exclude files/dirs whose names begin with `_` or `.`, so without
// the `all:` prefix on the embed directive the entire `_helpers/`
// tree disappears from the binary and no helpers are seeded — the
// auto-prepend then has nothing to inject. This test fails fast if
// someone reverts to `//go:embed defaults`.
func TestSeededIncludesUnderscoredHelpersDir(t *testing.T) {
	root := seedTestRoot(t)
	helperPath := filepath.Join(root.Path, "_helpers", "firstText.js")
	body, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatalf("expected seeded _helpers/firstText.js, got %v — check that defaults_embed.go uses `//go:embed all:defaults`", err)
	}
	if !strings.Contains(string(body), "function firstText") {
		t.Errorf("seeded firstText.js missing helper body: %s", string(body))
	}
	prelude, err := root.helperPrelude()
	if err != nil {
		t.Fatalf("helperPrelude: %v", err)
	}
	if !strings.Contains(prelude, "function firstText") {
		t.Errorf("helperPrelude doesn't pick up firstText after seed: %q", prelude)
	}
}

func TestSkillsInitIdempotent(t *testing.T) {
	dir := t.TempDir()
	r1, err := SeedDefaultsInto(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate one default file, re-seed, verify the user edit survives.
	user := filepath.Join(r1.Path, "linkedin", "selectors.yaml")
	if err := writeFile(user, "selectors:\n  - field: only_user\n    selector: '#mine'\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedDefaultsInto(dir); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	set, err := r1.Selectors(DomainLinkedIn)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Pairs) != 1 || set.Pairs[0].Field != "only_user" {
		t.Errorf("user edit got clobbered: %+v", set.Pairs)
	}
}

func TestRefreshDefaultsOverwritesOwnedFilesAndPreservesCustomFiles(t *testing.T) {
	root := seedTestRoot(t)
	owned := filepath.Join(root.Path, "linkedin", "selectors.yaml")
	custom := filepath.Join(root.Path, "custom-domain", "extract.js")
	if err := writeFile(owned, "selectors:\n  - field: stale\n    selector: '#stale'\n"); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, custom, "(()=>({custom:true}))()\n")

	if err := root.RefreshDefaults(); err != nil {
		t.Fatalf("refresh defaults: %v", err)
	}
	set, err := root.Selectors(DomainLinkedIn)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Pairs) <= 1 || set.Pairs[0].Field == "stale" {
		t.Fatalf("owned default was not refreshed: %+v", set.Pairs)
	}
	gotCustom, err := os.ReadFile(custom)
	if err != nil {
		t.Fatalf("custom file was removed: %v", err)
	}
	if string(gotCustom) != "(()=>({custom:true}))()\n" {
		t.Fatalf("custom file changed: %q", gotCustom)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
