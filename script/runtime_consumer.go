package browserscript

import (
	"context"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"strings"
	"time"

	"github.com/nstranquist/pageskein/browser"
)

// RuntimeConsumerSmokeReceipt is the typed result of the real managed-runtime
// script smoke used by acceptance producers. Keeping the smoke in the
// production package lets an attesting binary execute the exact code linked
// into that binary instead of compiling an unbound child test executable from
// the current checkout.
type RuntimeConsumerSmokeReceipt struct {
	RuntimeKind       browser.RuntimeKind     `json:"runtime_kind"`
	RunID             string                  `json:"run_id"`
	Steps             int                     `json:"steps"`
	CapturedHeading   string                  `json:"captured_heading"`
	ViewportWidthPX   int                     `json:"viewport_width_px"`
	ViewportHeightPX  int                     `json:"viewport_height_px"`
	FullPageWidthPX   int                     `json:"full_page_width_px"`
	FullPageHeightPX  int                     `json:"full_page_height_px"`
	ManifestPersisted bool                    `json:"manifest_persisted"`
	LifecyclePolicy   browser.LifecyclePolicy `json:"lifecycle_policy"`
}

// RunRuntimeConsumerSmoke drives a real managed Headless Shell session through
// the public script runner and validates captures, assertions, screenshot
// geometry, telemetry, and manifest persistence.
func RunRuntimeConsumerSmoke(ctx context.Context, open func(browser.OpenOptions) (*browser.Session, error)) (RuntimeConsumerSmokeReceipt, error) {
	if open == nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("managed session opener is required")
	}
	resolved, err := browser.ResolveRuntime(browser.RuntimeRequest{Kind: browser.RuntimeHeadlessShell})
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("resolve managed Headless Shell: %w", err)
	}

	session := fmt.Sprintf("runtime-consumer-script-%d", os.Getpid())
	managedSession, err := open(browser.OpenOptions{Name: session, Runtime: browser.RuntimeHeadlessShell, ReadyTimeout: 30 * time.Second})
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("open managed session: %w", err)
	}
	defer browser.Close(session, true)
	if managedSession.EffectiveLifecyclePolicy() == browser.LifecycleUnmanaged {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("managed session opener returned no lifecycle policy")
	}

	plan := Plan{Name: "runtime-consumer-smoke", Session: session, Steps: []Step{
		{Verb: VerbViewport, Width: 800, Height: 600},
		{Verb: VerbNavigate, URL: "data:text/html,<style>body{margin:0}.tall{height:2400px}</style><h1 id=t>Hello PageSkein</h1><p class=s>Live</p><div class=tall></div>"},
		{Verb: VerbWait, Selector: "h1"},
		{Verb: VerbText, Selector: "#t", Capture: "heading"},
		{Verb: VerbAssertText, Selector: ".s", Assert: &Assertion{Equals: "Live"}},
		{Verb: VerbAssertURL, Assert: &Assertion{Contains: "data:"}},
		{Verb: VerbScreenshot, ScreenshotMode: ScreenshotModeViewport},
		{Verb: VerbScreenshot, ScreenshotMode: ScreenshotModeFullPage},
	}}

	var sink strings.Builder
	telemetry := NewFileTelemetry()
	report, err := NewRunner(browser.NewRemoteDriver()).Run(ctx, plan, RunOptions{
		Source: SourceSDK, Sink: &sink, Telemetry: telemetry, Now: time.Now, AllowDrive: true,
	})
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("run script smoke: %w", err)
	}
	if !report.OK || len(report.Steps) != len(plan.Steps) {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("script report ok=%t steps=%d, want ok with %d steps", report.OK, len(report.Steps), len(plan.Steps))
	}
	heading, _ := report.Steps[3].Captured["heading"].(string)
	if heading != "Hello PageSkein" {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("captured heading %q, want Hello PageSkein", heading)
	}
	viewport, err := runtimeConsumerScreenshotDimensions(report.Steps[6].Screenshot)
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("viewport screenshot: %w", err)
	}
	fullPage, err := runtimeConsumerScreenshotDimensions(report.Steps[7].Screenshot)
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("full-page screenshot: %w", err)
	}
	if got := report.Steps[6]; got.ScreenshotMode != ScreenshotModeViewport || got.ScreenshotWidthPX != viewport.X || got.ScreenshotHeightPX != viewport.Y {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("viewport receipt mode=%q %dx%d, artifact=%dx%d", got.ScreenshotMode, got.ScreenshotWidthPX, got.ScreenshotHeightPX, viewport.X, viewport.Y)
	}
	if got := report.Steps[7]; got.ScreenshotMode != ScreenshotModeFullPage || got.ScreenshotWidthPX != fullPage.X || got.ScreenshotHeightPX != fullPage.Y {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("full-page receipt mode=%q %dx%d, artifact=%dx%d", got.ScreenshotMode, got.ScreenshotWidthPX, got.ScreenshotHeightPX, fullPage.X, fullPage.Y)
	}
	if viewport.X != 800 || viewport.Y != 600 || fullPage.X != 800 || fullPage.Y <= viewport.Y {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("unexpected screenshot geometry viewport=%dx%d full-page=%dx%d", viewport.X, viewport.Y, fullPage.X, fullPage.Y)
	}
	if telemetry.LastErr != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("persist script telemetry: %w", telemetry.LastErr)
	}
	manifestPath, err := ManifestPath(report.RunID)
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("resolve manifest path: %w", err)
	}
	if _, err := os.Stat(manifestPath); err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("stat persisted manifest: %w", err)
	}

	return RuntimeConsumerSmokeReceipt{
		RuntimeKind: resolved.Kind, RunID: report.RunID, Steps: len(report.Steps), CapturedHeading: heading,
		ViewportWidthPX: viewport.X, ViewportHeightPX: viewport.Y, FullPageWidthPX: fullPage.X, FullPageHeightPX: fullPage.Y,
		ManifestPersisted: true, LifecyclePolicy: managedSession.EffectiveLifecyclePolicy(),
	}, nil
}

func runtimeConsumerScreenshotDimensions(path string) (image.Point, error) {
	file, err := os.Open(path)
	if err != nil {
		return image.Point{}, err
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(file)
	if err != nil {
		return image.Point{}, err
	}
	if format != "png" {
		return image.Point{}, fmt.Errorf("format=%q, want png", format)
	}
	return image.Pt(config.Width, config.Height), nil
}
