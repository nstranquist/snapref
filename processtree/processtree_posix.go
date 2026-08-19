//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package processtree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func configurePlatform(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
}

func killPlatform(cmd *exec.Cmd) error {
	pid := cmd.Process.Pid
	groupConfigured := cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid && cmd.SysProcAttr.Pgid == 0

	// Process.Kill is identity-aware: Go serializes it with Wait and uses a
	// pidfd where the platform supports one. Do not use a numeric group ID
	// unless stopping that exact root succeeded.
	if err := cmd.Process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
	if !groupConfigured {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate dedicated process group %d: %w", pid, err)
	}
	return nil
}

func platformCancellationScope() CancellationScope {
	return CancellationScopeProcessGroup
}
