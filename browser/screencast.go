package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

const (
	screencastStateFile = "state.json"
	screencastStopFile  = "STOP"
	screencastDoneFile  = "done.json"
	screencastFramesDir = "frames"
)

// ScreencastState is the on-disk record for an in-progress session recording.
type ScreencastState struct {
	Session   string    `json:"session"`
	Dir       string    `json:"dir"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at"`
	MaxWidth  int       `json:"max_width,omitempty"`
	MaxHeight int       `json:"max_height,omitempty"`
	Quality   int       `json:"quality,omitempty"`
}

// ScreencastResult is written when the recorder process exits cleanly.
type ScreencastResult struct {
	OK         bool      `json:"ok"`
	Session    string    `json:"session"`
	Dir        string    `json:"dir"`
	Frames     int       `json:"frames"`
	StartedAt  time.Time `json:"started_at"`
	StoppedAt  time.Time `json:"stopped_at"`
	DurationMS int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

// RecordingDir is the per-session recording workspace.
func RecordingDir(name string) (string, error) {
	if err := ValidateSessionName(name); err != nil {
		return "", err
	}
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "recordings", name), nil
}

// LoadScreencastState reads an in-progress recording, if any.
func LoadScreencastState(name string) (*ScreencastState, error) {
	dir, err := RecordingDir(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, screencastStateFile))
	if err != nil {
		return nil, err
	}
	var st ScreencastState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	st.Dir = dir
	return &st, nil
}

// BeginScreencast prepares the recording directory. It fails if one is already
// running (state file exists and no STOP/done pair has completed).
func BeginScreencast(name string, pid, maxWidth, maxHeight, quality int) (*ScreencastState, error) {
	dir, err := RecordingDir(name)
	if err != nil {
		return nil, err
	}
	liveRecorders.mu.Lock()
	_, live := liveRecorders.byID[name]
	liveRecorders.mu.Unlock()
	if live {
		return nil, fmt.Errorf("session %q is already recording", name)
	}
	if st, err := LoadScreencastState(name); err == nil && !screencastFinished(dir) {
		if st.PID <= 0 || processAlive(st.PID) {
			return nil, fmt.Errorf("session %q is already recording", name)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, screencastFramesDir), 0o755); err != nil {
		return nil, err
	}
	if maxWidth <= 0 {
		maxWidth = 1280
	}
	if maxHeight <= 0 {
		maxHeight = 720
	}
	if quality <= 0 {
		quality = 70
	}
	st := &ScreencastState{
		Session:   name,
		Dir:       dir,
		PID:       pid,
		StartedAt: time.Now().UTC(),
		MaxWidth:  maxWidth,
		MaxHeight: maxHeight,
		Quality:   quality,
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, screencastStateFile), append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	return st, nil
}

// SetScreencastPID records the detached recorder pid without wiping frames.
func SetScreencastPID(name string, pid int) error {
	st, err := LoadScreencastState(name)
	if err != nil {
		return err
	}
	st.PID = pid
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(st.Dir, screencastStateFile), append(data, '\n'), 0o644)
}

// RequestScreencastStop writes the STOP sentinel the recorder watches.
func RequestScreencastStop(name string) error {
	dir, err := RecordingDir(name)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, screencastStopFile), []byte("stop\n"), 0o644)
}

// WaitScreencastDone waits for the recorder to write done.json.
func WaitScreencastDone(name string, timeout time.Duration) (*ScreencastResult, error) {
	dir, err := RecordingDir(name)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(filepath.Join(dir, screencastDoneFile))
		if err == nil {
			var res ScreencastResult
			if err := json.Unmarshal(data, &res); err != nil {
				return nil, err
			}
			return &res, nil
		}
		if st, loadErr := LoadScreencastState(name); loadErr == nil && st.PID > 0 && !processAlive(st.PID) {
			return nil, fmt.Errorf("recorder process %d exited before writing a result", st.PID)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for recording to stop")
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// CountScreencastFrames returns how many JPEG frames were written.
func CountScreencastFrames(dir string) (int, error) {
	entries, err := os.ReadDir(filepath.Join(dir, screencastFramesDir))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jpg" {
			n++
		}
	}
	return n, nil
}

// ScreencastFramesDir is the JPEG sequence directory.
func ScreencastFramesDir(dir string) string {
	return filepath.Join(dir, screencastFramesDir)
}

func screencastFinished(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, screencastDoneFile))
	return err == nil
}

// RunScreencast writes a JPEG frame sequence until ctx is cancelled or a
// STOP file appears. Headless Chrome does not emit Page.screencastFrame
// events (the page is hidden), so this captures viewport screenshots on a
// timer instead. Used by the detached recorder and in-process duration runs.
func RunScreencast(ctx context.Context, s *Session, dir string, maxWidth, maxHeight, quality int) (*ScreencastResult, error) {
	if s == nil {
		return nil, errors.New("session is required")
	}
	if maxWidth <= 0 {
		maxWidth = 1280
	}
	if maxHeight <= 0 {
		maxHeight = 720
	}
	if quality <= 0 {
		quality = 70
	}
	if err := os.MkdirAll(filepath.Join(dir, screencastFramesDir), 0o755); err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	res := &ScreencastResult{Session: s.Name, Dir: dir, StartedAt: started}
	stopPath := filepath.Join(dir, screencastStopFile)
	interval := 120 * time.Millisecond
	frameN := 0
	var loopErr error
	for {
		if _, err := os.Stat(stopPath); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			loopErr = nil
			goto done
		default:
		}
		png, err := RemoteScreenshotCurrentWithTimeout(s, false, 8*time.Second)
		if err != nil {
			loopErr = err
			break
		}
		jpg, err := pngToJPEG(png, quality)
		if err != nil {
			loopErr = err
			break
		}
		if maxWidth > 0 || maxHeight > 0 {
			if scaled, scaleErr := scaleJPEG(jpg, maxWidth, maxHeight, quality); scaleErr == nil {
				jpg = scaled
			}
		}
		frameN++
		path := filepath.Join(dir, screencastFramesDir, fmt.Sprintf("%06d.jpg", frameN))
		if err := os.WriteFile(path, jpg, 0o644); err != nil {
			loopErr = err
			break
		}
		select {
		case <-ctx.Done():
			loopErr = nil
			goto done
		case <-time.After(interval):
		}
	}
done:
	res.Frames = frameN
	res.StoppedAt = time.Now().UTC()
	res.DurationMS = res.StoppedAt.Sub(started).Milliseconds()
	res.OK = loopErr == nil && frameN > 0
	if loopErr != nil {
		res.Error = loopErr.Error()
	} else if frameN == 0 {
		res.Error = "no screencast frames captured"
	}
	data, _ := json.MarshalIndent(res, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, screencastDoneFile), append(data, '\n'), 0o644)
	if loopErr != nil {
		return res, loopErr
	}
	if frameN == 0 {
		return res, errors.New(res.Error)
	}
	return res, nil
}

type liveRecorder struct {
	cancel context.CancelFunc
}

var liveRecorders = struct {
	mu   sync.Mutex
	byID map[string]liveRecorder
}{byID: map[string]liveRecorder{}}

// StartLiveScreencast runs a recorder goroutine against an open session.
func StartLiveScreencast(s *Session, dir string, maxWidth, maxHeight, quality int) error {
	liveRecorders.mu.Lock()
	defer liveRecorders.mu.Unlock()
	if _, ok := liveRecorders.byID[s.Name]; ok {
		return fmt.Errorf("session %q is already recording", s.Name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	liveRecorders.byID[s.Name] = liveRecorder{cancel: cancel}
	go func() {
		_, _ = RunScreencast(ctx, s, dir, maxWidth, maxHeight, quality)
		liveRecorders.mu.Lock()
		delete(liveRecorders.byID, s.Name)
		liveRecorders.mu.Unlock()
	}()
	return nil
}

// StopLiveScreencast cancels an in-process recorder and waits for done.json.
func StopLiveScreencast(name string, timeout time.Duration) (*ScreencastResult, error) {
	liveRecorders.mu.Lock()
	rec, ok := liveRecorders.byID[name]
	liveRecorders.mu.Unlock()
	if ok {
		rec.cancel()
	}
	if err := RequestScreencastStop(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return WaitScreencastDone(name, timeout)
}

func pngToJPEG(png []byte, quality int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return nil, err
	}
	if quality <= 0 {
		quality = 70
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func scaleJPEG(jpg []byte, maxW, maxH, quality int) ([]byte, error) {
	if maxW <= 0 && maxH <= 0 {
		return jpg, nil
	}
	img, _, err := image.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return jpg, nil
	}
	scale := 1.0
	if maxW > 0 && w > maxW {
		scale = float64(maxW) / float64(w)
	}
	if maxH > 0 && h > maxH {
		if s := float64(maxH) / float64(h); s < scale {
			scale = s
		}
	}
	if scale >= 1 {
		return jpg, nil
	}
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 2 {
		nw = 2
	}
	if nh < 2 {
		nh = 2
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	var buf bytes.Buffer
	if quality <= 0 {
		quality = 70
	}
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
