package browsermcp

import (
	"strconv"
	"strings"
)

// auditDetail persists operation metadata, never page content, URLs, selectors,
// local paths, script names, or driver errors. Tool responses retain their full
// diagnostics. New tools get a safe default until their metadata is reviewed.
func auditDetail(tool string, allowed, ok bool, detail string) string {
	if !allowed {
		switch detail {
		case "driving disabled", "host read disabled", "host write disabled":
			return detail
		}
		if strings.HasPrefix(detail, "origin not allowlisted: ") {
			return "origin not allowlisted"
		}
	}
	if !ok {
		return "operation failed"
	}
	fields := strings.Fields(detail)
	count := func(key string) string {
		if len(fields) == 0 {
			return ""
		}
		value, found := strings.CutPrefix(fields[len(fields)-1], key+"=")
		if !found {
			return ""
		}
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return ""
		}
		return key + "=" + value
	}
	switch tool {
	case "browser_console":
		var out []string
		if strings.HasPrefix(detail, "navigate=") {
			out = append(out, "navigate")
		} else if strings.HasPrefix(detail, "reload ") {
			out = append(out, "reload")
		}
		for _, field := range fields {
			switch field {
			case "eval", "eval=settled", "eval=navigated":
				out = append(out, field)
			}
		}
		// Console constructs these final two fields from measured counts.
		if len(fields) >= 2 {
			for _, field := range fields[len(fields)-2:] {
				key, value, found := strings.Cut(field, "=")
				if found && (key == "errors" || key == "warnings") {
					if _, err := strconv.ParseUint(value, 10, 64); err == nil {
						out = append(out, field)
					}
				}
			}
		}
		return strings.Join(out, " ")
	case "browser_navigate":
		out := []string{"navigate"}
		for _, field := range fields {
			if field == "reload=true" || field == "bypass_cache=true" {
				out = append(out, field)
			}
		}
		return strings.Join(out, " ")
	case "browser_select":
		if value := count("option_bytes"); value != "" {
			return "select " + value
		}
		return "select"
	case "browser_script_run":
		if value := count("steps"); value != "" {
			return "script " + value
		}
		return "script"
	case "browser_eval":
		return "eval"
	case "browser_fetch":
		return "fetch"
	case "browser_click", "browser_type", "browser_press":
		return "actuation"
	case "browser_shot", "browser_screenshot":
		return "host write"
	case "browser_record":
		return "record"
	default:
		return "completed"
	}
}
