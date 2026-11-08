package browsermcp

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExplicitNavigationPolicies(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		url  string
		want bool
	}{
		{"unrestricted", Options{}, "https://other.test:443/path", true},
		{"host subdomain", Options{AllowHosts: []string{"example.com"}}, "http://www.example.com:8123/path", true},
		{"host suffix boundary", Options{AllowHosts: []string{"example.com"}}, "https://evilexample.com", false},
		{"exact default port", Options{ExactOrigins: []string{"https://example.com"}}, "https://EXAMPLE.com:443/path?q=1", true},
		{"exact scheme", Options{ExactOrigins: []string{"https://example.com"}}, "http://example.com", false},
		{"exact port", Options{ExactOrigins: []string{"https://example.com"}}, "https://example.com:8443", false},
		{"exact subdomain", Options{ExactOrigins: []string{"https://example.com"}}, "https://www.example.com", false},
		{"ipv6", Options{ExactOrigins: []string{"http://[::1]:8080"}}, "http://[::1]:8080/a", true},
		{"policies intersect", Options{AllowHosts: []string{"other.test"}, ExactOrigins: []string{"https://example.com"}}, "https://example.com", false},
		{"legacy host semantics", Options{AllowOrigins: []string{"https://example.com:7777"}}, "http://www.example.com:8080", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePolicy(tc.opts); err != nil {
				t.Fatal(err)
			}
			if got := navigationAllowed(tc.opts, tc.url); got != tc.want {
				t.Fatalf("allowed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInvalidPolicyFailsBeforeServing(t *testing.T) {
	for _, opts := range []Options{
		{AllowHosts: []string{"https://example.com"}}, {AllowHosts: []string{"*.example.com"}},
		{AllowHosts: []string{""}}, {AllowHosts: []string{"example.com:443"}},
		{ExactOrigins: []string{"https://example.com/private"}}, {ExactOrigins: []string{"https://user:secret@example.com"}},
		{ExactOrigins: []string{"https://example.com?token=secret"}}, {ExactOrigins: []string{"https://example.com:99999"}},
		{ExactOrigins: []string{"file:///tmp/private"}},
	} {
		var out bytes.Buffer
		if err := Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), &out, opts); err == nil || out.Len() != 0 {
			t.Fatalf("invalid policy served requests: %+v", opts)
		}
	}
}

type brokenAudit struct{ short bool }

func (w brokenAudit) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("secret writer path")
}

func TestAuditFailureIsVisibleWithoutChangingSuccessfulActuation(t *testing.T) {
	for _, short := range []bool{false, true} {
		var diagnostic bytes.Buffer
		s := &server{opts: Options{Audit: brokenAudit{short}, Diagnostics: &diagnostic, Now: time.Now}}
		for range 2 {
			if _, bad := s.runDrive("browser_click", "s", "clicked", func() error { return nil }); bad {
				t.Fatal("audit failure must not invite retry of completed actuation")
			}
		}
		if got := diagnostic.String(); strings.Count(got, "audit write failed") != 1 || strings.Contains(got, "secret") {
			t.Fatalf("diagnostic = %q", got)
		}
	}
}
