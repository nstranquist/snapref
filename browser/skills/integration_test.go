package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/snapref/browser"
)

// TestCheckSelectorsAgainstFixtureHTML proves the selector probe JS
// works against a real Chromium. Hosts a synthetic TikTok-shaped HTML
// fixture via httptest, runs the drift check through the stateless
// browser harness, and asserts the right selectors light up.
//
// Skipped when:
//   - testing -short
//   - no Chromium binary is available (CI without it)
func TestCheckSelectorsAgainstFixtureHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := browser.ResolveRuntime(browser.RuntimeRequest{Kind: browser.RuntimeHeadlessShell}); err != nil {
		t.Skipf("no managed Headless Shell: %v", err)
	}

	const fixture = `<!doctype html>
<html><head><title>Test TikTok-shaped</title>
<script type="application/ld+json">{"@type":"VideoObject","interactionStatistic":[]}</script>
</head>
<body>
<div data-e2e="like-count">12.3K</div>
<div data-e2e="comment-count">42</div>
<a data-e2e="browse-username">@example</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := seedTestRoot(t)
	page := NewStatelessPage(browser.LaunchOptions{Runtime: browser.RuntimeHeadlessShell, Headless: true, Timeout: 25 * time.Second})
	if err := page.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	report, err := root.CheckSelectorsForDomain(ctx, page, DomainTikTok, srv.URL)
	if err != nil {
		t.Fatalf("CheckSelectorsForDomain: %v", err)
	}

	// Three of the seven registered TikTok selectors should match in our
	// synthetic fixture: likes, comments, username. The other four
	// (likes_alt, comments_alt, shares, ld_json) should be dead.
	wantMatched := map[string]bool{
		"likes":    true,
		"comments": true,
		"username": true,
		"ld_json":  true, // we included a script[type="application/ld+json"]
	}
	wantDead := map[string]bool{
		"likes_alt":    true,
		"comments_alt": true,
		"shares":       true,
	}

	for _, r := range report.Results {
		if wantMatched[r.Field] && r.Matched == 0 {
			t.Errorf("%s should have matched, got 0", r.Field)
		}
		if wantDead[r.Field] && r.Matched != 0 {
			t.Errorf("%s should be dead, got matched=%d", r.Field, r.Matched)
		}
	}
	if report.TotalSelectors != 7 {
		t.Errorf("total = %d, want 7", report.TotalSelectors)
	}
	if report.DeadSelectors != 3 {
		t.Errorf("dead = %d, want 3 (likes_alt + comments_alt + shares)", report.DeadSelectors)
	}
}

// TestExtractSocialAgainstFixtureHTML proves the value extractor JS for
// TikTok runs against a real Chromium and returns the expected fields.
func TestExtractSocialAgainstFixtureHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := browser.ResolveRuntime(browser.RuntimeRequest{Kind: browser.RuntimeHeadlessShell}); err != nil {
		t.Skipf("no managed Headless Shell: %v", err)
	}

	const fixture = `<!doctype html>
<html><head><title>vid</title></head>
<body>
<div data-e2e="like-count">12345</div>
<div data-e2e="comment-count">67</div>
<div data-e2e="share-count">8</div>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := seedTestRoot(t)
	page := NewStatelessPage(browser.LaunchOptions{Runtime: browser.RuntimeHeadlessShell, Headless: true, Timeout: 25 * time.Second})
	if err := page.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	got, err := ExtractSocial(ctx, page, root, DomainTikTok, srv.URL)
	if err != nil {
		t.Fatalf("ExtractSocial: %v", err)
	}
	if got.Fields["likes"] == nil || int(toFloat(got.Fields["likes"])) != 12345 {
		t.Errorf("likes = %v, want 12345", got.Fields["likes"])
	}
	if got.Fields["comments"] == nil || int(toFloat(got.Fields["comments"])) != 67 {
		t.Errorf("comments = %v, want 67", got.Fields["comments"])
	}
	if got.Fields["shares"] == nil || int(toFloat(got.Fields["shares"])) != 8 {
		t.Errorf("shares = %v, want 8", got.Fields["shares"])
	}
}

// TestEveryDefaultExtractorAgainstFixtureHTML executes every embedded default
// extractor in real Chromium. Unit tests cover assembly and typed adapters;
// this catches JavaScript syntax/runtime errors and missing shared helpers in
// the exact assets that skills init installs.
func TestEveryDefaultExtractorAgainstFixtureHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := browser.ResolveRuntime(browser.RuntimeRequest{Kind: browser.RuntimeHeadlessShell}); err != nil {
		t.Skipf("no managed Headless Shell: %v", err)
	}

	const fixture = `<!doctype html>
<html><head>
<title>Fixture</title>
<meta property="al:ios:url" content="instagram://media?id=12345">
<meta property="og:title" content="Instagram Fixture">
<meta property="og:description" content="1,234 likes, 56 comments - @alice: fixture">
<meta name="title" content="Fixture Video">
<meta itemprop="interactionCount" content="9876">
</head><body>
<article class="Box-row"><h2><a href="/owner/repo">owner / repo</a></h2><p>Repository description</p><a href="/owner/repo/stargazers">42</a><a href="/owner/repo/forks">7</a><span itemprop="programmingLanguage">Go</span><span class="d-inline-block float-sm-right">12 stars today</span></article>
<div class="posting-headline"><h2>Senior Engineer</h2></div><div class="company-name">Acme Jobs</div><div class="location">Remote</div><main class="posting">Build reliable systems.</main><a href="/apply">Apply now</a>
<h1 class="text-heading-xlarge">Jane Doe</h1><div class="text-body-medium break-words">Builder</div><div class="text-body-small inline t-black--light break-words">Chicago</div>
<div class="pvs-list__paged-list-item"><div class="t-bold"><span aria-hidden="true">Staff Engineer</span></div><div class="t-14 t-normal"><span aria-hidden="true">Acme · Full-time</span></div></div>
<section><div id="about"></div><div class="inline-show-more-text">About Jane</div></section>
<h1 class="org-top-card-summary__title">Acme Corp</h1><div class="org-top-card-summary__tagline">Reliable software</div><p class="org-about-us-organization-description__text">Acme builds reliable software for teams around the world every day.</p>
<dl class="org-page-details__definition-list"><dt>Industry</dt><dd>Software</dd><dt>Website</dt><dd><a href="https://acme.example">acme.example</a></dd></dl>
<div data-e2e="like-count">12.3K</div><div data-e2e="comment-count">42</div><div data-e2e="share-count">8</div>
<ytd-channel-name><a href="/@fixture">Fixture Channel</a></ytd-channel-name>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := seedTestRoot(t)
	page := NewStatelessPage(browser.LaunchOptions{Runtime: browser.RuntimeHeadlessShell, Headless: true, Timeout: 25 * time.Second})
	if err := page.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	tests := []struct {
		domain Domain
		name   string
		wants  []string
	}{
		{Domain("github"), "extract", []string{`"name":"owner / repo"`, `"language":"Go"`}},
		{DomainInstagram, "extract", []string{`"content_id":"12345"`, `"handle":"alice"`}},
		{DomainJobPosting, "extract", []string{`"title":"Senior Engineer"`, `"company":"Acme Jobs"`}},
		{DomainLinkedIn, "person", []string{`"full_name":"Jane Doe"`, `"job_title":"Staff Engineer"`}},
		{DomainLinkedIn, "company", []string{`"company_name":"Acme Corp"`, `"industry":"Software"`}},
		{DomainTikTok, "extract", []string{`"likes":12300`, `"comments":42`}},
		{DomainYouTube, "extract", []string{`"title":"Fixture Video"`, `"channel":"fixture"`}},
	}
	for _, test := range tests {
		t.Run(string(test.domain)+"/"+test.name, func(t *testing.T) {
			raw, err := root.RunExtractor(ctx, page, test.domain, test.name)
			if err != nil {
				t.Fatalf("RunExtractor: %v", err)
			}
			for _, want := range test.wants {
				if !strings.Contains(string(raw), want) {
					t.Errorf("result %s missing %s", raw, want)
				}
			}
		})
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
