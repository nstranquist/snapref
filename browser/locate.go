// Package browser is SnapRef's Go-native CDP engine for agent-driven browser
// work. It drives a separate Chromium process for scriptable, scheduled, and
// headless flows without taking over the user's daily browser profile.
//
// Stateless commands launch and tear down their own Chromium. Named
// sessions persist an isolated profile and CDP endpoint for follow-up
// `snapref exec` calls.
package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LocateResult is what `snapref doctor` returns.
type LocateResult struct {
	SchemaVersion                int                 `json:"schema_version"`
	RequestedRuntime             RuntimeKind         `json:"requested_runtime"`
	ResolvedRuntime              RuntimeKind         `json:"resolved_runtime,omitempty"`
	Managed                      bool                `json:"managed"`
	Path                         string              `json:"path,omitempty"`
	Version                      string              `json:"version,omitempty"`
	Source                       string              `json:"source"`
	Error                        string              `json:"error,omitempty"`
	SupportsExtensions           bool                `json:"supports_extensions"`
	ExtensionLoadingNote         string              `json:"extension_loading_note,omitempty"`
	Capabilities                 RuntimeCapabilities `json:"capabilities"`
	InstalledRuntimes            []RuntimeRecord     `json:"installed_runtimes"`
	PersonalBrowserCollisionRisk bool                `json:"personal_browser_collision_risk"`
	Remediation                  string              `json:"remediation,omitempty"`
}

// ErrNoChrome is returned when no Chromium-class browser is found on the
// host. Callers print a clear remediation message.
var ErrNoChrome = errors.New("no Chromium-class browser found")

// LocateChrome is retained for explicit external-runtime compatibility only.
// It never searches PATH, application directories, or personal browsers.
// SNAPREF_CHROME wins; NDEV_BROWSER_CHROME remains a legacy adapter alias.
func LocateChrome() (string, error) {
	if env, label := chromeOverride(); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("%s=%q: %w", label, env, err)
		}
		return env, nil
	}
	return "", fmt.Errorf("%w: explicit SNAPREF_CHROME or NDEV_BROWSER_CHROME override is required", ErrNoChrome)
}

func chromeOverride() (path, label string) {
	if path = strings.TrimSpace(os.Getenv("SNAPREF_CHROME")); path != "" {
		return path, "SNAPREF_CHROME"
	}
	if path = strings.TrimSpace(os.Getenv("NDEV_BROWSER_CHROME")); path != "" {
		return path, "NDEV_BROWSER_CHROME"
	}
	return "", ""
}

// Doctor runs a setup probe: locate the binary + read its version. Used
// by `snapref doctor` and as a precondition for every other verb.
func Doctor() LocateResult {
	return DoctorRuntime(RuntimeAuto)
}

// DoctorRuntime diagnoses one runtime request without downloading or mutating
// runtime state.
func DoctorRuntime(requested RuntimeKind) LocateResult {
	if requested == "" {
		requested = RuntimeAuto
	}
	res := LocateResult{SchemaVersion: 1, RequestedRuntime: requested, Source: "managed-selection", InstalledRuntimes: []RuntimeRecord{}}
	if _, label := chromeOverride(); label != "" {
		res.Source = "env:" + label
	}
	if records, err := ListRuntimeRecords(); err == nil {
		res.InstalledRuntimes = records
	}
	resolved, err := ResolveRuntime(RuntimeRequest{Kind: requested})
	if err != nil {
		res.Error = err.Error()
		if requested == RuntimeHeadlessShell || requested == RuntimeChromeForTesting {
			res.Remediation = runtimeInstallRemediation(requested)
		} else if strings.Contains(err.Error(), string(RuntimeChromeForTesting)) {
			res.Remediation = runtimeInstallRemediation(RuntimeChromeForTesting)
		}
		return res
	}
	res.ResolvedRuntime = resolved.Kind
	res.Managed = resolved.Managed
	res.Path = resolved.Path
	res.Version = resolved.Version
	res.Source = resolved.Source
	res.Capabilities = resolved.Capabilities
	res.SupportsExtensions = resolved.Capabilities.Extensions
	res.PersonalBrowserCollisionRisk = resolved.Kind == RuntimeExternal && isCommonPersonalBrowserPath(resolved.Path)
	if !res.SupportsExtensions {
		if resolved.Kind == RuntimeHeadlessShell {
			res.ExtensionLoadingNote = "Chrome Headless Shell does not support unpacked-extension workflows; use --runtime chrome-for-testing."
		} else {
			res.ExtensionLoadingNote = "this external browser does not support --load-extension; use managed Chrome for Testing or an explicitly compatible external Chromium build."
		}
	}
	return res
}

func isCommonPersonalBrowserPath(path string) bool {
	clean := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	for _, marker := range []string{
		"/google chrome.app/", "/google chrome canary.app/", "/chromium.app/",
		"/google/chrome/application/", "/chromium/application/",
		"/microsoft/edge/application/", "/bravesoftware/brave-browser/application/",
		"/usr/bin/google-chrome", "/usr/bin/chromium", "/snap/bin/chromium",
	} {
		if strings.Contains(clean, marker) {
			return true
		}
	}
	return false
}

// chromeSupportsLoadExtension returns true if the given chrome version
// banner indicates a build that honors --load-extension. Stable Google
// Chrome refuses; Chromium / Chrome for Testing / Canary / Beta / Dev allow it.
func chromeSupportsLoadExtension(version string) bool {
	v := strings.ToLower(version)
	if strings.Contains(v, "chromium") {
		return true
	}
	if strings.Contains(v, "chrome for testing") {
		return true
	}
	if strings.Contains(v, "canary") || strings.Contains(v, "beta") || strings.Contains(v, "dev") {
		return true
	}
	// Plain "Google Chrome 147.x" → stable. Refuses.
	return false
}

func chromeVersion(path string) (string, error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
