package browser

import (
	"context"
	"errors"
	"runtime/debug"
	"strings"

	"github.com/nstranquist/snapref/contract"
)

const BrowserTelemetrySchemaVersion = 2

type ErrorClass string

const (
	ErrorClassUsage     ErrorClass = "usage"
	ErrorClassPolicy    ErrorClass = "policy"
	ErrorClassAssertion ErrorClass = "assertion"
	ErrorClassSelector  ErrorClass = "selector"
	ErrorClassTransport ErrorClass = "transport"
	ErrorClassTimeout   ErrorClass = "timeout"
	ErrorClassProduct   ErrorClass = "product"
	ErrorClassInternal  ErrorClass = "internal"
)

func BrowserBuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	revision := ""
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision == "" {
		return "dev"
	}
	if dirty {
		return revision + "+dirty"
	}
	return revision
}

func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrorClassTimeout
	}
	var refErr *SnapshotRefError
	if errors.As(err, &refErr) {
		return ErrorClassSelector
	}
	// Console capture failures are typed, so their (page-supplied) text cannot
	// be misread by the substring rules below: an eval that threw is the
	// caller's interaction failing, an unreachable target is the product, and
	// a load or an eval that never finished is a timeout.
	var missingErr *SessionMissingError
	if errors.As(err, &missingErr) {
		return ErrorClassUsage
	}
	var evalErr *ConsoleEvalError
	if errors.As(err, &evalErr) {
		return ErrorClassUsage
	}
	var navErr *ConsoleNavigateError
	if errors.As(err, &navErr) {
		return ErrorClassProduct
	}
	var loadErr *ConsoleLoadTimeoutError
	if errors.As(err, &loadErr) {
		return ErrorClassTimeout
	}
	var evalTimeoutErr *ConsoleEvalTimeoutError
	if errors.As(err, &evalTimeoutErr) {
		return ErrorClassTimeout
	}
	message := strings.ToLower(err.Error())
	switch {
	case containsAny(message, "drive-gated", "driving disabled", "disabled on this bridge", "not in the navigation allowlist", "host write", "host file read", "policy"):
		return ErrorClassPolicy
	case containsAny(message, "assertion failed", "does not contain", "fail-on-error", "console captured"):
		return ErrorClassAssertion
	case containsAny(message, "timeout", "deadline exceeded", "context canceled"):
		return ErrorClassTimeout
	case containsAny(message, "selector", "snapshot ref", "no node", "not visible", "precondition=", "fingerprint"):
		return ErrorClassSelector
	case containsAny(message, "websocket", "cdp", "connection refused", "transport", "no page target", "not reachable"):
		return ErrorClassTransport
	case containsAny(message, "usage:", "requires ", "invalid ", "unknown flag", "unknown tool", "malformed", "no such session", "is already running"):
		return ErrorClassUsage
	case containsAny(message, "product", "upstream returned", "http 4", "http 5"):
		return ErrorClassProduct
	default:
		return ErrorClassInternal
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func telemetryOperation(command string, data map[string]any) string {
	var id browsercontract.OperationID
	switch {
	case strings.HasPrefix(command, "exec."):
		verb := strings.TrimPrefix(command, "exec.")
		if verb == "screenshot-current" {
			verb = string(browsercontract.Screenshot)
		}
		if verb == "assert" {
			switch data["assertion"] {
			case "text":
				id = browsercontract.AssertText
			case "count":
				id = browsercontract.AssertCount
			default:
				id = browsercontract.AssertVisible
			}
		} else if operation, ok := browsercontract.ByScript(strings.ReplaceAll(verb, "-", "_")); ok {
			id = operation.ID
		}
	case strings.HasPrefix(command, "script:"):
		verb := strings.TrimPrefix(command, "script:")
		if verb == "start" || verb == "end" {
			id = browsercontract.ScriptRun
		} else if operation, ok := browsercontract.ByScript(verb); ok {
			id = operation.ID
		}
	default:
		if operation, ok := browsercontract.ByID(browsercontract.OperationID(command)); ok {
			id = operation.ID
		}
	}
	if operation, ok := browsercontract.ByID(id); ok {
		return operation.Telemetry
	}
	return ""
}
