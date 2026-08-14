package skills

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/nstranquist/pageskein/browser"
)

// RuntimeConsumerSmokeReceipt is the typed result of the persistent-session
// skill smoke executed in-process by acceptance producers.
type RuntimeConsumerSmokeReceipt struct {
	RuntimeKind             browser.RuntimeKind     `json:"runtime_kind"`
	DomainsChecked          int                     `json:"domains_checked"`
	TikTokSelectorsChecked  int                     `json:"tiktok_selectors_checked"`
	TikTokDeadSelectors     int                     `json:"tiktok_dead_selectors"`
	YouTubeMatchedSelectors int                     `json:"youtube_matched_selectors"`
	YouTubeViews            int                     `json:"youtube_views"`
	LifecyclePolicy         browser.LifecyclePolicy `json:"lifecycle_policy"`
}

// RunRuntimeConsumerSmoke drives two domain skills through one real managed
// Headless Shell session and checks both selector drift and extracted values.
func RunRuntimeConsumerSmoke(ctx context.Context, open func(browser.OpenOptions) (*browser.Session, error)) (RuntimeConsumerSmokeReceipt, error) {
	if open == nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("managed session opener is required")
	}
	resolved, err := browser.ResolveRuntime(browser.RuntimeRequest{Kind: browser.RuntimeHeadlessShell})
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("resolve managed Headless Shell: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/tiktok", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><div data-e2e="like-count">12.3K</div><a data-e2e="browse-username">@user</a></body></html>`))
	})
	mux.HandleFunc("/youtube", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta itemprop="interactionCount" content="9876"><meta property="og:title" content="vid"></head><body></body></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	sessionName := fmt.Sprintf("runtime-consumer-skills-%d", os.Getpid())
	session, err := open(browser.OpenOptions{Name: sessionName, Runtime: browser.RuntimeHeadlessShell, ReadyTimeout: 30 * time.Second})
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("open managed session: %w", err)
	}
	defer browser.Close(sessionName, true)
	if session.EffectiveLifecyclePolicy() == browser.LifecycleUnmanaged {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("managed session opener returned no lifecycle policy")
	}

	rootPath, err := os.MkdirTemp("", "pageskein-runtime-consumer-skills-")
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("create temporary skills root: %w", err)
	}
	defer os.RemoveAll(rootPath)
	root, err := SeedDefaultsInto(rootPath)
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("seed default skills: %w", err)
	}
	page := NewSessionPage(session)
	page.Timeout = 30 * time.Second

	tikTokURL := server.URL + "/tiktok"
	if err := page.Navigate(ctx, tikTokURL); err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("navigate TikTok fixture: %w", err)
	}
	tikTokReport, err := root.CheckSelectorsForDomain(ctx, page, DomainTikTok, tikTokURL)
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("check TikTok selectors: %w", err)
	}
	if tikTokReport.TotalSelectors != 7 || tikTokReport.DeadSelectors != 5 {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("TikTok selector receipt total=%d dead=%d, want 7/5", tikTokReport.TotalSelectors, tikTokReport.DeadSelectors)
	}

	youTubeURL := server.URL + "/youtube"
	if err := page.Navigate(ctx, youTubeURL); err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("navigate YouTube fixture: %w", err)
	}
	youTubeReport, err := root.CheckSelectorsForDomain(ctx, page, DomainYouTube, youTubeURL)
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("check YouTube selectors: %w", err)
	}
	matched := 0
	for _, result := range youTubeReport.Results {
		if result.Matched > 0 {
			matched++
		}
	}
	if matched < 2 {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("YouTube matched selectors=%d, want at least 2", matched)
	}
	youTubeFields, err := ExtractSocial(ctx, page, root, DomainYouTube, youTubeURL)
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("extract YouTube fields: %w", err)
	}
	views := int(runtimeFloat64(youTubeFields.Fields["views"]))
	if views != 9876 {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("YouTube views=%d, want 9876", views)
	}

	return RuntimeConsumerSmokeReceipt{
		RuntimeKind: resolved.Kind, DomainsChecked: 2,
		TikTokSelectorsChecked: tikTokReport.TotalSelectors, TikTokDeadSelectors: tikTokReport.DeadSelectors,
		YouTubeMatchedSelectors: matched, YouTubeViews: views, LifecyclePolicy: session.EffectiveLifecyclePolicy(),
	}, nil
}

func runtimeFloat64(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case int:
		return float64(number)
	case int64:
		return float64(number)
	default:
		return 0
	}
}
