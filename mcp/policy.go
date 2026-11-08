package browsermcp

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidatePolicy rejects invalid new grants before serving any requests. Legacy
// AllowOrigins retains its host-only interpretation for compatibility.
func ValidatePolicy(opts Options) error {
	for _, host := range opts.AllowHosts {
		if !validHost(host) {
			return fmt.Errorf("invalid MCP host grant; use a hostname or IP without scheme, path or port")
		}
	}
	for _, origin := range opts.ExactOrigins {
		if _, err := canonicalOrigin(origin, true); err != nil {
			return fmt.Errorf("invalid MCP exact-origin grant; use http(s)://host[:port] without credentials, path, query or fragment")
		}
	}
	return nil
}

func validHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/?#@* \\%") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 || strings.Contains(host, ":") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func canonicalOrigin(raw string, grant bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || !validHost(u.Hostname()) || u.Opaque != "" || u.User != nil {
		return "", fmt.Errorf("invalid origin")
	}
	if grant && ((u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "") {
		return "", fmt.Errorf("origin includes URL components")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	// ParseURL accepts numeric ports outside the TCP range; reject those too.
	var n int
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil || n < 1 || n > 65535 || fmt.Sprint(n) != port {
		return "", fmt.Errorf("invalid port")
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}

func navigationAllowed(opts Options, raw string) bool {
	if len(opts.AllowOrigins)+len(opts.AllowHosts)+len(opts.ExactOrigins) == 0 {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	matchesHosts := func(hosts []string, legacy bool) bool {
		if len(hosts) == 0 {
			return true
		}
		for _, allowed := range hosts {
			allowed = strings.TrimSpace(allowed)
			if legacy {
				if i := strings.Index(allowed, "://"); i >= 0 {
					allowed = allowed[i+3:]
				}
				allowed = strings.TrimSuffix(allowed, "/")
				if h, _, err := net.SplitHostPort(allowed); err == nil {
					allowed = h
				}
			}
			allowed = strings.ToLower(allowed)
			if host == allowed || net.ParseIP(allowed) == nil && strings.HasSuffix(host, "."+allowed) {
				return true
			}
		}
		return false
	}
	if !matchesHosts(opts.AllowOrigins, true) || !matchesHosts(opts.AllowHosts, false) {
		return false
	}
	if len(opts.ExactOrigins) == 0 {
		return true
	}
	origin, err := canonicalOrigin(raw, false)
	if err != nil {
		return false
	}
	for _, allowed := range opts.ExactOrigins {
		if other, err := canonicalOrigin(allowed, true); err == nil && other == origin {
			return true
		}
	}
	return false
}
