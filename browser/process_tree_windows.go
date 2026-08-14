//go:build windows

package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"

	"golang.org/x/sys/windows"
)

func killStartedProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := terminateWindowsProcessTree(uint32(cmd.Process.Pid)); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return errors.Join(err, killErr)
		}
		return err
	}
	return nil
}

func terminateWindowsProcessTree(rootPID uint32) error {
	descendants, err := windowsDescendantPIDs(rootPID)
	if err != nil {
		return fmt.Errorf("enumerate descendants of pid %d: %w", rootPID, err)
	}
	var errs []error
	if err := terminateWindowsPID(rootPID); err != nil {
		errs = append(errs, err)
	}
	// Children are returned breadth-first; terminate deepest descendants first
	// after stopping the root so no new renderer generation can be spawned.
	for i := len(descendants) - 1; i >= 0; i-- {
		if err := terminateWindowsPID(descendants[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func windowsDescendantPIDs(rootPID uint32) ([]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	children := map[uint32][]uint32{}
	entrySize := uint32(reflect.TypeFor[windows.ProcessEntry32]().Size())
	entry := windows.ProcessEntry32{Size: entrySize}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	for {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		entry.Size = entrySize
		err = windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
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

func terminateWindowsPID(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return fmt.Errorf("open pid %d for termination: %w", pid, err)
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return fmt.Errorf("inspect pid %d before termination: %w", pid, err)
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return nil
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("terminate pid %d: %w", pid, err)
	}
	state, err = windows.WaitForSingleObject(handle, 5000)
	if err != nil {
		return fmt.Errorf("wait for pid %d after termination: %w", pid, err)
	}
	if state == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("pid %d remained alive after termination", pid)
	}
	return nil
}
