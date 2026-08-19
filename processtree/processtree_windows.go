//go:build windows

package processtree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"syscall"
)

const windowsErrorInvalidParameter = syscall.Errno(87)

func configurePlatform(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

func killPlatform(cmd *exec.Cmd) error {
	var killErr error
	// Acquire the retained os.Process handle before reading any numeric PID.
	// WithHandle fails after Wait and keeps this exact process identity alive
	// throughout snapshot validation and termination.
	handleErr := cmd.Process.WithHandle(func(handle uintptr) {
		root := windowsProcessHandle{pid: uint32(cmd.Process.Pid), handle: syscall.Handle(handle)}
		if err := inspectWindowsProcess(root); err != nil {
			killErr = err
			return
		}
		killErr = killWindowsProcessTree(cmd)
	})
	if handleErr != nil {
		if errors.Is(handleErr, os.ErrProcessDone) || errors.Is(handleErr, syscall.EINVAL) {
			return os.ErrProcessDone
		}
		return handleErr
	}
	return killErr
}

func killWindowsProcessTree(cmd *exec.Cmd) error {
	rootPID := uint32(cmd.Process.Pid)
	descendantPIDs, snapshotErr := windowsDescendantPIDs(rootPID)

	// Hold process handles before stopping the root. A handle continues to name
	// the observed process even if its PID is reused before termination.
	descendants := make([]windowsProcessHandle, 0, len(descendantPIDs))
	var errs []error
	if snapshotErr != nil {
		errs = append(errs, fmt.Errorf("enumerate descendants of pid %d: %w", rootPID, snapshotErr))
	}
	for _, pid := range descendantPIDs {
		process, err := openWindowsProcess(pid)
		if err == nil {
			descendants = append(descendants, process)
			continue
		}
		if !errors.Is(err, os.ErrProcessDone) {
			errs = append(errs, err)
		}
	}

	// Re-snapshot after opening the handles. A PID that disappeared from the
	// root's tree may have exited and been reused between the first snapshot
	// and OpenProcess. Keep only handles whose PIDs are still descendants.
	validatedPIDs, validationErr := windowsDescendantPIDs(rootPID)
	if validationErr != nil {
		errs = append(errs, fmt.Errorf("validate descendants of pid %d: %w", rootPID, validationErr))
		for _, process := range descendants {
			_ = syscall.CloseHandle(process.handle)
		}
		descendants = nil
	} else {
		var discarded []windowsProcessHandle
		descendants, discarded = retainValidatedWindowsProcesses(descendants, validatedPIDs)
		for _, process := range discarded {
			_ = syscall.CloseHandle(process.handle)
		}
	}
	defer func() {
		for _, process := range descendants {
			_ = syscall.CloseHandle(process.handle)
		}
	}()

	rootErr := cmd.Process.Kill()
	if rootErr != nil {
		// Windows releases the process handle during Wait and can report EINVAL
		// instead of os.ErrProcessDone. In either form, no descendant handle is
		// used after the exact root handle is already done.
		if errors.Is(rootErr, os.ErrProcessDone) || errors.Is(rootErr, syscall.EINVAL) {
			return os.ErrProcessDone
		}
		errs = append(errs, fmt.Errorf("terminate root pid %d: %w", rootPID, rootErr))
		return errors.Join(errs...)
	}

	// The snapshot is breadth-first. Stop the root before walking the snapshot
	// backwards so it cannot intentionally create another renderer generation.
	for i := len(descendants) - 1; i >= 0; i-- {
		if err := terminateWindowsProcess(descendants[i]); err != nil && !errors.Is(err, os.ErrProcessDone) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func platformCancellationScope() CancellationScope {
	return CancellationScopeDescendantSnapshot
}

func windowsDescendantPIDs(rootPID uint32) ([]uint32, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snapshot)

	children := map[uint32][]uint32{}
	entrySize := uint32(reflect.TypeFor[syscall.ProcessEntry32]().Size())
	entry := syscall.ProcessEntry32{Size: entrySize}
	if err := syscall.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	for {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		entry.Size = entrySize
		err = syscall.Process32Next(snapshot, &entry)
		if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	queue := append([]uint32(nil), children[rootPID]...)
	result := make([]uint32, 0, len(queue))
	seen := map[uint32]bool{rootPID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if pid == 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		result = append(result, pid)
		queue = append(queue, children[pid]...)
	}
	return result, nil
}

type windowsProcessHandle struct {
	pid    uint32
	handle syscall.Handle
}

func retainValidatedWindowsProcesses(processes []windowsProcessHandle, validatedPIDs []uint32) ([]windowsProcessHandle, []windowsProcessHandle) {
	validated := make(map[uint32]bool, len(validatedPIDs))
	for _, pid := range validatedPIDs {
		validated[pid] = true
	}
	retained := make([]windowsProcessHandle, 0, len(processes))
	discarded := make([]windowsProcessHandle, 0, len(processes))
	for _, process := range processes {
		if validated[process.pid] {
			retained = append(retained, process)
		} else {
			discarded = append(discarded, process)
		}
	}
	return retained, discarded
}

func openWindowsProcess(pid uint32) (windowsProcessHandle, error) {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE|syscall.SYNCHRONIZE, false, pid)
	if err != nil {
		if errors.Is(err, windowsErrorInvalidParameter) {
			return windowsProcessHandle{}, os.ErrProcessDone
		}
		return windowsProcessHandle{}, fmt.Errorf("open pid %d for termination: %w", pid, err)
	}
	return windowsProcessHandle{pid: pid, handle: handle}, nil
}

func terminateWindowsProcess(process windowsProcessHandle) error {
	if err := inspectWindowsProcess(process); err != nil {
		return err
	}
	if err := syscall.TerminateProcess(process.handle, 1); err != nil {
		return fmt.Errorf("terminate pid %d: %w", process.pid, err)
	}
	return nil
}

func inspectWindowsProcess(process windowsProcessHandle) error {
	state, err := syscall.WaitForSingleObject(process.handle, 0)
	if err != nil {
		return fmt.Errorf("inspect pid %d before termination: %w", process.pid, err)
	}
	if state != uint32(syscall.WAIT_TIMEOUT) {
		return os.ErrProcessDone
	}
	return nil
}
