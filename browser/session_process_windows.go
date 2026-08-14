//go:build windows

package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const windowsProcessQueryAccess = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windowsProcessQueryAccess, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && state == uint32(windows.WAIT_TIMEOUT)
}

func sessionOwnsProcess(session *Session) (bool, error) {
	if session.PID <= 0 {
		return false, nil
	}
	if strings.TrimSpace(session.ProfileDir) == "" {
		return false, errors.New("session has no profile directory identity")
	}
	handle, err := windows.OpenProcess(windowsProcessQueryAccess, false, uint32(session.PID))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return false, fmt.Errorf("open pid %d: %w", session.PID, err)
	}
	defer windows.CloseHandle(handle)
	return windowsSessionOwnsHandle(session, handle)
}

func windowsSessionOwnsHandle(session *Session, handle windows.Handle) (bool, error) {
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return false, err
	}

	image, err := windowsProcessImage(handle)
	if err != nil {
		return false, fmt.Errorf("inspect pid %d image: %w", session.PID, err)
	}
	if session.ChromePath != "" && !strings.EqualFold(filepath.Clean(image), filepath.Clean(session.ChromePath)) {
		return false, nil
	}
	createdAt, err := windowsProcessStartedAt(handle)
	if err != nil {
		return false, fmt.Errorf("inspect pid %d creation time: %w", session.PID, err)
	}
	expectedStart := session.ProcessStartedAt
	startTolerance := 5 * time.Second
	if expectedStart.IsZero() {
		expectedStart = session.StartedAt
		startTolerance = 2 * time.Minute
	}
	if expectedStart.IsZero() || createdAt.Sub(expectedStart) > startTolerance || expectedStart.Sub(createdAt) > startTolerance {
		return false, nil
	}
	portBody, err := os.ReadFile(filepath.Join(session.ProfileDir, "DevToolsActivePort"))
	if err != nil {
		return false, nil
	}
	portLine, _, _ := strings.Cut(strings.TrimSpace(string(portBody)), "\n")
	port, err := strconv.Atoi(strings.TrimSpace(portLine))
	if err != nil || port != session.Port {
		return false, nil
	}
	return true, nil
}

func windowsProcessImage(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return string(utf16.Decode(buffer[:size])), nil
}

func windowsProcessStartedAt(handle windows.Handle) (time.Time, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, created.Nanoseconds()).UTC(), nil
}

func sessionSignalGracefulStop(session *Session, grace time.Duration) error {
	if session.PID <= 0 {
		return nil
	}
	handle, err := windows.OpenProcess(windowsProcessQueryAccess|windows.PROCESS_TERMINATE, false, uint32(session.PID))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle)
	owned, err := windowsSessionOwnsHandle(session, handle)
	if err != nil {
		return fmt.Errorf("refusing to signal pid %d because ownership could not be verified: %w", session.PID, err)
	}
	if !owned {
		return fmt.Errorf("refusing to signal pid %d because it is not the Chromium process for profile %s", session.PID, session.ProfileDir)
	}
	if grace > 0 && session.Port > 0 {
		closeTimeout := grace
		if closeTimeout > 2*time.Second {
			closeTimeout = 2 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		_ = requestBrowserClose(ctx, session)
		cancel()
		deadline := time.Now().Add(grace)
		for time.Now().Before(deadline) {
			state, waitErr := windows.WaitForSingleObject(handle, 0)
			if waitErr != nil {
				return fmt.Errorf("wait for pid %d during graceful close: %w", session.PID, waitErr)
			}
			if state != uint32(windows.WAIT_TIMEOUT) {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return fmt.Errorf("wait for pid %d before TerminateProcess: %w", session.PID, err)
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return nil
	}
	if err := terminateWindowsProcessTree(uint32(session.PID)); err != nil {
		return err
	}
	state, err = windows.WaitForSingleObject(handle, 5000)
	if err != nil {
		return fmt.Errorf("wait for pid %d after TerminateProcess: %w", session.PID, err)
	}
	if state == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("pid %d remained alive after TerminateProcess", session.PID)
	}
	return nil
}
