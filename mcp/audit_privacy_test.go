package browsermcp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nstranquist/snapref/browser"
)

type sensitiveErrorDriver struct{ *fakeDriver }

func (d sensitiveErrorDriver) Navigate(context.Context, string, string, ...browser.NavigateOptions) error {
	return errors.New("driver-secret https://user:pass@example.test/path?token=query-secret#fragment-secret")
}

func TestAuditDoesNotPersistSensitiveInputsOrDriverErrors(t *testing.T) {
	const rawURL = "https://user:password-secret@example.test/private-path?token=query-secret#fragment-secret"
	for _, tool := range []string{"browser_navigate", "browser_fetch"} {
		var audit bytes.Buffer
		d := &fakeDriver{}
		s := newTestServer(d, true, &audit)
		out, failed := call(s, tool, map[string]any{"session": "a", "url": rawURL})
		if failed || !strings.Contains(out, rawURL) {
			t.Fatalf("client result changed: %s", out)
		}
		if strings.Contains(audit.String(), rawURL) || strings.Contains(audit.String(), "secret") || strings.Contains(audit.String(), "private-path") {
			t.Fatalf("sensitive audit: %s", audit.String())
		}
		if !strings.Contains(audit.String(), `"ok":true`) {
			t.Fatalf("missing outcome: %s", audit.String())
		}
	}
	var audit bytes.Buffer
	s := newTestServer(sensitiveErrorDriver{&fakeDriver{}}, true, &audit)
	out, failed := call(s, "browser_navigate", map[string]any{"session": "a", "url": rawURL})
	if !failed || !strings.Contains(out, "driver-secret") {
		t.Fatalf("client error changed: %s", out)
	}
	if strings.Contains(audit.String(), "secret") || !strings.Contains(audit.String(), `"ok":false`) {
		t.Fatalf("error leaked or outcome lost: %s", audit.String())
	}
}

func TestAuditFiltersUntrustedDetailsCentrally(t *testing.T) {
	for _, tt := range []struct {
		tool, detail string
		allowed, ok  bool
	}{
		{"browser_console", "navigate=https://u:password-secret@x/?token=query-secret eval=eval-secret errors=1 warnings=0", true, true},
		{"browser_console", "console failure error-secret", true, false},
		{"browser_navigate", "origin not allowlisted: https://u:password-secret@x/?token=query-secret", false, false},
		{"browser_type", "target=[data-token=selector-secret]", true, true},
		{"browser_screenshot", "path=/private/path-secret", true, true},
		{"future_tool", "new-format-secret", true, true},
	} {
		var audit bytes.Buffer
		s := newTestServer(&fakeDriver{}, true, &audit)
		s.audit(tt.tool, "a", tt.allowed, tt.ok, tt.detail)
		if strings.Contains(audit.String(), "secret") {
			t.Fatalf("sensitive audit: %s", audit.String())
		}
	}
}
