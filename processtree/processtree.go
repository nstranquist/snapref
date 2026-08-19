// Package processtree configures bounded subprocess cancellation.
//
// POSIX commands use a dedicated process group. Windows commands take a
// descendant snapshot and terminate the observed tree. Other platforms retain
// direct-child cancellation. Call PlatformCancellationScope when a caller must
// fail closed unless descendant cancellation is available.
package processtree

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CancellationScope describes the strongest cancellation behavior available
// on the current platform.
type CancellationScope string

const (
	// CancellationScopeDirectChild stops only the command's direct process.
	CancellationScopeDirectChild CancellationScope = "direct-child"
	// CancellationScopeProcessGroup stops the POSIX process group created for
	// the command. A descendant that deliberately leaves the group is outside
	// this boundary.
	CancellationScopeProcessGroup CancellationScope = "process-group"
	// CancellationScopeDescendantSnapshot stops the Windows descendants that
	// are observable when cancellation begins. It is a best-effort snapshot,
	// not a Windows Job Object ownership boundary.
	CancellationScopeDescendantSnapshot CancellationScope = "descendant-snapshot"
)

// DefaultWaitDelay bounds pipe cleanup after cancellation or after the direct
// child exits while a descendant still owns an inherited output descriptor.
const DefaultWaitDelay = 2 * time.Second

// CommandContext is exec.CommandContext with the strongest platform-supported
// cancellation scope and bounded output-pipe cleanup preconfigured.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configure(cmd, true)
	return cmd
}

// Configure prepares an existing command for bounded Run, Output, and
// CombinedOutput calls. It must be called before the command starts.
//
// When cmd was created by exec.CommandContext, Configure replaces its direct-
// child cancellation with the strongest platform-supported cancellation.
// Commands created by exec.Command remain startable; call Kill explicitly when
// their owner decides to cancel them.
func Configure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	configure(cmd, cmd.Cancel != nil)
}

func configure(cmd *exec.Cmd, contextCommand bool) {
	configurePlatform(cmd)
	if contextCommand {
		cmd.Cancel = func() error { return Kill(cmd) }
	}
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = DefaultWaitDelay
	}
}

// Kill stops cmd using the cancellation scope configured before start. A raw,
// unconfigured command receives direct-child cancellation. The caller must own
// the process. A nil, unstarted, or completed command returns
// os.ErrProcessDone. Platform implementations verify or stop the exact root
// process before they target a numeric process-group or descendant ID.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	return killPlatform(cmd)
}

// PlatformCancellationScope reports the current platform's cancellation
// boundary without starting a process.
func PlatformCancellationScope() CancellationScope {
	return platformCancellationScope()
}

// SupportsDescendantCancellation reports whether this platform has a process-
// group or descendant-snapshot implementation.
func SupportsDescendantCancellation() bool {
	return PlatformCancellationScope() != CancellationScopeDirectChild
}

var minimalEnvironmentNames = map[string]struct{}{
	"COLORTERM":         {},
	"COMSPEC":           {},
	"FORCE_COLOR":       {},
	"LANG":              {},
	"LANGUAGE":          {},
	"LC_ADDRESS":        {},
	"LC_ALL":            {},
	"LC_COLLATE":        {},
	"LC_CTYPE":          {},
	"LC_IDENTIFICATION": {},
	"LC_MEASUREMENT":    {},
	"LC_MESSAGES":       {},
	"LC_MONETARY":       {},
	"LC_NAME":           {},
	"LC_NUMERIC":        {},
	"LC_PAPER":          {},
	"LC_TELEPHONE":      {},
	"LC_TIME":           {},
	"NO_COLOR":          {},
	"PATH":              {},
	"PATHEXT":           {},
	"SYSTEMROOT":        {},
	"TEMP":              {},
	"TERM":              {},
	"TMP":               {},
	"TMPDIR":            {},
	"TZ":                {},
	"WINDIR":            {},
}

// MinimalEnvironment returns a fresh, fail-closed environment containing only
// execution essentials and explicitly allowed names. Name matching is case-
// insensitive so the same policy is safe on Windows. Standard locale variable
// names are listed explicitly; an arbitrary LC_ prefix does not grant access.
//
// This function limits environment authority only. It does not remove
// credentials from arguments, the filesystem, the working directory, open
// handles, or network identity.
func MinimalEnvironment(ambient []string, additionalNames ...string) []string {
	allowed := make(map[string]struct{}, len(minimalEnvironmentNames)+len(additionalNames))
	for name := range minimalEnvironmentNames {
		allowed[name] = struct{}{}
	}
	for _, name := range additionalNames {
		normalized := normalizeEnvironmentName(name)
		if normalized != "" && !strings.Contains(normalized, "=") {
			allowed[normalized] = struct{}{}
		}
	}

	result := make([]string, 0, len(ambient))
	for _, pair := range ambient {
		name, _, ok := strings.Cut(pair, "=")
		normalized := normalizeEnvironmentName(name)
		if !ok || normalized == "" {
			continue
		}
		if _, ok := allowed[normalized]; !ok {
			continue
		}
		result = append(result, pair)
	}
	return result
}

// CurrentMinimalEnvironment is the process-local convenience form.
func CurrentMinimalEnvironment(additionalNames ...string) []string {
	return MinimalEnvironment(os.Environ(), additionalNames...)
}

// CredentialFreeEnvironment is retained for source compatibility.
// Deprecated: use MinimalEnvironment. The returned environment is now a
// fail-closed allowlist rather than a credential-name denylist.
func CredentialFreeEnvironment(ambient []string) []string {
	return MinimalEnvironment(ambient)
}

// CurrentCredentialFreeEnvironment is retained for source compatibility.
// Deprecated: use CurrentMinimalEnvironment.
func CurrentCredentialFreeEnvironment() []string {
	return CurrentMinimalEnvironment()
}

func normalizeEnvironmentName(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}
