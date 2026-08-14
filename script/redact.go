package browserscript

import (
	"net/url"
	"strings"
)

// secretKeyHints mark variable names whose values must be masked in the
// persisted manifest. Mirrors the defense-in-depth posture of the
// browserobserve redactor (kept local to avoid a cross-module dependency).
var secretKeyHints = []string{
	"token", "secret", "password", "passwd", "pwd", "apikey", "api_key",
	"auth", "cookie", "bearer", "session", "credential", "private",
}

const redactedMark = "[redacted]"
const varValueMax = 512

// redactVars returns a display-safe copy of the bindings for the run manifest:
// secret-named values are masked, URL values have query/fragment stripped, and
// everything is length-bounded.
func redactVars(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = redactValue(k, v)
	}
	return out
}

func redactValue(key, val string) string {
	lk := strings.ToLower(key)
	for _, h := range secretKeyHints {
		if strings.Contains(lk, h) {
			return redactedMark
		}
	}
	return truncate(redactURL(val), varValueMax)
}

// redactReportForManifest returns a disk-safe copy of the report: per-step
// captured values and assertion "actual" values are run through the same
// redaction as vars, so secrets read off a page (capture: token, or an
// assert against a token field) are not persisted in plaintext. The live
// stdout stream keeps full values — only the durable artifact is scrubbed.
func redactReportForManifest(rep *RunReport) *RunReport {
	cp := *rep
	cp.Steps = make([]Result, len(rep.Steps))
	for i, s := range rep.Steps {
		rs := s
		if len(s.Captured) > 0 {
			rc := make(map[string]any, len(s.Captured))
			for k, v := range s.Captured {
				if sv, ok := v.(string); ok {
					rc[k] = redactValue(k, sv)
				} else {
					rc[k] = v
				}
			}
			rs.Captured = rc
		}
		if s.Assert != nil {
			a := *s.Assert
			a.Actual = truncate(redactURL(a.Actual), assertActualMax)
			rs.Assert = &a
		}
		cp.Steps[i] = rs
	}
	return &cp
}

// redactURL drops the query and fragment (where secrets/tokens hide) while
// keeping scheme/host/path for context. Non-URL strings pass through.
func redactURL(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
