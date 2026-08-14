package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func setBrowserHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PAGESKEIN_HOME", home)
	return home
}

func TestValidateSessionName(t *testing.T) {
	good := []string{"foo", "foo-bar", "foo_bar", "abc123", "a"}
	bad := []string{"", "Foo", "_bar", "../etc", "a/b", "with space", "1" + stringRepeat("x", 64)}
	for _, n := range good {
		if err := ValidateSessionName(n); err != nil {
			t.Errorf("good name %q rejected: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateSessionName(n); err == nil {
			t.Errorf("bad name %q accepted", n)
		}
	}
}

func TestChromeLaunchArgsHeadfulOffscreenIsExplicit(t *testing.T) {
	o := OpenOptions{
		Headful:          true,
		HeadfulOffscreen: true,
		Viewport:         Viewport{Width: 900, Height: 700},
	}
	args := chromeLaunchArgs(o, RuntimeChromeForTesting, "/tmp/pageskein-profile")
	if !slices.Contains(args, "--window-position=-32000,-32000") {
		t.Fatalf("offscreen headful launch missing window position: %v", args)
	}

	o.HeadfulOffscreen = false
	args = chromeLaunchArgs(o, RuntimeChromeForTesting, "/tmp/pageskein-profile")
	if slices.Contains(args, "--window-position=-32000,-32000") {
		t.Fatalf("ordinary headful launch unexpectedly moved offscreen: %v", args)
	}
}

func stringRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestSessionRoundTrip(t *testing.T) {
	home := setBrowserHome(t)

	s := &Session{
		Name:       "smoke",
		PID:        99999,
		Port:       49555,
		ProfileDir: filepath.Join(home, "profiles", "smoke"),
		ChromePath: "/fake/chrome",
		StartedAt:  time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		Headful:    true,
		WSURL:      "ws://127.0.0.1:49555/devtools/browser/abc",
	}
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, err := LoadSession("smoke")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got.PID != s.PID || got.Port != s.Port || got.WSURL != s.WSURL {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, s)
	}

	// File mode must be 0600.
	p, _ := SessionPath("smoke")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("session file mode = %o, want 0600", st.Mode().Perm())
	}
}

func TestSessionIdleLeaseRoundTripAndTouch(t *testing.T) {
	setBrowserHome(t)
	started := time.Now().UTC().Add(-time.Hour)
	if err := SaveSession(&Session{
		Name: "leased", PID: os.Getpid(), Port: 1, ChromePath: "/fake", StartedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	leased, err := SetSessionIdleTimeout("leased", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if leased.IdleTimeout != "30m0s" || leased.LastActivityAt.IsZero() || leased.LifecyclePolicy != LifecycleIdle {
		t.Fatalf("lease = %+v", leased)
	}
	firstActivity := leased.LastActivityAt
	time.Sleep(time.Millisecond)
	if err := TouchSession("leased"); err != nil {
		t.Fatal(err)
	}
	touched, err := LoadSession("leased")
	if err != nil {
		t.Fatal(err)
	}
	if !touched.LastActivityAt.After(firstActivity) {
		t.Fatalf("activity did not advance: first=%s touched=%s", firstActivity, touched.LastActivityAt)
	}
}

func TestSessionKeepAlivePolicyIsExplicit(t *testing.T) {
	setBrowserHome(t)
	if err := SaveSession(&Session{Name: "persistent", PID: os.Getpid(), Port: 1}); err != nil {
		t.Fatal(err)
	}
	persistent, err := SetSessionKeepAlive("persistent")
	if err != nil {
		t.Fatal(err)
	}
	if persistent.LifecyclePolicy != LifecycleKeepAlive || persistent.IdleTimeout != "" || !persistent.LastActivityAt.IsZero() {
		t.Fatalf("keep-alive policy = %+v", persistent)
	}
}

func TestTouchSessionLeavesUnleasedSessionUnchanged(t *testing.T) {
	setBrowserHome(t)
	s := &Session{Name: "persistent", PID: os.Getpid(), Port: 1, ChromePath: "/fake"}
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}
	if err := TouchSession("persistent"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSession("persistent")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.IsZero() || got.IdleTimeout != "" {
		t.Fatalf("unleased session changed: %+v", got)
	}
}

func TestListSessionsDeterministicOrder(t *testing.T) {
	setBrowserHome(t)
	for _, name := range []string{"banana", "apple", "cherry"} {
		s := &Session{Name: name, PID: 1, Port: 1, ChromePath: "/fake"}
		if err := SaveSession(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"apple", "banana", "cherry"}
	if len(got) != len(want) {
		t.Fatalf("got %d sessions, want %d", len(got), len(want))
	}
	for i, n := range want {
		if got[i].Name != n {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, n)
		}
	}
}

func TestListSessionsFailsClosedOnCorruptRegistryEntry(t *testing.T) {
	setBrowserHome(t)
	path, err := SessionPath("corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListSessions(); err == nil || !strings.Contains(err.Error(), "corrupt.json") {
		t.Fatalf("ListSessions corrupt entry error = %v", err)
	}
}

func TestLoadSessionRejectsMismatchedNameAndNonCanonicalProfile(t *testing.T) {
	home := setBrowserHome(t)
	writeRaw := func(name string, session Session) {
		t.Helper()
		path, err := SessionPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeRaw("expected", Session{Name: "different", PID: 1})
	if _, err := LoadSession("expected"); err == nil || !strings.Contains(err.Error(), "does not match filename") {
		t.Fatalf("mismatched embedded name error = %v", err)
	}

	outside := filepath.Join(t.TempDir(), "must-survive")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRaw("unsafe", Session{Name: "unsafe", PID: 999999, ProfileDir: outside})
	if _, err := LoadSession("unsafe"); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("noncanonical profile error = %v", err)
	}
	if err := Close("unsafe", true); err == nil {
		t.Fatal("close accepted a tampered profile record")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("tampered cleanup touched outside sentinel: %v (home %s)", err, home)
	}
}

func TestClosePurgeDoesNotFollowCanonicalProfileSymlink(t *testing.T) {
	home := setBrowserHome(t)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(home, "profiles", "linked")
	if err := os.MkdirAll(filepath.Dir(profile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, profile); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveSession(&Session{Name: "linked", PID: 999999, ProfileDir: profile}); err != nil {
		t.Fatal(err)
	}
	if err := Close("linked", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("purge followed profile symlink: %v", err)
	}
}

func TestLoadSessionMissingReturnsNotExist(t *testing.T) {
	setBrowserHome(t)
	_, err := LoadSession("nope")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestRemoveSession(t *testing.T) {
	setBrowserHome(t)
	s := &Session{Name: "tmp", PID: 1, Port: 1, ChromePath: "/fake"}
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSession("tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSession("tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session should be gone, err = %v", err)
	}
}

func TestCloseMissingSessionIsIdempotent(t *testing.T) {
	home := setBrowserHome(t)
	if err := Close("missing", false); err != nil {
		t.Fatalf("close missing without purge: %v", err)
	}
	profile := filepath.Join(home, "profiles", "missing")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Close("missing", true); err != nil {
		t.Fatalf("close missing with purge: %v", err)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile should be removed, err=%v", err)
	}
}

func TestGCDryRunAndApplyReapDeadEntriesOnly(t *testing.T) {
	home := setBrowserHome(t)
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	old := now.Add(-72 * time.Hour)
	deadProfile := filepath.Join(home, "profiles", "dead")
	liveProfile := filepath.Join(home, "profiles", "live")
	orphanProfile := filepath.Join(home, "profiles", "orphan")
	for _, path := range []string{deadProfile, liveProfile, orphanProfile} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	orphanPayload := []byte("reclaim-me")
	if err := os.WriteFile(filepath.Join(orphanProfile, "payload"), orphanPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(orphanProfile, old, old); err != nil {
		t.Fatal(err)
	}
	if err := SaveSession(&Session{Name: "dead", PID: 999999, Port: 1, ProfileDir: deadProfile, ChromePath: "/fake", StartedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSession(&Session{Name: "live", PID: os.Getpid(), Port: 1, ProfileDir: liveProfile, ChromePath: "/fake", StartedAt: old}); err != nil {
		t.Fatal(err)
	}

	report, err := GC(GCOptions{OlderThan: 24 * time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.RemovedSessions != 0 || report.RemovedProfiles != 0 {
		t.Fatalf("dry-run mutated: %+v", report)
	}
	if report.StaleKeepAlive == nil || report.DeadSessions == nil || report.DeadProfiles == nil {
		t.Fatalf("gc collection fields must encode as arrays, not null: %+v", report)
	}
	if len(report.DeadSessions) != 1 || report.DeadSessions[0].Name != "dead" {
		t.Fatalf("dead sessions = %+v", report.DeadSessions)
	}
	if names := gcEntryNames(report.DeadProfiles); !slices.Equal(names, []string{"dead", "orphan"}) {
		t.Fatalf("dead profiles = %#v", names)
	}
	if report.ReclaimableBytes < int64(len(orphanPayload)) {
		t.Fatalf("reclaimable bytes = %d, want at least %d", report.ReclaimableBytes, len(orphanPayload))
	}
	if _, err := LoadSession("dead"); err != nil {
		t.Fatalf("dry-run removed dead session: %v", err)
	}

	applied, err := GC(GCOptions{OlderThan: 24 * time.Hour, Apply: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if applied.DryRun || applied.RemovedSessions != 1 || applied.RemovedProfiles != 2 {
		t.Fatalf("apply report = %+v", applied)
	}
	if applied.RemovedBytes < int64(len(orphanPayload)) {
		t.Fatalf("removed bytes = %d, want at least %d", applied.RemovedBytes, len(orphanPayload))
	}
	if _, err := LoadSession("dead"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead session still present: %v", err)
	}
	if _, err := os.Stat(deadProfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead profile still present: %v", err)
	}
	if _, err := os.Stat(orphanProfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan profile still present: %v", err)
	}
	if _, err := os.Stat(liveProfile); err != nil {
		t.Fatalf("live profile should remain: %v", err)
	}
}

func TestGCProtectsProfileForYoungDeadSession(t *testing.T) {
	home := setBrowserHome(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	profile := filepath.Join(home, "profiles", "young-dead")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-72 * time.Hour)
	if err := os.Chtimes(profile, old, old); err != nil {
		t.Fatal(err)
	}
	if err := SaveSession(&Session{
		Name: "young-dead", PID: 999999, ProfileDir: profile,
		StartedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	report, err := GC(GCOptions{OlderThan: 24 * time.Hour, Apply: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedSessions != 0 || report.RemovedProfiles != 0 {
		t.Fatalf("young session cleanup = %+v", report)
	}
	if _, err := LoadSession("young-dead"); err != nil {
		t.Fatalf("young dead session removed: %v", err)
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("young dead session profile removed: %v", err)
	}
}

func TestGCAuditsButNeverRemovesStaleKeepAliveSession(t *testing.T) {
	home := setBrowserHome(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	profile := filepath.Join(home, "profiles", "persistent")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveSession(&Session{
		Name: "persistent", PID: os.Getpid(), Port: 1, ProfileDir: profile,
		ChromePath: "/fake", StartedAt: now.Add(-3 * time.Hour),
		LifecyclePolicy: LifecycleKeepAlive,
	}); err != nil {
		t.Fatal(err)
	}
	report, err := GC(GCOptions{OlderThan: 2 * time.Hour, Apply: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.StaleKeepAlive) != 1 || report.StaleKeepAlive[0].Name != "persistent" {
		t.Fatalf("stale keep-alive report = %+v", report.StaleKeepAlive)
	}
	if report.RemovedSessions != 0 || report.RemovedProfiles != 0 {
		t.Fatalf("live keep-alive session was treated as garbage: %+v", report)
	}
	if _, err := LoadSession("persistent"); err != nil {
		t.Fatalf("live keep-alive session was removed: %v", err)
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("live keep-alive profile was removed: %v", err)
	}
}

func gcEntryNames(entries []GCEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	sort.Strings(names)
	return names
}

func TestSessionIsAliveFalseForBogusPID(t *testing.T) {
	s := &Session{PID: 999999} // very unlikely to exist
	if s.IsAlive() {
		t.Skip("pid 999999 happens to exist on this host; skipping")
	}
}

func TestSessionOwnershipRejectsUnrelatedLivePID(t *testing.T) {
	s := &Session{
		PID: os.Getpid(), ProfileDir: t.TempDir(), ChromePath: "/fake/chrome",
	}
	owned, err := s.OwnsProcess()
	if err != nil {
		t.Fatalf("OwnsProcess: %v", err)
	}
	if owned {
		t.Fatal("current test process must not be identified as the session Chromium")
	}
	err = s.SignalGracefulStop(0)
	if err == nil || !strings.Contains(err.Error(), "refusing to signal") {
		t.Fatalf("unowned live PID should be refused, got %v", err)
	}
}

func TestExpireIdleSessionStateMachine(t *testing.T) {
	home := setBrowserHome(t)
	now := time.Now().UTC()
	deadPID := 999999
	for processAlive(deadPID) {
		deadPID++
	}
	tests := []struct {
		name        string
		session     *Session
		expectedPID int
		want        IdleExpiryResult
		wantFile    bool
		purge       bool
	}{
		{
			name:        "active",
			session:     &Session{Name: "active-expiry", PID: os.Getpid(), StartedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Minute)},
			expectedPID: os.Getpid(),
			want:        IdleExpiryResult{Reason: IdleExpiryActive},
			wantFile:    true,
		},
		{
			name:        "new generation",
			session:     &Session{Name: "replaced-expiry", PID: os.Getpid(), StartedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour)},
			expectedPID: os.Getpid() + 1,
			want:        IdleExpiryResult{Done: true, Reason: IdleExpiryGenerationReplaced},
			wantFile:    true,
		},
		{
			name:        "dead process",
			session:     &Session{Name: "dead-expiry", PID: deadPID, StartedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour)},
			expectedPID: deadPID,
			want:        IdleExpiryResult{Done: true, Reason: IdleExpiryProcessGone},
		},
		{
			name:        "ownership lost",
			session:     &Session{Name: "unowned-expiry", PID: os.Getpid(), ProfileDir: filepath.Join(home, "profiles", "unowned-expiry"), ChromePath: "/fake/chrome", StartedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour)},
			expectedPID: os.Getpid(),
			want:        IdleExpiryResult{Done: true, Reason: IdleExpiryOwnershipLost},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := *tt.session
			session.IdleTimeout = (30 * time.Minute).String()
			session.PurgeOnExpiry = tt.purge
			if err := SaveSession(&session); err != nil {
				t.Fatal(err)
			}
			got, err := ExpireIdleSession(session.Name, tt.expectedPID, 30*time.Minute, tt.purge, now)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("result = %+v, want %+v", got, tt.want)
			}
			_, loadErr := LoadSession(session.Name)
			if tt.wantFile && loadErr != nil {
				t.Fatalf("session file removed: %v", loadErr)
			}
			if !tt.wantFile && !errors.Is(loadErr, os.ErrNotExist) {
				t.Fatalf("session file retained: %v", loadErr)
			}
		})
	}

	transientProfile := filepath.Join(home, "profiles", "dead-transient-expiry")
	if err := os.MkdirAll(transientProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	transient := &Session{
		Name: "dead-transient-expiry", PID: deadPID, ProfileDir: transientProfile,
		StartedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour),
		IdleTimeout: (30 * time.Minute).String(), PurgeOnExpiry: true,
	}
	if err := SaveSession(transient); err != nil {
		t.Fatal(err)
	}
	result, err := ExpireIdleSession(transient.Name, deadPID, 30*time.Minute, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if result != (IdleExpiryResult{Done: true, Reason: IdleExpiryProcessGone}) {
		t.Fatalf("transient result = %+v", result)
	}
	if _, err := os.Stat(transientProfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transient profile retained: %v", err)
	}

	missing, err := ExpireIdleSession("missing-expiry", os.Getpid(), time.Minute, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if missing != (IdleExpiryResult{Done: true, Reason: IdleExpiryMissing}) {
		t.Fatalf("missing result = %+v", missing)
	}
}

func TestExpireIdleSessionStopsWhenPolicyChanges(t *testing.T) {
	setBrowserHome(t)
	now := time.Now().UTC()
	if err := SaveSession(&Session{
		Name: "policy-change", PID: os.Getpid(), StartedAt: now.Add(-time.Hour),
		LifecyclePolicy: LifecycleKeepAlive,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := ExpireIdleSession("policy-change", os.Getpid(), time.Minute, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if result != (IdleExpiryResult{Done: true, Reason: IdleExpiryPolicyReplaced}) {
		t.Fatalf("result = %+v", result)
	}
	if _, err := LoadSession("policy-change"); err != nil {
		t.Fatalf("policy replacement removed session: %v", err)
	}
}

func TestExpireIdleSessionCannotBroadenPersistedPurgePolicy(t *testing.T) {
	home := setBrowserHome(t)
	now := time.Now().UTC()
	profile := filepath.Join(home, "profiles", "retain-on-expiry")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "sentinel"), []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadPID := 999999
	for processAlive(deadPID) {
		deadPID++
	}
	if err := SaveSession(&Session{
		Name: "retain-on-expiry", PID: deadPID, ProfileDir: profile,
		StartedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour),
		IdleTimeout: time.Minute.String(), LifecyclePolicy: LifecycleIdle,
		PurgeOnExpiry: false,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := ExpireIdleSession("retain-on-expiry", deadPID, time.Minute, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != IdleExpiryProcessGone {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(profile, "sentinel")); err != nil {
		t.Fatalf("watchdog argument broadened purge policy: %v", err)
	}
}

func TestSessionLifecycleLockSerializesSameName(t *testing.T) {
	setBrowserHome(t)
	unlockFirst, err := acquireSessionLifecycleLock("locked", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		unlock, lockErr := acquireSessionLifecycleLock("locked", time.Second)
		if lockErr != nil {
			errs <- lockErr
			return
		}
		acquired <- unlock
	}()

	select {
	case unlock := <-acquired:
		unlock()
		unlockFirst()
		t.Fatal("second lifecycle operation acquired before the first released")
	case err := <-errs:
		unlockFirst()
		t.Fatalf("second lifecycle operation failed while waiting: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	unlockFirst()
	select {
	case unlock := <-acquired:
		unlock()
	case err := <-errs:
		t.Fatalf("second lifecycle operation failed after release: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("second lifecycle operation did not acquire after release")
	}
}

func TestSessionLifecycleLockHonorsSameProcessTimeout(t *testing.T) {
	setBrowserHome(t)
	unlock, err := acquireSessionLifecycleLock("bounded", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	started := time.Now()
	secondUnlock, err := acquireSessionLifecycleLock("bounded", 80*time.Millisecond)
	if secondUnlock != nil {
		secondUnlock()
		t.Fatal("second same-process lifecycle lock unexpectedly acquired")
	}
	if err == nil || !strings.Contains(err.Error(), "lifecycle is busy") {
		t.Fatalf("bounded acquisition error = %v", err)
	}
	elapsed := time.Since(started)
	if elapsed < 60*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("bounded acquisition elapsed = %s, want near 80ms", elapsed)
	}
}

func TestSessionLifecycleLockSerializesAcrossProcesses(t *testing.T) {
	home := setBrowserHome(t)
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLifecycleLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		"PAGESKEIN_HOME="+home,
		"PAGESKEIN_LOCK_HELPER=1",
		"PAGESKEIN_LOCK_READY="+ready,
		"PAGESKEIN_LOCK_RELEASE="+release,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire lifecycle lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if unlock, err := acquireSessionLifecycleLock("cross-process", 100*time.Millisecond); err == nil {
		unlock()
		t.Fatal("parent acquired lifecycle lock while helper held it")
	} else if !strings.Contains(err.Error(), "lifecycle is busy") {
		t.Fatalf("cross-process contention error = %v", err)
	}
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
	cmd.Process = nil

	unlock, err := acquireSessionLifecycleLock("cross-process", time.Second)
	if err != nil {
		t.Fatalf("acquire after helper release: %v", err)
	}
	unlock()
}

func TestSessionLifecycleLockHelperProcess(t *testing.T) {
	if os.Getenv("PAGESKEIN_LOCK_HELPER") != "1" {
		return
	}
	unlock, err := acquireSessionLifecycleLock("cross-process", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := os.WriteFile(os.Getenv("PAGESKEIN_LOCK_READY"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("PAGESKEIN_LOCK_RELEASE")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for parent release")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSessionLifecycleLockDoesNotTreatDiagnosticFileAsOwnership(t *testing.T) {
	setBrowserHome(t)
	path, err := SessionPath("stale-diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".lifecycle.lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(`{"pid":999999,"token":"stale"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err := acquireSessionLifecycleLock("stale-diagnostic", time.Second)
	if err != nil {
		t.Fatalf("stale diagnostic file blocked kernel lock: %v", err)
	}
	unlock()
}

func TestWaitForDevToolsActivePort(t *testing.T) {
	profile := t.TempDir()
	path := filepath.Join(profile, "DevToolsActivePort")
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(path, []byte("not-a-port\n"), 0o600)
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(path, []byte("43123\n/devtools/browser/test\n"), 0o600)
	}()
	port, err := waitForDevToolsActivePort(profile, 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if port != 43123 {
		t.Fatalf("port = %d, want 43123", port)
	}
}

func TestOpenFailureRemovesOnlyNewProfiles(t *testing.T) {
	home := setBrowserHome(t)
	_, err := Open(OpenOptions{Name: "new-failure", ChromePath: filepath.Join(t.TempDir(), "missing-chrome")})
	if err == nil {
		t.Fatal("Open unexpectedly succeeded with a missing Chrome binary")
	}
	newProfile := filepath.Join(home, "profiles", "new-failure")
	if _, statErr := os.Stat(newProfile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("new failed profile retained: %v", statErr)
	}

	existingProfile := filepath.Join(home, "profiles", "existing-failure")
	if err := os.MkdirAll(existingProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existingProfile, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = Open(OpenOptions{
		Name: "existing-failure", ChromePath: os.Args[0], ReadyTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("Open unexpectedly succeeded without DevToolsActivePort")
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Fatalf("failed open removed reusable profile: %v", statErr)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("500ms readiness budget expanded across stages: %s", elapsed)
	}
}

func TestProfileDirForSandboxed(t *testing.T) {
	home := setBrowserHome(t)
	got, err := ProfileDirFor("foo")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "profiles", "foo")
	if got != want {
		t.Errorf("ProfileDirFor: got %s, want %s", got, want)
	}
}

func TestInspectSessionReadsTargets(t *testing.T) {
	home := setBrowserHome(t)
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/json/version":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Browser":              "FakeChrome/1",
				"webSocketDebuggerUrl": "ws://127.0.0.1:" + port + "/devtools/browser/root",
			})
		case "/json":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":                   "target-1",
				"type":                 "page",
				"url":                  "http://example.test/app",
				"title":                "Example App",
				"devtoolsFrontendUrl":  "/devtools/inspector.html?ws=127.0.0.1:" + port + "/devtools/page/target-1",
				"webSocketDebuggerUrl": "ws://127.0.0.1:" + port + "/devtools/page/target-1",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	s := &Session{
		Name:       "watch",
		PID:        os.Getpid(),
		Port:       mustAtoiForTest(t, port),
		ProfileDir: filepath.Join(home, "profiles", "watch"),
		ChromePath: "/fake/chrome",
		StartedAt:  time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC),
		WSURL:      "ws://127.0.0.1:" + port + "/devtools/browser/root",
	}
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}

	report, err := InspectSession("watch", InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || !report.Session.Reachable {
		t.Fatalf("inspect not reachable: %+v", report)
	}
	if report.Current == nil {
		t.Fatalf("missing current target: %+v", report.Targets)
	}
	if report.Current.URL != "http://example.test/app" || report.Current.Title != "Example App" {
		t.Fatalf("current target = %+v", report.Current)
	}
	if !strings.HasPrefix(report.Current.DevToolsFrontendURL, "http://127.0.0.1:"+port+"/devtools/") {
		t.Fatalf("devtools URL not normalized: %q", report.Current.DevToolsFrontendURL)
	}
}

func TestObserveSnapshotIncludesCommandSessionAndTargets(t *testing.T) {
	home := setBrowserHome(t)
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/json/version":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Browser":              "FakeChrome/1",
				"webSocketDebuggerUrl": "ws://127.0.0.1:" + port + "/devtools/browser/root",
			})
		case "/json":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":    "target-1",
				"type":  "page",
				"url":   "http://example.test/app",
				"title": "Example App",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	if err := SaveSession(&Session{
		Name:       "watch",
		PID:        os.Getpid(),
		Port:       mustAtoiForTest(t, port),
		ProfileDir: filepath.Join(home, "profiles", "watch"),
		ChromePath: "/fake/chrome",
		StartedAt:  time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC),
		WSURL:      "ws://127.0.0.1:" + port + "/devtools/browser/root",
	}); err != nil {
		t.Fatal(err)
	}
	if err := AppendCommandEvent("watch", ObserverCommand{
		Command:    "exec.navigate",
		ExitCode:   0,
		OK:         true,
		DurationMS: 12,
		Data:       map[string]any{"url": "http://example.test/app"},
	}); err != nil {
		t.Fatal(err)
	}

	events, _, err := ObserveSnapshot("", 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Kind] = true
	}
	for _, want := range []string{"browser.command", "browser.session", "browser.target"} {
		if !seen[want] {
			t.Fatalf("missing event kind %q in %#v", want, events)
		}
	}
}

func TestObserveSnapshotFiltersCommandEventsBySession(t *testing.T) {
	setBrowserHome(t)
	for _, name := range []string{"watch", "other"} {
		if err := AppendCommandEvent(name, ObserverCommand{
			Command:    "inspect",
			ExitCode:   0,
			OK:         true,
			DurationMS: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	events, _, err := ObserveSnapshot("watch", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "browser.command" && event.Session != "watch" {
			t.Fatalf("unfiltered command event: %+v", event)
		}
	}
}

func mustAtoiForTest(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("parse int %q: %v", s, err)
	}
	return n
}
