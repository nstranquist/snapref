package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordingDirAndBeginScreencast(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	st, err := BeginScreencast("demo", 0, 1280, 720, 70)
	if err != nil {
		t.Fatal(err)
	}
	if st.Session != "demo" || st.MaxWidth != 1280 || st.Kind != screencastKindDetached || st.PID != 0 {
		t.Fatalf("state = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "frames")); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadScreencastState("demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Session != "demo" {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestRequestScreencastStopAndWaitDone(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	st, err := BeginScreencast("demo", 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := RequestScreencastStop("demo"); err != nil {
		t.Fatal(err)
	}
	res := ScreencastResult{OK: true, Session: "demo", Dir: st.Dir, Frames: 3, StartedAt: time.Now().UTC(), StoppedAt: time.Now().UTC()}
	data, _ := json.Marshal(res)
	if err := os.WriteFile(filepath.Join(st.Dir, screencastDoneFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := WaitScreencastDone("demo", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Frames != 3 {
		t.Fatalf("result = %+v", got)
	}
}

func TestBeginScreencastRejectsLiveRecorder(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if _, err := BeginScreencast("demo", os.Getpid(), 1280, 720, 70); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginScreencast("demo", os.Getpid(), 1280, 720, 70); err == nil {
		t.Fatal("expected already-recording error")
	}
}

func TestBeginScreencastReclaimsAbandonedZeroPID(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	first, err := BeginInProcessScreencast("demo", 1280, 720, 70)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != screencastKindInproc {
		t.Fatalf("kind = %q, want %q", first.Kind, screencastKindInproc)
	}
	second, err := BeginInProcessScreencast("demo", 1280, 720, 70)
	if err != nil {
		t.Fatalf("abandoned pid-0 state should be reclaimed: %v", err)
	}
	if second.Dir != first.Dir {
		t.Fatalf("dir = %q, want %q", second.Dir, first.Dir)
	}
}

func TestAbortScreencastAllowsDetachedRestart(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err == nil {
		t.Fatal("expected already-recording error before abort")
	}
	if err := AbortScreencast("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err != nil {
		t.Fatalf("abort should unblock next start: %v", err)
	}
}

func TestAbortScreencastRejectsLiveRecorder(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	liveRecorders.mu.Lock()
	liveRecorders.byID["demo"] = liveRecorder{cancel: func() {}}
	liveRecorders.mu.Unlock()
	t.Cleanup(func() {
		liveRecorders.mu.Lock()
		delete(liveRecorders.byID, "demo")
		liveRecorders.mu.Unlock()
	})
	if err := AbortScreencast("demo"); err == nil {
		t.Fatal("expected still-recording error")
	}
}

func TestStartInProcessScreencastRejectsDetachedPending(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err != nil {
		t.Fatal(err)
	}
	if _, err := StartInProcessScreencast(&Session{Name: "demo"}, 1280, 720, 70); err == nil {
		t.Fatal("expected already-recording error for detached pending state")
	}
	if !liveScreencastActive("demo") {
		return
	}
	t.Fatal("failed start must not leave a live-recorder reservation")
}

func TestBeginScreencastRejectsDetachedPendingZeroPID(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err == nil {
		t.Fatal("expected already-recording error for detached pending pid-0")
	}
}

func TestBeginScreencastRejectsLiveInProcessRecorder(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	liveRecorders.mu.Lock()
	liveRecorders.byID["demo"] = liveRecorder{cancel: func() {}}
	liveRecorders.mu.Unlock()
	t.Cleanup(func() {
		liveRecorders.mu.Lock()
		delete(liveRecorders.byID, "demo")
		liveRecorders.mu.Unlock()
	})
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err == nil {
		t.Fatal("expected already-recording error for live in-process recorder")
	}
}

func TestWaitScreencastDoneReportsAbandonedInproc(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if _, err := BeginInProcessScreencast("demo", 1280, 720, 70); err != nil {
		t.Fatal(err)
	}
	_, err := WaitScreencastDone("demo", 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "in-process recorder") {
		t.Fatalf("err = %v, want in-process recorder gone", err)
	}
}

func TestWaitScreencastDoneFindsDoneAfterLiveUnregisters(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	st, err := BeginInProcessScreencast("demo", 1280, 720, 70)
	if err != nil {
		t.Fatal(err)
	}
	liveRecorders.mu.Lock()
	liveRecorders.byID["demo"] = liveRecorder{cancel: func() {}}
	liveRecorders.mu.Unlock()
	t.Cleanup(func() {
		waitScreencastAfterDoneMiss = nil
		liveRecorders.mu.Lock()
		delete(liveRecorders.byID, "demo")
		liveRecorders.mu.Unlock()
	})
	waitScreencastAfterDoneMiss = func() {
		res := ScreencastResult{OK: true, Session: "demo", Dir: st.Dir, Frames: 2, StartedAt: time.Now().UTC(), StoppedAt: time.Now().UTC()}
		if err := WriteScreencastResult(st.Dir, res); err != nil {
			t.Errorf("write done.json: %v", err)
		}
		liveRecorders.mu.Lock()
		delete(liveRecorders.byID, "demo")
		liveRecorders.mu.Unlock()
	}
	got, err := WaitScreencastDone("demo", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Frames != 2 {
		t.Fatalf("result = %+v", got)
	}
}

func TestWaitScreencastDoneRetriesTruncatedDoneJSON(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	st, err := BeginInProcessScreencast("demo", 1280, 720, 70)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, screencastDoneFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		res *ScreencastResult
		err error
	}, 1)
	go func() {
		res, err := WaitScreencastDone("demo", 2*time.Second)
		done <- struct {
			res *ScreencastResult
			err error
		}{res, err}
	}()
	time.Sleep(120 * time.Millisecond)
	res := ScreencastResult{OK: true, Session: "demo", Dir: st.Dir, Frames: 4, StartedAt: time.Now().UTC(), StoppedAt: time.Now().UTC()}
	if err := WriteScreencastResult(st.Dir, res); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.res.OK || got.res.Frames != 4 {
		t.Fatalf("result = %+v", got.res)
	}
}

func TestBeginScreencastFailsClosedOnUnreadableState(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	st, err := BeginScreencast("demo", 0, 1280, 720, 70)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, screencastStateFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err == nil {
		t.Fatal("expected fail-closed on unreadable state")
	}
	if _, err := StartInProcessScreencast(&Session{Name: "demo"}, 1280, 720, 70); err == nil {
		t.Fatal("expected reserved start to refuse unreadable detached state")
	}
	if liveScreencastActive("demo") {
		t.Fatal("failed reserved start must not leave a live reservation")
	}
	if err := AbortScreencast("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err != nil {
		t.Fatalf("abort of unreadable pending should unblock: %v", err)
	}
}

func TestWriteScreencastJSONReplacesWithoutDeletingFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	first := ScreencastState{Session: "demo", Kind: screencastKindDetached}
	if err := writeScreencastJSON(path, first); err != nil {
		t.Fatal(err)
	}
	second := ScreencastState{Session: "demo", Kind: screencastKindDetached, PID: 9}
	if err := writeScreencastJSON(path, second); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got ScreencastState
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.PID != 9 {
		t.Fatalf("state = %+v", got)
	}
}

func TestAbortScreencastRejectsLiveDetachedPID(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if _, err := BeginScreencast("demo", os.Getpid(), 1280, 720, 70); err != nil {
		t.Fatal(err)
	}
	if err := AbortScreencast("demo"); err == nil {
		t.Fatal("expected abort to refuse a live detached PID")
	}
}

func TestCountScreencastFrames(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, screencastFramesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, screencastFramesDir, "000001.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, screencastFramesDir, "000002.jpg"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := CountScreencastFrames(dir)
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}
}
