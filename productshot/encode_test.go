package productshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindFFmpegMissing(t *testing.T) {
	t.Setenv("FFMPEG_PATH", "")
	orig := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { lookPath = orig })
	_, err := FindFFmpeg()
	if err == nil {
		t.Fatal("expected missing ffmpeg")
	}
	if _, ok := err.(FFmpegMissingError); !ok {
		t.Fatalf("want FFmpegMissingError, got %T %v", err, err)
	}
}

func TestEncodeVideoUsesStubAndWritesOutputs(t *testing.T) {
	frames := t.TempDir()
	if err := os.WriteFile(filepath.Join(frames, "000001.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	origLook := lookPath
	origCmd := commandContext
	lookPath = func(string) (string, error) { return "/usr/bin/ffmpeg", nil }
	commandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// Last arg is the output path. Create it so EncodeVideo can stat it.
		script := "#!/bin/sh\nout=\"\"\nfor a in \"$@\"; do out=\"$a\"; done\n: > \"$out\"\n"
		path := filepath.Join(t.TempDir(), "ffmpeg-stub")
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, path, args...)
	}
	t.Cleanup(func() {
		lookPath = origLook
		commandContext = origCmd
	})
	arts, err := EncodeVideo(context.Background(), frames, outDir, VideoSpec{
		Name: "github-readme", Role: "github-readme.demo",
		Width: 1280, Height: 720, FPS: 12, WantMP4: true, WantGIF: true, GIFWidth: 800,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 2 {
		t.Fatalf("artifacts = %d, want 2: %+v", len(arts), arts)
	}
	for _, a := range arts {
		if !strings.Contains(a.Path, outDir) {
			t.Fatalf("artifact path not under out dir: %s", a.Path)
		}
		if _, err := os.Stat(a.Path); err != nil {
			t.Fatalf("missing encoded file %s: %v", a.Path, err)
		}
	}
}
