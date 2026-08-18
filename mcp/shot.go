package browsermcp

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"github.com/nstranquist/snapref/browser"
	"github.com/nstranquist/snapref/productshot"
)

func (s *server) runProductShot(ctx context.Context, session, path, preset, title, caption, theme string, includeMobile bool) (string, bool) {
	if session == "" {
		return "session is required", true
	}
	requestedPath := path
	if requestedPath != "" && !s.opts.AllowHostWrite {
		s.audit("browser_shot", session, false, false, "host write disabled")
		return "browser_shot path is disabled on this bridge. Omit path for a unique managed temp directory, or enable host writes with SNAPREF_MCP_ALLOW_HOST_WRITE=1 / --allow-host-write.", true
	}
	sess, err := browser.LoadSession(session)
	if err != nil {
		return err.Error(), true
	}
	if !sess.IsAlive() {
		return fmt.Sprintf("session %q is not running", session), true
	}
	pageURL, pageTitle := browser.CurrentPageInfo(sess)
	opt, presets, err := shotOptions(preset, title, caption, theme, includeMobile, pageURL, pageTitle)
	if err != nil {
		return err.Error(), true
	}
	png, err := browser.RemoteScreenshotCurrentWithTimeout(sess, false, 45*time.Second)
	if err != nil {
		s.audit("browser_shot", session, requestedPath != "", false, err.Error())
		return err.Error(), true
	}
	img, err := productshot.DecodeImage(png)
	if err != nil {
		return err.Error(), true
	}
	src := productshot.Sources{Desktop: img, URL: pageURL}
	if opt.WantsMobile(presets) {
		mobile, err := captureMobileStill(ctx, sess, 45*time.Second)
		if err != nil {
			return err.Error(), true
		}
		src.Mobile = mobile
	}
	dir := requestedPath
	if dir == "" {
		dir = filepath.Join(os.TempDir(), productshot.DefaultDir(pageURL, presets))
	}
	pack, err := productshot.WritePack(dir, src, presets, opt)
	if err != nil {
		s.audit("browser_shot", session, requestedPath != "", false, err.Error())
		return err.Error(), true
	}
	if requestedPath != "" {
		s.audit("browser_shot", session, true, true, "path="+requestedPath)
	}
	body, _ := json.MarshalIndent(map[string]any{
		"ok":        true,
		"dir":       pack.Dir,
		"artifacts": pack.Manifest.Artifacts,
		"readme":    pack.Manifest.Readme,
	}, "", "  ")
	return string(body), false
}

func (s *server) runProductRecord(session, action, path, preset, title string) (string, bool) {
	if session == "" {
		return "session is required", true
	}
	action = normalizeRecordAction(action)
	if action == "stop" && path != "" && !s.opts.AllowHostWrite {
		s.audit("browser_record", session, false, false, "host write disabled")
		return "browser_record path is disabled on this bridge. Omit path for a unique managed temp directory, or enable host writes with SNAPREF_MCP_ALLOW_HOST_WRITE=1 / --allow-host-write.", true
	}
	sess, err := browser.LoadSession(session)
	if err != nil {
		return err.Error(), true
	}
	if !sess.IsAlive() {
		return fmt.Sprintf("session %q is not running", session), true
	}
	switch action {
	case "start":
		st, err := browser.BeginScreencast(session, 0, 1280, 720, 70)
		if err != nil {
			return err.Error(), true
		}
		if err := browser.StartLiveScreencast(sess, st.Dir, 1280, 720, 70); err != nil {
			return err.Error(), true
		}
		return "recording started: " + session, false
	case "stop":
		requestedPath := path
		res, err := browser.StopLiveScreencast(session, 20*time.Second)
		if err != nil {
			return err.Error(), true
		}
		if res.Frames == 0 {
			return "recording produced no frames", true
		}
		pageURL, pageTitle := browser.CurrentPageInfo(sess)
		opt, presets, err := shotOptions(preset, title, "", "light", false, pageURL, pageTitle)
		if err != nil {
			return err.Error(), true
		}
		png, err := browser.RemoteScreenshotCurrentWithTimeout(sess, false, 45*time.Second)
		if err != nil {
			return err.Error(), true
		}
		img, err := productshot.DecodeImage(png)
		if err != nil {
			return err.Error(), true
		}
		src := productshot.Sources{Desktop: img, URL: pageURL}
		if opt.WantsMobile(presets) {
			mobile, err := captureMobileStill(context.Background(), sess, 45*time.Second)
			if err != nil {
				return err.Error(), true
			}
			src.Mobile = mobile
		}
		dir := requestedPath
		if dir == "" {
			dir = filepath.Join(os.TempDir(), productshot.DefaultDir(pageURL, presets))
		}
		pack, err := productshot.WritePack(dir, src, presets, opt)
		if err != nil {
			return err.Error(), true
		}
		arts, err := encodeRecordingPresets(context.Background(), res, pack.Dir, presets)
		if err != nil {
			return err.Error(), true
		}
		if len(arts) > 0 {
			pack, err = productshot.AppendVideo(pack.Dir, arts)
			if err != nil {
				return err.Error(), true
			}
		}
		body, _ := json.MarshalIndent(map[string]any{
			"ok":        true,
			"dir":       pack.Dir,
			"frames":    res.Frames,
			"artifacts": pack.Manifest.Artifacts,
			"readme":    pack.Manifest.Readme,
		}, "", "  ")
		return string(body), false
	default:
		return "action must be start or stop", true
	}
}

func shotOptions(preset, title, caption, theme string, includeMobile bool, pageURL, pageTitle string) (productshot.Options, []productshot.Preset, error) {
	p, err := productshot.ParsePreset(preset)
	if err != nil {
		return productshot.Options{}, nil, err
	}
	th, err := productshot.ParseTheme(theme)
	if err != nil {
		return productshot.Options{}, nil, err
	}
	opt := productshot.Options{
		Title:         title,
		Caption:       caption,
		URL:           pageURL,
		Theme:         th,
		IncludeMobile: includeMobile,
	}
	if opt.Title == "" {
		opt.Title = pageTitle
	}
	return opt, []productshot.Preset{p}, nil
}

func captureMobileStill(ctx context.Context, sess *browser.Session, timeout time.Duration) (image.Image, error) {
	mw, mh := productshot.MobileCapture()
	png, err := browser.ScreenshotAtViewport(ctx, sess, browser.Viewport{Width: mw, Height: mh}, timeout)
	if err != nil {
		return nil, err
	}
	return productshot.DecodeImage(png)
}

func encodeRecordingPresets(ctx context.Context, rec *browser.ScreencastResult, packDir string, presets []productshot.Preset) ([]productshot.Artifact, error) {
	if rec == nil {
		return nil, fmt.Errorf("recording produced no frames")
	}
	frames := browser.ScreencastFramesDir(rec.Dir)
	videoDir := filepath.Join(packDir, "record")
	var out []productshot.Artifact
	for _, preset := range presets {
		for _, spec := range productshot.VideoSpecs(preset) {
			if rec != nil {
				spec = spec.WithCapture(rec.Frames, rec.DurationMS)
			}
			arts, err := productshot.EncodeVideo(ctx, frames, videoDir, spec)
			if err != nil {
				return out, err
			}
			for i := range arts {
				if rel, err := filepath.Rel(packDir, arts[i].Path); err == nil {
					arts[i].Path = rel
				}
			}
			out = append(out, arts...)
		}
	}
	return out, nil
}

func normalizeRecordAction(action string) string {
	switch action {
	case "", "start":
		return "start"
	default:
		return action
	}
}
