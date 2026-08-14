//go:build darwin

package browser

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type runtimeFocusSnapshot struct {
	Frontmost    string
	FrontmostPID int
}

func runtimeFocusRequired() bool        { return true }
func runtimeFocusNotApplicable() string { return "fail" }

func runtimeFocusState() (runtimeFocusSnapshot, error) {
	out, err := exec.Command("osascript", "-e", `tell application "System Events"
set frontProcess to first application process whose frontmost is true
return (bundle identifier of frontProcess as text) & "|" & (unix id of frontProcess as text)
end tell`).CombinedOutput()
	if err != nil {
		return runtimeFocusSnapshot{}, fmt.Errorf("query frontmost application: %w: %s", err, strings.TrimSpace(string(out)))
	}
	frontmost, pidRaw, ok := strings.Cut(strings.TrimSpace(string(out)), "|")
	pid, parseErr := strconv.Atoi(pidRaw)
	if !ok || frontmost == "" || parseErr != nil || pid <= 0 {
		return runtimeFocusSnapshot{}, fmt.Errorf("frontmost application identity is empty")
	}
	return runtimeFocusSnapshot{Frontmost: frontmost, FrontmostPID: pid}, nil
}

func runtimeApplicationProcesses() (map[int]string, error) {
	out, err := exec.Command("osascript", "-e", `tell application "System Events"
set processRows to {}
repeat with currentProcess in every application process
try
set currentBundle to bundle identifier of currentProcess as text
set currentPID to unix id of currentProcess as text
set end of processRows to currentPID & "|" & currentBundle
end try
end repeat
set AppleScript's text item delimiters to linefeed
return processRows as text
end tell`).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("query application identities: %w: %s", err, strings.TrimSpace(string(out)))
	}
	applications := make(map[int]string)
	for _, row := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pidRaw, identity, ok := strings.Cut(strings.TrimSpace(row), "|")
		pid, parseErr := strconv.Atoi(pidRaw)
		if !ok || parseErr != nil || pid <= 0 || identity == "" {
			continue
		}
		applications[pid] = identity
	}
	return applications, nil
}

func (runtimeFocusSnapshot) VisibleBrowserWindows(pids []int) (int, error) {
	if len(pids) == 0 {
		return 0, nil
	}
	items := make([]string, 0, len(pids))
	for _, pid := range pids {
		items = append(items, strconv.Itoa(pid))
	}
	script := fmt.Sprintf(`tell application "System Events"
set targetPIDs to {%s}
set visibleCount to 0
repeat with targetPID in targetPIDs
set currentPID to contents of targetPID
set matches to every application process whose unix id is currentPID
repeat with targetProcess in matches
set visibleCount to visibleCount + (count of every window of targetProcess)
end repeat
end repeat
return visibleCount
end tell`, strings.Join(items, ","))
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("query visible browser windows: %w: %s", err, strings.TrimSpace(string(out)))
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, err
	}
	return count, nil
}
