//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package processtree

import (
	"errors"
	"os"
	"os/exec"
)

// Non-POSIX platforms retain os/exec's direct-child cancellation. WaitDelay
// still guarantees that inherited output descriptors cannot block forever.
func configurePlatform(_ *exec.Cmd) {}

func killPlatform(cmd *exec.Cmd) error {
	if err := cmd.Process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func platformCancellationScope() CancellationScope {
	return CancellationScopeDirectChild
}
