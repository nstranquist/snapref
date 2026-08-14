package browserscript

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// ShotsUploadOptions configures pushing a run's screenshots into the
// screenshots-comparison viewer (PUT /api/shots/<page>/<slot>, content-type
// image/png). Local `pnpm dev:local` needs no auth; prod CF Access needs an
// admin email header.
type ShotsUploadOptions struct {
	BaseURL    string // e.g. http://localhost:5173 (no trailing slash needed)
	Slot       string // "before" | "after" (default "after")
	Viewport   string // page-key suffix the viewer files under (default "web")
	AdminEmail string // Cf-Access-Authenticated-User-Email (prod); empty for local
	Client     *http.Client
}

// ShotUploadResult is the per-screenshot upload outcome.
type ShotUploadResult struct {
	Page   string `json:"page"`
	Slot   string `json:"slot"`
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

var shotKeyRE = regexp.MustCompile(`[^a-z0-9]+`)

// safeShotKey lowercases and dash-collapses a label into a viewer page key.
func safeShotKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = shotKeyRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// UploadShots PUTs every screenshot produced by a run into the viewer. The page
// key is `<plan>-<step-label-or-verb-index>-<viewport>`; the slot is before/after.
// Steps without a screenshot are skipped. It returns one result per uploaded
// shot; a transport/HTTP error on one shot does not abort the rest.
func UploadShots(rep *RunReport, opts ShotsUploadOptions) []ShotUploadResult {
	slot := opts.Slot
	if slot != "before" {
		slot = "after"
	}
	viewport := opts.Viewport
	if viewport == "" {
		viewport = "web"
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	base := strings.TrimRight(opts.BaseURL, "/")

	var out []ShotUploadResult
	for _, st := range rep.Steps {
		if st.Screenshot == "" {
			continue
		}
		label := st.Label
		if label == "" {
			label = fmt.Sprintf("%s-%d", st.Verb, st.Index)
		}
		page := safeShotKey(rep.Plan+"-"+label) + "-" + safeShotKey(viewport)
		res := ShotUploadResult{Page: page, Slot: slot, Path: st.Screenshot}

		b, err := os.ReadFile(st.Screenshot)
		if err != nil {
			res.Error = "read: " + err.Error()
			out = append(out, res)
			continue
		}
		url := fmt.Sprintf("%s/api/shots/%s/%s", base, page, slot)
		req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		req.Header.Set("content-type", "image/png")
		if opts.AdminEmail != "" {
			req.Header.Set("Cf-Access-Authenticated-User-Email", opts.AdminEmail)
		}
		resp, err := client.Do(req)
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		res.Status = resp.StatusCode
		res.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
		if !res.OK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
			res.Error = strings.TrimSpace(string(body))
		}
		resp.Body.Close()
		out = append(out, res)
	}
	return out
}
