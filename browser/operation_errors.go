package browser

import (
	"errors"
	"github.com/chromedp/cdproto/runtime"
)

// JavaScriptEvalError marks a caller expression that threw. Page text is never
// used to decide its class or its safe telemetry reason.
type JavaScriptEvalError struct{ Cause error }

func (e *JavaScriptEvalError) Error() string { return "JavaScript evaluation: " + e.Cause.Error() }
func (e *JavaScriptEvalError) Unwrap() error { return e.Cause }

// SelectOptionError marks a failed selection in the browser. Transport and
// deadline errors retain their original type.
type SelectOptionError struct{ Cause error }

func (e *SelectOptionError) Error() string { return "select option: " + e.Cause.Error() }
func (e *SelectOptionError) Unwrap() error { return e.Cause }

func evaluationError(err error) error {
	var exception *runtime.ExceptionDetails
	if errors.As(err, &exception) {
		return &JavaScriptEvalError{Cause: err}
	}
	return err
}

func selectionError(err error) error {
	var exception *runtime.ExceptionDetails
	if errors.As(err, &exception) {
		return &SelectOptionError{Cause: err}
	}
	return err
}

// ErrorReason returns a bounded code; it never includes JavaScript, URLs,
// selectors, stderr, or page-provided exception text.
func ErrorReason(err error) string {
	if err == nil {
		return ""
	}
	var eval *JavaScriptEvalError
	if errors.As(err, &eval) {
		return "javascript_exception"
	}
	var selection *SelectOptionError
	if errors.As(err, &selection) {
		return "select_option_failed"
	}
	return ErrorClassReason(ClassifyError(err))
}

func ErrorClassReason(class ErrorClass) string {
	switch class {
	case ErrorClassUsage:
		return "invalid_input"
	case ErrorClassPolicy:
		return "policy_denied"
	case ErrorClassAssertion:
		return "gate_not_satisfied"
	case ErrorClassSelector:
		return "selector_failed"
	case ErrorClassTransport:
		return "transport_failed"
	case ErrorClassTimeout:
		return "deadline_exceeded"
	case ErrorClassProduct:
		return "product_failed"
	case ErrorClassInternal:
		return "command_failure"
	default:
		return ""
	}
}
