// Command snapref-runtime-cost measures what a headless browser session
// costs on this machine, for SnapRef's managed runtimes and for agent-browser:
// startup time, process count, resident memory and CPU while working and
// while idle, processes left after close, whether the session ever takes the
// desktop's foreground, and whether a screenshot reproduces a fixture's
// colours exactly. It also reports disk use and whether a newer version
// exists.
//
// It is a dated, machine-local receipt (like snapref-benchmark), not a
// release gate: other work on the host moves the numbers, so it records the
// load average next to every run and alternates providers between runs.
//
//	go run ./cmd/snapref-runtime-cost --runs 3 --out benchmark/runtime-cost.json
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/snapref/browser"
)

type proc struct {
	PID, PPID int
	RSSKB     int
	CPU       time.Duration
	Command   string
}

type sample struct {
	Procs  int     `json:"procs"`
	RSSMB  float64 `json:"rss_mb"`
	CPUPct float64 `json:"cpu_pct"` // of one core, over the window
}

type fidelity struct {
	Width, Height int    `json:"-"`
	Size          string `json:"size"`
	ExactColours  bool   `json:"exact_colours"`
	Detail        string `json:"detail,omitempty"`
}

type run struct {
	Provider       string   `json:"provider"`
	Run            int      `json:"run"`
	Load1          float64  `json:"load_1m"`
	StartupMS      int64    `json:"startup_ms"` // open + navigate + first snapshot
	ScreenshotMS   int64    `json:"screenshot_ms"`
	Active         sample   `json:"active"` // across three screenshots
	Idle           sample   `json:"idle"`   // 10 s with no commands
	CloseMS        int64    `json:"close_ms"`
	DrainMS        int64    `json:"drain_ms"`               // close returned → last tree process gone (capped at 30 s)
	LeftAfterClose int      `json:"left_after_close"`       // tree processes still alive 30 s after close
	LastToExit     string   `json:"last_to_exit,omitempty"` // the process that kept the drain open
	LeftCommands   []string `json:"left_commands,omitempty"`
	ForegroundApp  bool     `json:"registered_foreground_app"` // a tree pid listed by lsappinfo as Foreground
	FrontChanged   bool     `json:"front_app_changed"`
	Fidelity       fidelity `json:"fidelity"`
	Error          string   `json:"error,omitempty"`
}

type provider struct {
	Name    string
	Version string
	Disk    map[string]float64 // label -> MB
	Latest  string
	open    func(ctx context.Context, session, url string) error
	shot    func(ctx context.Context, session, path string) error
	close   func(ctx context.Context, session string) error
	marker  func(session string) []string // argv substrings that identify the session's root processes
}

type report struct {
	SchemaVersion int                 `json:"schema_version"`
	CapturedAt    time.Time           `json:"captured_at"`
	Machine       string              `json:"machine"` // class only: os/arch, CPUs, OS version (never the host name; this package is published)
	Method        string              `json:"method"`
	Providers     []providerSummary   `json:"providers"`
	Runs          []run               `json:"runs"`
	Resident      map[string][]string `json:"resident_after_all"` // provider -> commands still running at the end
}

type providerSummary struct {
	Name    string             `json:"name"`
	Version string             `json:"version"`
	Latest  string             `json:"latest,omitempty"`
	DiskMB  map[string]float64 `json:"disk_mb"`
	Median  map[string]float64 `json:"median"`
}

func main() {
	runs := flag.Int("runs", 3, "runs per provider (alternated)")
	out := flag.String("out", "", "write the JSON report here (default stdout)")
	shots := flag.String("shots", "", "also save each provider's fixture screenshot in this directory")
	snaprefBin := flag.String("snapref", "", "snapref binary (default: go build ./cmd/snapref into a temp dir)")
	only := flag.String("providers", "", "comma-separated provider names to run (default all)")
	flag.Parse()
	ctx := context.Background()

	bin := *snaprefBin
	if bin == "" {
		tmp, err := os.MkdirTemp("", "snapref-cost-")
		fatal(err)
		defer os.RemoveAll(tmp)
		bin = filepath.Join(tmp, "snapref")
		_, err = run1(ctx, "go", "build", "-o", bin, "./cmd/snapref")
		fatal(err)
	}
	srv := fixture()
	defer srv.Close()

	var providers []*provider
	for _, p := range []*provider{snapref(ctx, bin, "headless-shell"), snapref(ctx, bin, "chrome-for-testing"), agentBrowser(ctx)} {
		if *only == "" || strings.Contains(","+*only+",", ","+p.Name+",") {
			providers = append(providers, p)
		}
	}
	rep := report{SchemaVersion: 1, CapturedAt: time.Now().UTC(), Resident: map[string][]string{},
		Method: "Each run: open a fresh session on a local fixture, take a snapshot, three screenshots, 10 s idle, close. Process tree = processes started during the run whose argv names the session, plus their descendants; CPU is the tree's CPU-time delta over wall time (100 = one core). Providers alternate run by run."}
	rep.Machine = machine()
	if *shots != "" {
		fatal(os.MkdirAll(*shots, 0o755))
	}
	for i := 1; i <= *runs; i++ {
		for _, p := range providers {
			r := measure(ctx, p, i, srv.URL, *shots)
			fmt.Fprintf(os.Stderr, "%s run %d: startup %d ms, active %.0f MB / %.0f%%, idle %.0f MB / %.1f%%, left %d, fg %v, exact %v %s\n",
				p.Name, i, r.StartupMS, r.Active.RSSMB, r.Active.CPUPct, r.Idle.RSSMB, r.Idle.CPUPct, r.LeftAfterClose, r.ForegroundApp, r.Fidelity.ExactColours, r.Error)
			rep.Runs = append(rep.Runs, r)
		}
	}
	time.Sleep(5 * time.Second)
	all, _ := processes()
	for _, p := range providers {
		for _, pr := range all {
			if residentFor(p.Name, pr.Command) {
				rep.Resident[p.Name] = append(rep.Resident[p.Name], trim(pr.Command, 160))
			}
		}
	}
	for _, p := range providers {
		rep.Providers = append(rep.Providers, summarize(p, rep.Runs))
	}
	raw, _ := json.MarshalIndent(rep, "", "  ")
	if *out == "" {
		os.Stdout.Write(append(raw, '\n'))
		return
	}
	fatal(os.WriteFile(*out, append(raw, '\n'), 0o644))
}

// residentFor matches long-lived helpers a provider may leave by design
// (agent-browser's daemon) or by accident (a browser with its profile).
func residentFor(name, cmd string) bool {
	switch {
	case strings.HasPrefix(name, "agent-browser"):
		return strings.Contains(cmd, "agent-browser") || strings.Contains(cmd, "/.agent-browser/")
	default:
		return strings.Contains(cmd, "snapcost-snapref-")
	}
}

func measure(ctx context.Context, p *provider, i int, url, shotDir string) (r run) {
	r = run{Provider: p.Name, Run: i, Load1: load1()}
	session := fmt.Sprintf("snapcost-%s-%d-%d", strings.ReplaceAll(p.Name, "/", "-"), i, time.Now().Unix()%100000)
	before, err := processes()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	baseline := map[int]bool{}
	for _, b := range before {
		baseline[b.PID] = true
	}
	front0 := frontApp()

	t0 := time.Now()
	if err := p.open(ctx, session, url); err != nil {
		r.Error = "open: " + err.Error()
		_ = p.close(ctx, session)
		return r
	}
	r.StartupMS = time.Since(t0).Milliseconds()
	tree := func() []proc { return sessionTree(baseline, p.marker(session)) }

	// Active: three screenshots, CPU over the whole window.
	shotPath := filepath.Join(os.TempDir(), session+".png")
	a0, w0 := tree(), time.Now()
	t1 := time.Now()
	for k := 0; k < 3; k++ {
		if err := p.shot(ctx, session, shotPath); err != nil {
			r.Error = "screenshot: " + err.Error()
			break
		}
	}
	r.ScreenshotMS = time.Since(t1).Milliseconds() / 3
	a1 := tree()
	r.Active = window(a0, a1, time.Since(w0))
	r.Fidelity = checkFidelity(shotPath)
	if shotDir != "" && i == 1 {
		raw, _ := os.ReadFile(shotPath)
		_ = os.WriteFile(filepath.Join(shotDir, strings.ReplaceAll(p.Name, "/", "-")+".png"), raw, 0o644)
	}
	os.Remove(shotPath)

	// Idle: ten seconds with no commands.
	i0, iw := tree(), time.Now()
	time.Sleep(10 * time.Second)
	i1 := tree()
	r.Idle = window(i0, i1, time.Since(iw))
	r.ForegroundApp = registeredForeground(i1)
	r.FrontChanged = frontApp() != front0

	t2 := time.Now()
	if err := p.close(ctx, session); err != nil && r.Error == "" {
		r.Error = "close: " + err.Error()
	}
	r.CloseMS = time.Since(t2).Milliseconds()
	// Watch the tree drain; anything alive after 30 s is left behind.
	known := append(append([]proc{}, i1...), sessionTree(baseline, p.marker(session))...)
	t3 := time.Now()
	var left []proc
	for {
		left = stillAlive(known)
		if len(left) == 0 || time.Since(t3) > 30*time.Second {
			break
		}
		r.LastToExit = trim(left[0].Command, 160)
		time.Sleep(250 * time.Millisecond)
	}
	r.DrainMS = time.Since(t3).Milliseconds()
	for _, a := range left {
		r.LeftAfterClose++
		r.LeftCommands = append(r.LeftCommands, trim(a.Command, 160))
	}
	return r
}

func stillAlive(known []proc) []proc {
	all, _ := processes()
	live := map[int]proc{}
	for _, a := range all {
		live[a.PID] = a
	}
	seen := map[int]bool{}
	var out []proc
	for _, t := range known {
		if a, ok := live[t.PID]; ok && a.Command == t.Command && !seen[t.PID] {
			seen[t.PID] = true
			out = append(out, a)
		}
	}
	return out
}

// sessionTree is every process started since baseline whose argv names the
// session, plus all of their descendants.
func sessionTree(baseline map[int]bool, markers []string) []proc {
	all, err := processes()
	if err != nil {
		return nil
	}
	return treeOf(all, baseline, markers)
}

func treeOf(all []proc, baseline map[int]bool, markers []string) []proc {
	in := map[int]bool{}
	for _, p := range all {
		if baseline[p.PID] {
			continue
		}
		for _, m := range markers {
			if strings.Contains(p.Command, m) {
				in[p.PID] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range all {
			if !in[p.PID] && in[p.PPID] && !baseline[p.PID] {
				in[p.PID], changed = true, true
			}
		}
	}
	var out []proc
	for _, p := range all {
		if in[p.PID] {
			out = append(out, p)
		}
	}
	return out
}

func window(a, b []proc, wall time.Duration) sample {
	cpu0 := map[int]time.Duration{}
	for _, p := range a {
		cpu0[p.PID] = p.CPU
	}
	var s sample
	var used time.Duration
	for _, p := range b {
		s.Procs++
		s.RSSMB += float64(p.RSSKB) / 1024
		used += p.CPU - cpu0[p.PID] // a new process counts from zero
	}
	if wall > 0 {
		s.CPUPct = 100 * used.Seconds() / wall.Seconds()
	}
	s.RSSMB = round1(s.RSSMB)
	s.CPUPct = round1(s.CPUPct)
	return s
}

func processes() ([]proc, error) {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,ppid=,rss=,time=,command=").Output()
	if err != nil {
		return nil, err
	}
	var ps []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		rss, _ := strconv.Atoi(f[2])
		ps = append(ps, proc{PID: pid, PPID: ppid, RSSKB: rss, CPU: cputime(f[3]), Command: strings.Join(f[4:], " ")})
	}
	return ps, nil
}

// cputime parses ps TIME: [[dd-]hh:]mm:ss.cc
func cputime(s string) time.Duration {
	days := 0
	if i := strings.Index(s, "-"); i > 0 {
		days, _ = strconv.Atoi(s[:i])
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	var secs float64
	for _, p := range parts {
		v, _ := strconv.ParseFloat(p, 64)
		secs = secs*60 + v
	}
	return time.Duration((secs + float64(days)*86400) * float64(time.Second))
}

func machine() string {
	m := fmt.Sprintf("%s/%s, %d CPUs", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	if v, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
		m += ", macOS " + strings.TrimSpace(string(v))
	}
	return m
}

func load1() float64 {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{}"))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func frontApp() string {
	out, err := exec.Command("lsappinfo", "front").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var lsPID = regexp.MustCompile(`pid = (\d+)`)

// registeredForeground reports whether any tree process is a Foreground
// app to LaunchServices (a Dock icon, able to take focus).
func registeredForeground(tree []proc) bool {
	out, err := exec.Command("lsappinfo", "list").Output()
	if err != nil {
		return false
	}
	in := map[string]bool{}
	for _, p := range tree {
		in[strconv.Itoa(p.PID)] = true
	}
	for _, block := range strings.Split(string(out), "\n\n") {
		m := lsPID.FindStringSubmatch(block)
		if m != nil && in[m[1]] && strings.Contains(block, `type="Foreground"`) {
			return true
		}
	}
	return false
}

// The fixture fills the viewport with four exact colours.
var quadrants = []struct {
	fx, fy  float64
	r, g, b uint32
}{{0.25, 0.25, 255, 0, 0}, {0.75, 0.25, 0, 255, 0}, {0.25, 0.75, 0, 0, 255}, {0.75, 0.75, 255, 255, 255}}

func fixture() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>runtime cost</title><style>html,body{margin:0;height:100%}
.q{position:fixed;width:50vw;height:50vh}</style>
<div class=q style="left:0;top:0;background:#ff0000"></div><div class=q style="left:50vw;top:0;background:#00ff00"></div>
<div class=q style="left:0;top:50vh;background:#0000ff"></div><div class=q style="left:50vw;top:50vh;background:#ffffff"><button>Action</button></div>`)
	}))
}

func checkFidelity(path string) fidelity {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fidelity{Detail: "no screenshot"}
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return fidelity{Detail: "not a PNG: " + err.Error()}
	}
	b := img.Bounds()
	f := fidelity{Width: b.Dx(), Height: b.Dy(), Size: fmt.Sprintf("%dx%d", b.Dx(), b.Dy()), ExactColours: true}
	for _, q := range quadrants {
		x, y := b.Min.X+int(q.fx*float64(b.Dx())), b.Min.Y+int(q.fy*float64(b.Dy()))
		r, g, bl, _ := img.At(x, y).RGBA()
		if r>>8 != q.r || g>>8 != q.g || bl>>8 != q.b {
			f.ExactColours = false
			f.Detail += fmt.Sprintf("(%d,%d) got %d,%d,%d want %d,%d,%d; ", x, y, r>>8, g>>8, bl>>8, q.r, q.g, q.b)
		}
	}
	return f
}

func snapref(ctx context.Context, bin, runtime string) *provider {
	p := &provider{Name: "snapref/" + runtime, Disk: map[string]float64{}}
	if v, err := run1(ctx, bin, "version"); err == nil {
		p.Version = strings.TrimSpace(firstLine(v))
	}
	// SessionRoot honours SNAPREF_HOME (and the NDEV_BROWSER_HOME host alias),
	// so the measured runtime matches the binary under test.
	if root, err := browser.SessionRoot(); err == nil {
		p.Disk["runtime"] = duMB(filepath.Join(root, "runtimes", runtime))
	}
	if out, err := run1(ctx, bin, "runtime", "list"); err == nil {
		var l struct {
			Runtimes []struct {
				Kind     string `json:"runtime_kind"`
				Version  string `json:"version"`
				Selected bool   `json:"selected"`
			} `json:"runtimes"`
		}
		if json.Unmarshal([]byte(out), &l) == nil {
			for _, r := range l.Runtimes {
				if r.Kind == runtime && r.Selected {
					p.Version += " · " + r.Kind + " " + r.Version
				}
			}
		}
	}
	p.Latest = latestChromeForTesting(ctx)
	p.open = func(ctx context.Context, s, url string) error {
		if _, err := run1(ctx, bin, "open", s, "--runtime", runtime, "--idle-timeout", "2m"); err != nil {
			return err
		}
		if _, err := run1(ctx, bin, "exec", s, "navigate", url); err != nil {
			return err
		}
		_, err := run1(ctx, bin, "exec", s, "snapshot")
		return err
	}
	p.shot = func(ctx context.Context, s, path string) error {
		_, err := run1(ctx, bin, "exec", s, "screenshot", path)
		return err
	}
	p.close = func(ctx context.Context, s string) error {
		_, err := run1(ctx, bin, "close", s, "--purge-profile")
		return err
	}
	p.marker = func(s string) []string { return []string{s} }
	return p
}

func agentBrowser(ctx context.Context) *provider {
	p := &provider{Name: "agent-browser", Disk: map[string]float64{}}
	ab := func(ctx context.Context, args ...string) (string, error) {
		return run1(ctx, "npx", append([]string{"--yes", "agent-browser"}, args...)...)
	}
	if v, err := ab(ctx, "--version"); err == nil {
		p.Version = strings.TrimSpace(lastLine(v))
	}
	if v, err := run1(ctx, "npm", "view", "agent-browser", "version"); err == nil {
		p.Latest = strings.TrimSpace(v)
	}
	home, _ := os.UserHomeDir()
	p.Disk["browsers"] = duMB(filepath.Join(home, ".agent-browser"))
	if out, err := run1(ctx, "npm", "config", "get", "cache"); err == nil {
		p.Disk["npx_cache"] = duNpx(filepath.Join(strings.TrimSpace(out), "_npx"))
	}
	p.open = func(ctx context.Context, s, url string) error {
		if _, err := ab(ctx, "--session", s, "open", url); err != nil {
			return err
		}
		_, err := ab(ctx, "--session", s, "snapshot")
		return err
	}
	p.shot = func(ctx context.Context, s, path string) error {
		_, err := ab(ctx, "--session", s, "screenshot", path)
		return err
	}
	p.close = func(ctx context.Context, s string) error {
		_, err := ab(ctx, "--session", s, "close")
		return err
	}
	// Neither the daemon nor Chrome names the session in argv; Chrome's
	// crash handlers are re-parented to launchd. Match the install paths.
	p.marker = func(string) []string {
		return []string{"/agent-browser/bin/agent-browser", "/.agent-browser/browsers/"}
	}
	return p
}

func summarize(p *provider, runs []run) providerSummary {
	s := providerSummary{Name: p.Name, Version: p.Version, Latest: p.Latest, DiskMB: p.Disk, Median: map[string]float64{}}
	pick := map[string]func(r run) float64{
		"startup_ms": func(r run) float64 { return float64(r.StartupMS) }, "screenshot_ms": func(r run) float64 { return float64(r.ScreenshotMS) },
		"close_ms":      func(r run) float64 { return float64(r.CloseMS) },
		"drain_ms":      func(r run) float64 { return float64(r.DrainMS) },
		"active_rss_mb": func(r run) float64 { return r.Active.RSSMB }, "active_cpu_pct": func(r run) float64 { return r.Active.CPUPct },
		"idle_rss_mb": func(r run) float64 { return r.Idle.RSSMB }, "idle_cpu_pct": func(r run) float64 { return r.Idle.CPUPct },
		"idle_procs": func(r run) float64 { return float64(r.Idle.Procs) }, "left_after_close": func(r run) float64 { return float64(r.LeftAfterClose) },
		"load_1m": func(r run) float64 { return r.Load1 },
	}
	for k, f := range pick {
		var v []float64
		for _, r := range runs {
			if r.Provider == p.Name && r.Error == "" {
				v = append(v, f(r))
			}
		}
		if len(v) == 0 {
			continue
		}
		sort.Float64s(v)
		s.Median[k] = round1(v[len(v)/2])
	}
	return s
}

func latestChromeForTesting(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions.json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		Channels map[string]struct {
			Version string `json:"version"`
		} `json:"channels"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return ""
	}
	return v.Channels["Stable"].Version
}

func duMB(path string) float64 {
	out, err := exec.Command("du", "-sk", path).Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(string(out))
	kb, _ := strconv.ParseFloat(f[0], 64)
	return round1(kb / 1024)
}

// duNpx sums the npx cache entries that hold agent-browser.
func duNpx(dir string) float64 {
	entries, _ := os.ReadDir(dir)
	var total float64
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "node_modules", "agent-browser")); err == nil {
			total += duMB(filepath.Join(dir, e.Name()))
		}
	}
	return round1(total)
}

func run1(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(args[:min(3, len(args))], " "), err, trim(strings.TrimSpace(errb.String()), 300))
	}
	return out.String(), nil
}

func firstLine(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }
func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return l[len(l)-1]
}
func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
