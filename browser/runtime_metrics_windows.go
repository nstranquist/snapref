//go:build windows

package browser

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func runtimeProcessTreeRSS(rootPID int) (int64, error) {
	pids, err := runtimeProcessTreePIDs(rootPID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, pid := range pids {
		rss, err := windowsProcessRSS(uint32(pid))
		if err != nil {
			return 0, fmt.Errorf("read RSS for pid %d: %w", pid, err)
		}
		total += rss
	}
	if total <= 0 {
		return 0, fmt.Errorf("process tree rooted at pid %d reported no RSS", rootPID)
	}
	return total, nil
}

func runtimeProcessTreePIDs(rootPID int) ([]int, error) {
	if rootPID <= 0 {
		return nil, fmt.Errorf("invalid process-tree root pid %d", rootPID)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshot process tree: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	children := map[uint32][]uint32{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("read process snapshot: %w", err)
	}
	found := false
	for {
		if entry.ProcessID == uint32(rootPID) {
			found = true
		}
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		err = windows.Process32Next(snapshot, &entry)
		if err == nil {
			continue
		}
		if err == syscall.ERROR_NO_MORE_FILES {
			break
		}
		return nil, fmt.Errorf("walk process snapshot: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("process-tree root pid %d is not running", rootPID)
	}

	queue := []uint32{uint32(rootPID)}
	seen := map[uint32]bool{}
	var pids []int
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, int(pid))
		queue = append(queue, children[pid]...)
	}
	return pids, nil
}

func windowsProcessRSS(pid uint32) (int64, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)
	counters := processMemoryCounters{CB: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	ok, _, callErr := getProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.CB),
	)
	if ok == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return 0, callErr
		}
		return 0, syscall.EINVAL
	}
	return int64(counters.WorkingSetSize), nil
}
