//go:build windows

package browser

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// ConfigureDetachedProcess gives any SnapRef-owned long-lived child the
// same cross-platform console and signal isolation as Chromium itself.
func ConfigureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = detachAttr()
}

// detachAttr keeps Chromium independent from the short-lived CLI console
// process. CREATE_NEW_PROCESS_GROUP also gives the child an isolated console
// control group when a console is present.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}
