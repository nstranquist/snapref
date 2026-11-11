package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CheckForegroundPolicy checks the durable host policy before launching or
// connecting to a headed browser. Headless operations do no filesystem work.
// A user-wide marker also covers callers that choose a different session root.
func CheckForegroundPolicy(headful bool) error {
	if !headful {
		return nil
	}
	root, err := SessionRoot()
	if err != nil {
		return fmt.Errorf("read foreground policy: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read foreground policy: %w", err)
	}
	for _, marker := range []string{filepath.Join(home, ".config", "snapref", "headless-only"), filepath.Join(root, "headless-only")} {
		_, err = os.Stat(marker)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read foreground policy: %w", err)
		}
		return errors.New("headful browser disabled by host policy (headless-only); use a headless session")
	}
	return nil
}

func checkSessionForegroundPolicy(s *Session) error {
	if s == nil {
		return errors.New("missing browser session")
	}
	return CheckForegroundPolicy(s.Headful)
}
