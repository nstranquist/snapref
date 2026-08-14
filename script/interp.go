package browserscript

import (
	"regexp"
	"strings"
)

// varRef matches ${name} references. Names are [A-Za-z0-9_.-].
var varRef = regexp.MustCompile(`\$\{([A-Za-z0-9_.\-]+)\}`)

// scope holds the live variable bindings during a run: plan params seeded first,
// then captures accumulate as steps complete.
type scope struct {
	vals map[string]string
}

func newScope(seed map[string]string) *scope {
	m := make(map[string]string, len(seed))
	for k, v := range seed {
		m[k] = v
	}
	return &scope{vals: m}
}

func (s *scope) set(name, val string) {
	if name == "" {
		return
	}
	s.vals[name] = val
}

// expand replaces ${name} references with bound values. Unknown references are
// left verbatim so a missing var is visible in the failure rather than silently
// becoming empty.
func (s *scope) expand(in string) string {
	if in == "" || !strings.Contains(in, "${") {
		return in
	}
	return varRef.ReplaceAllStringFunc(in, func(m string) string {
		name := varRef.FindStringSubmatch(m)[1]
		if v, ok := s.vals[name]; ok {
			return v
		}
		return m
	})
}

// snapshot returns a copy of the current bindings (for the run manifest).
func (s *scope) snapshot() map[string]string {
	out := make(map[string]string, len(s.vals))
	for k, v := range s.vals {
		out[k] = v
	}
	return out
}
