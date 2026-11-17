package browser

import "strings"

// AdmissionRuntimeKind predicts which runtime a launch request will use, for
// host admission only. It follows ResolveRuntime's auto choice without
// verifying the installed binary, so a session-pressure gate can price the
// session (headless-shell about 290 MB, Chrome for Testing about 928 MB on
// the M4 runtime-cost receipt) before the launch. An external browser is
// priced as Chrome for Testing. Any error falls back to Chrome for Testing,
// the conservative cost.
func AdmissionRuntimeKind(request RuntimeRequest) RuntimeKind {
	kind := request.Kind
	if kind == "" {
		kind = RuntimeAuto
	}
	switch kind {
	case RuntimeHeadlessShell, RuntimeChromeForTesting:
		return kind
	case RuntimeAuto:
	default:
		return RuntimeChromeForTesting
	}
	if strings.TrimSpace(request.ExternalPath) != "" || request.Headful || request.ExtensionPath != "" {
		return RuntimeChromeForTesting
	}
	if path, _ := chromeOverride(); path != "" {
		return RuntimeChromeForTesting
	}
	platform, err := runtimePlatform()
	if err != nil {
		return RuntimeChromeForTesting
	}
	selections, err := loadRuntimeSelections()
	if err != nil || !headlessShellAccepted(platform, selections) {
		return RuntimeChromeForTesting
	}
	return RuntimeHeadlessShell
}
