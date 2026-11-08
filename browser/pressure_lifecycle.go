package browser

import "time"

// CloseSessionGeneration keeps a pressure watchdog from closing a replacement
// session which reused the same name after its old Chromium process exited.
func CloseSessionGeneration(name string, expectedPID int) error {
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := LoadSession(name)
	if err != nil {
		return err
	}
	if s.PID != expectedPID {
		return nil
	}
	return closeLocked(name, false)
}
