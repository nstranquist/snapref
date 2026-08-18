package productshot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// lookPath and commandContext are overridable so tests can stub ffmpeg.
var (
	lookPath       = exec.LookPath
	commandContext = exec.CommandContext
)

// FFmpegMissingError is returned when a recording pack needs ffmpeg and it
// is not on PATH. Shot (still) packs never require ffmpeg.
type FFmpegMissingError struct {
	Wanted string
}

func (e FFmpegMissingError) Error() string {
	return fmt.Sprintf("%s is required for screen recordings (install ffmpeg, or set FFMPEG_PATH)", e.Wanted)
}

// FindFFmpeg returns the ffmpeg binary. FFMPEG_PATH wins when set.
func FindFFmpeg() (string, error) {
	if env := strings.TrimSpace(os.Getenv("FFMPEG_PATH")); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", FFmpegMissingError{Wanted: env}
		}
		return env, nil
	}
	path, err := lookPath("ffmpeg")
	if err != nil {
		return "", FFmpegMissingError{Wanted: "ffmpeg"}
	}
	return path, nil
}

// EncodeVideo turns a sequential JPEG frame directory into mp4 and/or gif
// according to spec. Frame files must be named 000001.jpg, 000002.jpg, ...
func EncodeVideo(ctx context.Context, framesDir, outDir string, spec VideoSpec) ([]Artifact, error) {
	bin, err := FindFFmpeg()
	if err != nil {
		return nil, err
	}
	if spec.FPS <= 0 {
		spec.FPS = 12
	}
	if spec.Width <= 0 {
		spec.Width = 1280
	}
	if spec.Height <= 0 {
		spec.Height = 720
	}
	pattern := filepath.Join(framesDir, "%06d.jpg")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	inFPS := spec.InputFPS
	if inFPS <= 0 {
		inFPS = float64(spec.FPS)
	}
	inFPSStr := strconv.FormatFloat(inFPS, 'f', 3, 64)
	var out []Artifact
	if spec.WantMP4 {
		path := filepath.Join(outDir, spec.Name+".mp4")
		args := []string{
			"-y",
			"-framerate", inFPSStr,
			"-i", pattern,
			"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,fps=%s,format=yuv420p",
				spec.Width, spec.Height, spec.Width, spec.Height, inFPSStr),
			"-c:v", "libx264",
			"-pix_fmt", "yuv420p",
			"-movflags", "+faststart",
			"-an",
		}
		if spec.MaxSeconds > 0 {
			args = append(args, "-t", strconv.Itoa(spec.MaxSeconds))
		}
		args = append(args, path)
		if err := runFFmpeg(ctx, bin, args); err != nil {
			return out, err
		}
		art, err := statArtifact(path, spec.Role+".mp4", spec.Width, spec.Height)
		if err != nil {
			return out, err
		}
		out = append(out, art)
	}
	if spec.WantGIF {
		gifW := spec.GIFWidth
		if gifW <= 0 {
			gifW = 800
		}
		path := filepath.Join(outDir, spec.Name+".gif")
		gifFPS := inFPS
		if gifFPS > 10 {
			gifFPS = 10
		}
		vf := fmt.Sprintf("fps=%s,scale=%d:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=128[p];[s1][p]paletteuse=dither=bayer",
			strconv.FormatFloat(gifFPS, 'f', 3, 64), gifW)
		args := []string{
			"-y",
			"-framerate", inFPSStr,
			"-i", pattern,
			"-vf", vf,
			"-loop", "0",
		}
		if spec.MaxSeconds > 0 {
			args = append(args, "-t", strconv.Itoa(spec.MaxSeconds))
		}
		args = append(args, path)
		if err := runFFmpeg(ctx, bin, args); err != nil {
			return out, err
		}
		art, err := statArtifact(path, spec.Role+".gif", gifW, 0)
		if err != nil {
			return out, err
		}
		out = append(out, art)
	}
	return out, nil
}

func runFFmpeg(ctx context.Context, bin string, args []string) error {
	cmd := commandContext(ctx, bin, args...)
	cmd.Stderr = nil
	cmd.Stdout = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func statArtifact(path, role string, w, h int) (Artifact, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Artifact{}, err
	}
	rel := path
	return Artifact{
		Role:   role,
		Path:   rel,
		Width:  w,
		Height: h,
		Bytes:  int(info.Size()),
		Kind:   "video",
	}, nil
}
