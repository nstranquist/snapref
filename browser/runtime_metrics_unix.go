//go:build !windows

package browser

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func runtimeProcessTreeRSS(rootPID int) (int64, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=").Output()
	if err != nil {
		return 0, err
	}
	type row struct {
		pid, ppid int
		rss       int64
	}
	var rows []row
	children := map[int][]int{}
	rssByPID := map[int]int64{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		rssKB, rssErr := strconv.ParseInt(fields[2], 10, 64)
		if pidErr != nil || ppidErr != nil || rssErr != nil {
			continue
		}
		rows = append(rows, row{pid: pid, ppid: ppid, rss: rssKB * 1024})
	}
	for _, item := range rows {
		children[item.ppid] = append(children[item.ppid], item.pid)
		rssByPID[item.pid] = item.rss
	}
	seen := map[int]bool{}
	queue := []int{rootPID}
	var total int64
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		total += rssByPID[pid]
		queue = append(queue, children[pid]...)
	}
	if total <= 0 {
		return 0, fmt.Errorf("no RSS evidence for browser pid %d", rootPID)
	}
	return total, nil
}

func runtimeProcessTreePIDs(rootPID int) ([]int, error) {
	if rootPID <= 0 {
		return nil, fmt.Errorf("invalid process-tree root pid %d", rootPID)
	}
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	children := map[int][]int{}
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil {
			continue
		}
		if pid == rootPID {
			found = true
		}
		children[ppid] = append(children[ppid], pid)
	}
	if !found {
		return nil, fmt.Errorf("process-tree root pid %d is not running", rootPID)
	}
	seen := map[int]bool{}
	queue := []int{rootPID}
	var pids []int
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
		queue = append(queue, children[pid]...)
	}
	return pids, nil
}
