package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"
)

const runtimeAcceptancePolicyVersion = 4

type RuntimeBenchmarkOptions struct {
	Samples          int
	Record           bool
	ConsumerEvidence *RuntimeConsumerEvidence
}

type RuntimeBenchmarkResult struct {
	SchemaVersion int               `json:"schema_version"`
	Recorded      bool              `json:"recorded"`
	Acceptance    RuntimeAcceptance `json:"acceptance"`
	Errors        []string          `json:"errors"`
}

type runtimeBenchmarkSample struct {
	Duration time.Duration
	RSSBytes int64
}

// BenchmarkRuntimes runs the complete local acceptance gate against the exact
// selected managed pair. It never downloads and is non-mutating unless Record
// is true; a completed recorded failure replaces an older pass atomically.
func BenchmarkRuntimes(ctx context.Context, opts RuntimeBenchmarkOptions) (RuntimeBenchmarkResult, error) {
	if opts.Samples == 0 {
		opts.Samples = 30
	}
	if opts.Samples < 30 || opts.Samples > 200 {
		return RuntimeBenchmarkResult{}, errors.New("runtime benchmark samples must be 30..200")
	}
	platform, err := runtimePlatform()
	if err != nil {
		return RuntimeBenchmarkResult{}, err
	}
	selections, err := loadRuntimeSelections()
	if err != nil {
		return RuntimeBenchmarkResult{}, err
	}
	shellSelection, shellOK := selections.Selected[RuntimeHeadlessShell]
	cftSelection, cftOK := selections.Selected[RuntimeChromeForTesting]
	if err := validateRuntimeBenchmarkSelections(shellSelection, shellOK, cftSelection, cftOK, platform); err != nil {
		return RuntimeBenchmarkResult{}, err
	}
	if _, _, err := verifyInstalledRuntime(RuntimeHeadlessShell, platform, shellSelection.Version); err != nil {
		return RuntimeBenchmarkResult{}, fmt.Errorf("verify headless-shell: %w", err)
	}
	if _, _, err := verifyInstalledRuntime(RuntimeChromeForTesting, platform, cftSelection.Version); err != nil {
		return RuntimeBenchmarkResult{}, fmt.Errorf("verify chrome-for-testing: %w", err)
	}

	server := runtimeBenchmarkServer()
	defer server.Close()
	result := RuntimeBenchmarkResult{SchemaVersion: 1, Errors: []string{}}
	acceptance := RuntimeAcceptance{
		SchemaVersion: runtimeSchemaVersion, PolicyVersion: runtimeAcceptancePolicyVersion,
		Platform: platform, HeadlessShellVersion: shellSelection.Version,
		ChromeForTestingVersion: cftSelection.Version, TestedAt: time.Now().UTC(), Samples: opts.Samples,
		FocusIsolationStatus:                 runtimeFocusNotApplicable(),
		ChromeForTestingFocusIsolationStatus: runtimeFocusNotApplicable(),
		ConsumerEvidence:                     normalizedRuntimeConsumerEvidence(opts.ConsumerEvidence),
	}
	consumerErr := validateRuntimeConsumerEvidence(
		acceptance.ConsumerEvidence,
		platform,
		shellSelection.Version,
		cftSelection.Version,
		runtimeAcceptanceBuildVersion(),
	)
	acceptance.ConsumerPass = consumerErr == nil
	if consumerErr != nil {
		result.Errors = append(result.Errors, "consumer evidence: "+consumerErr.Error())
	}

	shellPNG, shellFunctionalErr := runtimeFunctionalSmoke(ctx, RuntimeHeadlessShell, server.URL)
	cftPNG, cftFunctionalErr := runtimeFunctionalSmoke(ctx, RuntimeChromeForTesting, server.URL)
	acceptance.FunctionalPass = shellFunctionalErr == nil && cftFunctionalErr == nil
	if shellFunctionalErr != nil {
		result.Errors = append(result.Errors, "headless-shell functional: "+shellFunctionalErr.Error())
	}
	if cftFunctionalErr != nil {
		result.Errors = append(result.Errors, "chrome-for-testing functional: "+cftFunctionalErr.Error())
	}
	if acceptance.FunctionalPass {
		visualDifference, visualErr := runtimeImageDifference(shellPNG, cftPNG)
		acceptance.VisualPass = visualErr == nil && visualDifference <= 0.03
		if visualErr != nil {
			result.Errors = append(result.Errors, "visual comparison: "+visualErr.Error())
		} else if !acceptance.VisualPass {
			result.Errors = append(result.Errors, fmt.Sprintf("visual comparison difference %.4f exceeds 0.0300", visualDifference))
		}
	}

	_, focusErr := runtimeFocusState()
	if focusErr != nil && runtimeFocusRequired() {
		result.Errors = append(result.Errors, "focus preflight: "+focusErr.Error())
		acceptance.FocusIsolationStatus = "fail"
		acceptance.ChromeForTestingFocusIsolationStatus = "fail"
		acceptance.ChromeForTestingFocusIsolationReason = focusErr.Error()
	}
	run, err := runRuntimeBenchmarkSamples(ctx, server.URL, opts.Samples)
	if err != nil {
		result.Errors = append(result.Errors, "interleaved runtime benchmark: "+err.Error())
	}
	if !run.HeadlessShellFocusPass && run.HeadlessShellFocusReason != "" {
		result.Errors = append(result.Errors, "headless-shell runtime focus: "+run.HeadlessShellFocusReason)
	}
	if runtimeFocusRequired() {
		if focusErr == nil && run.HeadlessShellFocusPass {
			acceptance.FocusIsolationStatus = "pass"
		} else {
			acceptance.FocusIsolationStatus = "fail"
		}
		if focusErr == nil && run.ChromeForTestingFocusPass {
			acceptance.ChromeForTestingFocusIsolationStatus = "pass"
		} else {
			acceptance.ChromeForTestingFocusIsolationStatus = "fail"
			acceptance.ChromeForTestingFocusIsolationReason = run.ChromeForTestingFocusReason
		}
	}
	if len(run.HeadlessShell) == opts.Samples && len(run.ChromeForTesting) == opts.Samples {
		acceptance.Metrics = RuntimeAcceptanceMetrics{
			HeadlessShellColdP50MS:         durationMedianMS(run.HeadlessShell),
			ChromeForTestingColdP50MS:      durationMedianMS(run.ChromeForTesting),
			HeadlessShellMedianRSSBytes:    rssMedian(run.HeadlessShell),
			ChromeForTestingMedianRSSBytes: rssMedian(run.ChromeForTesting),
		}
	}
	m := acceptance.Metrics
	performancePass := m.HeadlessShellColdP50MS > 0 && m.ChromeForTestingColdP50MS > 0 &&
		m.HeadlessShellColdP50MS <= m.ChromeForTestingColdP50MS*0.90 &&
		m.HeadlessShellMedianRSSBytes > 0 && m.ChromeForTestingMedianRSSBytes > 0 &&
		float64(m.HeadlessShellMedianRSSBytes) <= float64(m.ChromeForTestingMedianRSSBytes)*0.80
	if !performancePass {
		result.Errors = append(result.Errors, "headless-shell did not meet the 10% cold-start and 20% RSS improvement gate")
	}
	focusPass := acceptance.FocusIsolationStatus == "pass" || acceptance.FocusIsolationStatus == "not-applicable"
	acceptance.Pass = acceptance.FunctionalPass && acceptance.VisualPass && acceptance.ConsumerPass && focusPass && performancePass
	result.Acceptance = acceptance
	if opts.Record {
		path, err := runtimeAcceptancePath()
		if err != nil {
			return result, err
		}
		if err := writeJSONAtomic(path, acceptance, 0o600); err != nil {
			return result, err
		}
		result.Recorded = true
	}
	return result, nil
}

func validateRuntimeBenchmarkSelections(shell RuntimeSelection, shellOK bool, cft RuntimeSelection, cftOK bool, platform string) error {
	if !shellOK || !cftOK || shell.Platform != platform || cft.Platform != platform {
		return errors.New("runtime benchmark requires selected headless-shell and chrome-for-testing installations for this platform")
	}
	if shell.Version != cft.Version {
		return fmt.Errorf("runtime benchmark requires a version-matched pair; headless-shell=%s chrome-for-testing=%s", shell.Version, cft.Version)
	}
	return nil
}

func runtimeBenchmarkServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><style>
html,body{margin:0;width:800px;height:600px;background:#f4f1ea;color:#17202a;font:16px system-ui}
main{box-sizing:border-box;width:800px;height:600px;padding:48px}button{padding:12px 18px;background:#164e63;color:white;border:0;border-radius:6px}
</style></head><body><main><h1 id="title">SnapRef runtime acceptance</h1><p>Deterministic local fixture.</p><button id="action">Ready</button></main></body></html>`)
	}))
}

func runtimeFunctionalSmoke(ctx context.Context, kind RuntimeKind, targetURL string) ([]byte, error) {
	name := fmt.Sprintf("runtime-smoke-%s-%d", kind, time.Now().UnixNano()%1000000000)
	session, err := Open(OpenOptions{Name: name, Runtime: kind, ReadyTimeout: 30 * time.Second, Viewport: Viewport{Width: 800, Height: 600}})
	if err != nil {
		return nil, err
	}
	defer Close(name, true)
	if err := RemoteNavigate(ctx, session, targetURL, 20*time.Second); err != nil {
		return nil, err
	}
	if err := RemoteSetViewport(ctx, session, Viewport{Width: 800, Height: 600}, 10*time.Second); err != nil {
		return nil, err
	}
	var title string
	if err := RemoteEval(ctx, session, "", `document.querySelector('#title').textContent`, &title, 10*time.Second); err != nil {
		return nil, err
	}
	if title != "SnapRef runtime acceptance" {
		return nil, fmt.Errorf("unexpected fixture title %q", title)
	}
	return RemoteScreenshotCurrentWithTimeout(session, false, 20*time.Second)
}

type runtimeBenchmarkRun struct {
	HeadlessShell               []runtimeBenchmarkSample
	ChromeForTesting            []runtimeBenchmarkSample
	HeadlessShellFocusPass      bool
	HeadlessShellFocusReason    string
	ChromeForTestingFocusPass   bool
	ChromeForTestingFocusReason string
}

func runRuntimeBenchmarkSamples(ctx context.Context, targetURL string, count int) (runtimeBenchmarkRun, error) {
	run := runtimeBenchmarkRun{
		HeadlessShell:             make([]runtimeBenchmarkSample, 0, count),
		ChromeForTesting:          make([]runtimeBenchmarkSample, 0, count),
		HeadlessShellFocusPass:    true,
		ChromeForTestingFocusPass: true,
	}
	for i := 0; i < count; i++ {
		order := runtimeBenchmarkOrder(i)
		for _, kind := range order {
			if err := ctx.Err(); err != nil {
				return run, err
			}
			sample, focusPass, focusReason, err := runRuntimeBenchmarkSample(ctx, kind, targetURL, i)
			if err != nil {
				return run, fmt.Errorf("%s sample %d: %w", kind, i+1, err)
			}
			recordRuntimeBenchmarkFocus(&run, kind, focusPass, focusReason)
			if kind == RuntimeHeadlessShell {
				run.HeadlessShell = append(run.HeadlessShell, sample)
			} else {
				run.ChromeForTesting = append(run.ChromeForTesting, sample)
			}
		}
	}
	return run, nil
}

func recordRuntimeBenchmarkFocus(run *runtimeBenchmarkRun, kind RuntimeKind, pass bool, reason string) {
	if pass {
		return
	}
	if kind == RuntimeHeadlessShell {
		run.HeadlessShellFocusPass = false
		if run.HeadlessShellFocusReason == "" {
			run.HeadlessShellFocusReason = reason
		}
		return
	}
	run.ChromeForTestingFocusPass = false
	if run.ChromeForTestingFocusReason == "" {
		run.ChromeForTestingFocusReason = reason
	}
}

func runtimeBenchmarkOrder(index int) [2]RuntimeKind {
	if index%2 == 1 {
		return [2]RuntimeKind{RuntimeChromeForTesting, RuntimeHeadlessShell}
	}
	return [2]RuntimeKind{RuntimeHeadlessShell, RuntimeChromeForTesting}
}

func runRuntimeBenchmarkSample(ctx context.Context, kind RuntimeKind, targetURL string, index int) (sample runtimeBenchmarkSample, focusPass bool, focusReason string, err error) {
	focusPass = true
	monitor, monitorErr := newRuntimeFocusMonitor()
	if monitorErr != nil {
		return sample, false, "", monitorErr
	}
	name := fmt.Sprintf("runtime-bench-%s-%d-%d", kind, time.Now().UnixNano()%1000000, index)
	var session *Session
	defer func() {
		if monitor != nil {
			focusPass, focusReason = monitor.Stop()
		}
		if session != nil {
			if closeErr := Close(name, true); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	opts := OpenOptions{Name: name, Runtime: kind, ReadyTimeout: 30 * time.Second, Viewport: Viewport{Width: 800, Height: 600}}
	if monitor != nil {
		opts.onProcessStart = monitor.SetPID
	}
	started := time.Now()
	session, err = Open(opts)
	if err != nil {
		return sample, focusPass, focusReason, err
	}
	sample.Duration = time.Since(started)
	if err = RemoteNavigate(ctx, session, targetURL, 20*time.Second); err != nil {
		return sample, focusPass, focusReason, err
	}
	var title string
	if err = RemoteEval(ctx, session, "", `document.querySelector('#title')?.textContent || ''`, &title, 10*time.Second); err != nil {
		return sample, focusPass, focusReason, err
	}
	if title != "SnapRef runtime acceptance" {
		return sample, focusPass, focusReason, fmt.Errorf("unexpected steady-state fixture title %q", title)
	}
	sample.RSSBytes, err = waitRuntimeSteadyRSS(ctx, session.PID, 6*time.Second)
	return sample, focusPass, focusReason, err
}

func waitRuntimeSteadyRSS(ctx context.Context, pid int, timeout time.Duration) (int64, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	values := make([]int64, 0, 5)
	for {
		rss, err := runtimeProcessTreeRSS(pid)
		if err != nil {
			return 0, err
		}
		values = append(values, rss)
		if len(values) > 5 {
			values = values[len(values)-5:]
		}
		if len(values) == 5 && runtimeRSSWindowStable(values) {
			return medianInt64(values), nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-deadline.C:
			return 0, fmt.Errorf("process-tree RSS for pid %d did not stabilize within %s (last=%v)", pid, timeout, values)
		case <-ticker.C:
		}
	}
}

func runtimeRSSWindowStable(values []int64) bool {
	if len(values) < 5 {
		return false
	}
	median := medianInt64(values)
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}
	tolerance := median / 20
	if tolerance < 16<<20 {
		tolerance = 16 << 20
	}
	return maximum-minimum <= tolerance
}

func medianInt64(values []int64) int64 {
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	if len(ordered)%2 == 1 {
		return ordered[len(ordered)/2]
	}
	return (ordered[len(ordered)/2-1] + ordered[len(ordered)/2]) / 2
}

type runtimeFocusMonitor struct {
	baselineApplications map[int]string
	mu                   sync.Mutex
	pid                  int
	reason               string
	stop                 chan struct{}
	done                 chan struct{}
	stopOnce             sync.Once
}

func newRuntimeFocusMonitor() (*runtimeFocusMonitor, error) {
	if !runtimeFocusRequired() {
		return nil, nil
	}
	baselineApplications, err := runtimeApplicationProcesses()
	if err != nil {
		return nil, err
	}
	monitor := &runtimeFocusMonitor{
		baselineApplications: baselineApplications,
		stop:                 make(chan struct{}),
		done:                 make(chan struct{}),
	}
	go monitor.loop()
	return monitor, nil
}

func (monitor *runtimeFocusMonitor) SetPID(pid int) {
	monitor.mu.Lock()
	monitor.pid = pid
	monitor.mu.Unlock()
}

func (monitor *runtimeFocusMonitor) Stop() (bool, string) {
	monitor.stopOnce.Do(func() { close(monitor.stop) })
	<-monitor.done
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return monitor.reason == "", monitor.reason
}

func (monitor *runtimeFocusMonitor) loop() {
	defer close(monitor.done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		monitor.observe()
		select {
		case <-monitor.stop:
			monitor.observe()
			return
		case <-ticker.C:
		}
	}
}

func (monitor *runtimeFocusMonitor) observe() {
	monitor.mu.Lock()
	pid := monitor.pid
	failed := monitor.reason != ""
	monitor.mu.Unlock()
	if pid <= 0 || failed || !processAlive(pid) {
		return
	}
	current, err := runtimeFocusState()
	if err != nil {
		monitor.fail(err.Error())
		return
	}
	pids, err := runtimeProcessTreePIDs(pid)
	if err != nil {
		monitor.fail(err.Error())
		return
	}
	for _, candidate := range pids {
		if current.FrontmostPID == candidate {
			monitor.fail(fmt.Sprintf("browser process-tree pid %d became frontmost as %q", candidate, current.Frontmost))
			return
		}
	}
	applications, err := runtimeApplicationProcesses()
	if err != nil {
		monitor.fail(err.Error())
		return
	}
	if newPID, identity, found := runtimeNewBrowserApplication(monitor.baselineApplications, applications, pids); found {
		monitor.fail(fmt.Sprintf("new browser application %q pid %d appeared outside the managed process tree", identity, newPID))
		return
	}
	windows, err := current.VisibleBrowserWindows(pids)
	if err != nil {
		monitor.fail(err.Error())
		return
	}
	if windows != 0 {
		monitor.fail(fmt.Sprintf("browser process tree rooted at pid %d exposed %d visible window(s)", pid, windows))
	}
}

func (monitor *runtimeFocusMonitor) fail(reason string) {
	monitor.mu.Lock()
	if monitor.reason == "" {
		monitor.reason = reason
	}
	monitor.mu.Unlock()
}

func runtimeBrowserApplicationIdentity(identity string) bool {
	value := strings.ToLower(strings.TrimSpace(identity))
	// Chromium registers its alerts helper as an LSUIElement application and
	// launchd may reparent it outside the browser process tree. It is bundled
	// inside the selected Chrome application, cannot represent a new browser
	// owner, and is evaluated separately from the main Chrome app identity.
	if strings.HasSuffix(value, ".framework.alertnotificationservice") {
		return false
	}
	return strings.Contains(value, "chrome") || strings.Contains(value, "chromium")
}

func runtimeNewBrowserApplication(baseline, current map[int]string, managedPIDs []int) (int, string, bool) {
	managed := make(map[int]struct{}, len(managedPIDs))
	for _, pid := range managedPIDs {
		managed[pid] = struct{}{}
	}
	pids := make([]int, 0, len(current))
	for pid := range current {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		identity := current[pid]
		if _, existed := baseline[pid]; existed || !runtimeBrowserApplicationIdentity(identity) {
			continue
		}
		if _, owned := managed[pid]; owned {
			continue
		}
		return pid, identity, true
	}
	return 0, "", false
}

func durationMedianMS(samples []runtimeBenchmarkSample) float64 {
	values := make([]float64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, float64(sample.Duration.Microseconds())/1000)
	}
	sort.Float64s(values)
	return medianFloat(values)
}

func rssMedian(samples []runtimeBenchmarkSample) int64 {
	values := make([]int64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample.RSSBytes)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	if len(values)%2 == 1 {
		return values[len(values)/2]
	}
	return (values[len(values)/2-1] + values[len(values)/2]) / 2
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(values)%2 == 1 {
		return values[len(values)/2]
	}
	return (values[len(values)/2-1] + values[len(values)/2]) / 2
}

func runtimeImageDifference(aBody, bBody []byte) (float64, error) {
	a, err := png.Decode(bytes.NewReader(aBody))
	if err != nil {
		return 0, err
	}
	b, err := png.Decode(bytes.NewReader(bBody))
	if err != nil {
		return 0, err
	}
	if a.Bounds() != b.Bounds() {
		return 1, fmt.Errorf("screenshot bounds differ: %v vs %v", a.Bounds(), b.Bounds())
	}
	return normalizedImageDifference(a, b), nil
}

func normalizedImageDifference(a, b image.Image) float64 {
	bounds := a.Bounds()
	var total float64
	var channels int64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			total += math.Abs(float64(ar)-float64(br)) + math.Abs(float64(ag)-float64(bg)) + math.Abs(float64(ab)-float64(bb))
			channels += 3
		}
	}
	if channels == 0 {
		return 0
	}
	return total / float64(channels) / 65535
}
