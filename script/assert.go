package browserscript

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// AssertOutcome is the verdict of evaluating an Assertion against an actual
// value. It is part of a step Result.
type AssertOutcome struct {
	OK       bool   `json:"ok"`
	Mode     string `json:"mode"`     // equals | contains | regex (joined with + when multiple)
	Expected string `json:"expected"` // human description of what was required
	Actual   string `json:"actual"`   // the observed value (truncated for display)
}

const assertActualMax = 256

// evalAssertion checks actual against every mode set on a. Multiple modes are
// AND-ed. A nil/empty assertion is treated as a vacuous pass.
func evalAssertion(actual string, a *Assertion) AssertOutcome {
	out := AssertOutcome{OK: true, Actual: truncate(actual, assertActualMax)}
	if a.empty() {
		out.Mode = "none"
		out.Expected = "(no assertion)"
		return out
	}
	var modes, expects []string
	if a.Equals != "" {
		modes = append(modes, "equals")
		expects = append(expects, fmt.Sprintf("== %q", a.Equals))
		if actual != a.Equals {
			out.OK = false
		}
	}
	if a.Contains != "" {
		modes = append(modes, "contains")
		expects = append(expects, fmt.Sprintf("contains %q", a.Contains))
		if !strings.Contains(actual, a.Contains) {
			out.OK = false
		}
	}
	if a.Regex != "" {
		modes = append(modes, "regex")
		expects = append(expects, fmt.Sprintf("=~ %q", a.Regex))
		re, err := regexp.Compile(a.Regex)
		if err != nil {
			out.OK = false
			expects = append(expects, fmt.Sprintf("(invalid regex: %v)", err))
		} else if !re.MatchString(actual) {
			out.OK = false
		}
	}
	out.Mode = strings.Join(modes, "+")
	out.Expected = strings.Join(expects, " AND ")
	return out
}

func evalCountAssertion(actual string, a *CountAssertion) AssertOutcome {
	out := AssertOutcome{OK: true, Actual: truncate(actual, assertActualMax)}
	if a.empty() {
		out.Mode = "none"
		out.Expected = "(no count assertion)"
		return out
	}
	count, err := parseCountActual(actual)
	if err != nil {
		out.OK = false
		out.Mode = "count"
		out.Expected = "parse selector count"
		return out
	}
	out.Actual = strconv.Itoa(count)
	var modes, expects []string
	if a.Equals != nil {
		modes = append(modes, "equals")
		expects = append(expects, fmt.Sprintf("== %d", *a.Equals))
		if count != *a.Equals {
			out.OK = false
		}
	}
	if a.AtLeast != nil {
		modes = append(modes, "at_least")
		expects = append(expects, fmt.Sprintf(">= %d", *a.AtLeast))
		if count < *a.AtLeast {
			out.OK = false
		}
	}
	if a.AtMost != nil {
		modes = append(modes, "at_most")
		expects = append(expects, fmt.Sprintf("<= %d", *a.AtMost))
		if count > *a.AtMost {
			out.OK = false
		}
	}
	out.Mode = strings.Join(modes, "+")
	out.Expected = strings.Join(expects, " AND ")
	return out
}

func parseCountActual(actual string) (int, error) {
	raw := strings.TrimSpace(actual)
	if raw == "" {
		return 0, fmt.Errorf("empty count")
	}
	var n int
	if err := json.Unmarshal([]byte(raw), &n); err == nil {
		return n, nil
	}
	var f float64
	if err := json.Unmarshal([]byte(raw), &f); err == nil {
		return int(f), nil
	}
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return strconv.Atoi(strings.TrimSpace(s))
	}
	return strconv.Atoi(raw)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Back up to a UTF-8 rune boundary so we never split a multibyte rune.
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
