//go:build !windows

package browser

import (
	"os/exec"
	"syscall"
)

// ConfigureDetachedProcess gives any PageSkein-owned long-lived child the
// same cross-platform console and signal isolation as Chromium itself.
func ConfigureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = detachAttr()
}

// detachAttr starts a new session so terminal closure and CLI process-group
// signals do not tear down a long-lived Chromium or watchdog child.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
