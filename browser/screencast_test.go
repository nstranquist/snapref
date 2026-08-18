package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordingDirAndBeginScreencast(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	st, err := BeginScreencast("demo", 0, 1280, 720, 70)
	if err != nil {
		t.Fatal(err)
	}
	if st.Session != "demo" || st.MaxWidth != 1280 {
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

func TestBeginScreencastRejectsUnfinishedZeroPID(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginScreencast("demo", 0, 1280, 720, 70); err == nil {
		t.Fatal("expected already-recording error for unfinished pid-0 state")
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
