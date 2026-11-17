package main

import (
	"testing"
	"time"
)

func TestCPUTimeParsesPsFormats(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:01.50":    1500 * time.Millisecond,
		"2:03.00":    123 * time.Second,
		"1:00:00.00": time.Hour,
		"1-00:00:01": 24*time.Hour + time.Second,
	} {
		if got := cputime(in); got != want {
			t.Errorf("cputime(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestWindowCountsNewProcessesFromZero(t *testing.T) {
	a := []proc{{PID: 1, RSSKB: 1024, CPU: time.Second}}
	b := []proc{{PID: 1, RSSKB: 2048, CPU: 2 * time.Second}, {PID: 2, RSSKB: 1024, CPU: time.Second}}
	s := window(a, b, 4*time.Second)
	if s.Procs != 2 || s.RSSMB != 3 || s.CPUPct != 50 {
		t.Fatalf("window = %+v, want 2 procs, 3 MB, 50%%", s)
	}
}

func TestSessionTreeFollowsDescendantsOnly(t *testing.T) {
	// sessionTree reads ps; check the marker and descendant rule on a
	// synthetic list through the same helper logic.
	all := []proc{{PID: 10, PPID: 1, Command: "daemon --session snapcost-x"}, {PID: 11, PPID: 10, Command: "chrome"}, {PID: 12, PPID: 1, Command: "chrome other"}}
	got := treeOf(all, map[int]bool{}, []string{"snapcost-x"})
	if len(got) != 2 {
		t.Fatalf("tree = %+v, want the daemon and its child", got)
	}
}
