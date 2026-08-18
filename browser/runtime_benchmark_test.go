package browser

import (
	"context"
	"image"
	"image/color"
	"os"
	"testing"
	"time"
)

func TestRuntimeBenchmarkStatistics(t *testing.T) {
	samples := []runtimeBenchmarkSample{
		{Duration: 30 * time.Millisecond, RSSBytes: 300},
		{Duration: 10 * time.Millisecond, RSSBytes: 100},
		{Duration: 20 * time.Millisecond, RSSBytes: 200},
		{Duration: 40 * time.Millisecond, RSSBytes: 400},
	}
	if got := durationMedianMS(samples); got != 25 {
		t.Fatalf("duration median = %v", got)
	}
	if got := rssMedian(samples); got != 250 {
		t.Fatalf("RSS median = %d", got)
	}
}

func TestRuntimeImageDifference(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 2, 2))
	b := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			a.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
			b.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	if got := normalizedImageDifference(a, b); got != 0 {
		t.Fatalf("identical difference = %v", got)
	}
	b.Set(0, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	if got := normalizedImageDifference(a, b); got <= 0 || got >= 1 {
		t.Fatalf("changed difference = %v", got)
	}
}

func TestRuntimeBenchmarkRejectsInsufficientSamplesBeforeStateRead(t *testing.T) {
	if _, err := BenchmarkRuntimes(context.Background(), RuntimeBenchmarkOptions{Samples: 29}); err == nil {
		t.Fatal("benchmark accepted fewer than 30 samples")
	}
}

func TestRuntimeBenchmarkRequiresVersionMatchedPair(t *testing.T) {
	platform := "darwin-arm64"
	shell := RuntimeSelection{Platform: platform, Version: "151.0.0.0"}
	cft := RuntimeSelection{Platform: platform, Version: "150.0.0.0"}
	if err := validateRuntimeBenchmarkSelections(shell, true, cft, true, platform); err == nil {
		t.Fatal("mismatched runtime versions accepted")
	}
	cft.Version = shell.Version
	if err := validateRuntimeBenchmarkSelections(shell, true, cft, true, platform); err != nil {
		t.Fatalf("matched runtime pair rejected: %v", err)
	}
}

func TestRuntimeBenchmarkOrderAlternates(t *testing.T) {
	if got := runtimeBenchmarkOrder(0); got != [2]RuntimeKind{RuntimeHeadlessShell, RuntimeChromeForTesting} {
		t.Fatalf("even order = %v", got)
	}
	if got := runtimeBenchmarkOrder(1); got != [2]RuntimeKind{RuntimeChromeForTesting, RuntimeHeadlessShell} {
		t.Fatalf("odd order = %v", got)
	}
}

func TestRuntimeRSSWindowRequiresConvergence(t *testing.T) {
	stable := []int64{200 << 20, 202 << 20, 198 << 20, 201 << 20, 199 << 20}
	if !runtimeRSSWindowStable(stable) {
		t.Fatal("stable RSS window rejected")
	}
	unstable := []int64{200 << 20, 260 << 20, 190 << 20, 280 << 20, 210 << 20}
	if runtimeRSSWindowStable(unstable) {
		t.Fatal("unstable RSS window accepted")
	}
	if got := medianInt64(stable); got != 200<<20 {
		t.Fatalf("stable median = %d", got)
	}
}

func TestRuntimeBrowserApplicationIdentity(t *testing.T) {
	for _, identity := range []string{"com.google.Chrome", "com.google.Chrome.forTesting", "org.chromium.Chromium"} {
		if !runtimeBrowserApplicationIdentity(identity) {
			t.Fatalf("browser identity %q not recognized", identity)
		}
	}
	if runtimeBrowserApplicationIdentity("com.apple.Terminal") {
		t.Fatal("non-browser application classified as browser")
	}
	for _, identity := range []string{
		"com.google.chrome.for.testing.framework.AlertNotificationService",
		"com.google.Chrome.framework.AlertNotificationService",
	} {
		if runtimeBrowserApplicationIdentity(identity) {
			t.Fatalf("bundled LSUIElement alerts helper %q classified as a browser owner", identity)
		}
	}
}

func TestRuntimeNewBrowserApplicationIgnoresBaselineAndManagedProcesses(t *testing.T) {
	baseline := map[int]string{742: "com.google.Chrome"}
	current := map[int]string{
		742:  "com.google.Chrome",
		9001: "com.google.Chrome.forTesting",
		9002: "com.apple.Terminal",
	}
	if _, _, found := runtimeNewBrowserApplication(baseline, current, []int{9001}); found {
		t.Fatal("baseline or managed browser application classified as a new external browser")
	}
	current[9003] = "org.chromium.Chromium"
	pid, identity, found := runtimeNewBrowserApplication(baseline, current, []int{9001})
	if !found || pid != 9003 || identity != "org.chromium.Chromium" {
		t.Fatalf("new external browser = (%d, %q, %t)", pid, identity, found)
	}
}

func TestRuntimeBenchmarkFocusSeparatesTargetFromComparator(t *testing.T) {
	run := runtimeBenchmarkRun{HeadlessShellFocusPass: true, ChromeForTestingFocusPass: true}
	recordRuntimeBenchmarkFocus(&run, RuntimeChromeForTesting, false, "cft activated")
	if !run.HeadlessShellFocusPass || run.HeadlessShellFocusReason != "" {
		t.Fatalf("CFT comparator finding contaminated Headless Shell gate: %+v", run)
	}
	if run.ChromeForTestingFocusPass || run.ChromeForTestingFocusReason != "cft activated" {
		t.Fatalf("CFT comparator finding not retained: %+v", run)
	}
	recordRuntimeBenchmarkFocus(&run, RuntimeHeadlessShell, false, "shell activated")
	if run.HeadlessShellFocusPass || run.HeadlessShellFocusReason != "shell activated" {
		t.Fatalf("Headless Shell target finding not retained: %+v", run)
	}
}

func TestRuntimeBenchmarkManagedSampleIntegration(t *testing.T) {
	if os.Getenv("SNAPREF_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set SNAPREF_RUNTIME_INTEGRATION=1 to exercise installed managed runtimes")
	}
	server := runtimeBenchmarkServer()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, kind := range []RuntimeKind{RuntimeHeadlessShell, RuntimeChromeForTesting} {
		t.Run(string(kind), func(t *testing.T) {
			sample, focusPass, focusReason, err := runRuntimeBenchmarkSample(ctx, kind, server.URL, 0)
			if err != nil {
				t.Fatal(err)
			}
			if sample.Duration <= 0 || sample.RSSBytes <= 0 {
				t.Fatalf("sample = %+v", sample)
			}
			if !focusPass {
				t.Fatalf("focus isolation failed: %s", focusReason)
			}
		})
	}
}
