package browser

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const sessionLockPollInterval = 50 * time.Millisecond

var sessionLocalLocks sync.Map

type sessionLocalLock struct {
	permit chan struct{}
}

func newSessionLocalLock() *sessionLocalLock {
	lock := &sessionLocalLock{permit: make(chan struct{}, 1)}
	lock.permit <- struct{}{}
	return lock
}

type sessionLockInfo struct {
	PID        int       `json:"pid"`
	Token      string    `json:"token"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// acquireSessionLifecycleLock serializes open/close for one session across
// goroutines and processes. The local permit is deadline-aware; the file lock
// is kernel-owned, so process exit releases it without stale-file reclamation
// or an unlink/replacement race. The persistent file contains diagnostics only.
func acquireSessionLifecycleLock(name string, timeout time.Duration) (func(), error) {
	if err := ValidateSessionName(name); err != nil {
		return nil, err
	}
	path, err := SessionPath(name)
	if err != nil {
		return nil, err
	}
	lockPath := path + ".lifecycle.lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create session lock directory: %w", err)
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)

	key, err := filepath.Abs(lockPath)
	if err != nil {
		return nil, fmt.Errorf("resolve session lock: %w", err)
	}
	value, _ := sessionLocalLocks.LoadOrStore(key, newSessionLocalLock())
	local, ok := value.(*sessionLocalLock)
	if !ok {
		return nil, fmt.Errorf("session lock %s has unexpected local state %T", lockPath, value)
	}
	if !acquireLocalSessionLock(local, deadline) {
		return nil, sessionLifecycleBusyError(name, lockPath)
	}
	releaseLocal := func() { local.permit <- struct{}{} }

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		releaseLocal()
		return nil, fmt.Errorf("open session lock: %w", err)
	}
	if err := os.Chmod(lockPath, 0o600); err != nil {
		_ = file.Close()
		releaseLocal()
		return nil, fmt.Errorf("secure session lock: %w", err)
	}

	for {
		locked, lockErr := tryLockSessionFile(file)
		if lockErr != nil {
			_ = file.Close()
			releaseLocal()
			return nil, fmt.Errorf("lock session lifecycle: %w", lockErr)
		}
		if locked {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = file.Close()
			releaseLocal()
			return nil, sessionLifecycleBusyError(name, lockPath)
		}
		if remaining > sessionLockPollInterval {
			remaining = sessionLockPollInterval
		}
		time.Sleep(remaining)
	}

	info, err := newSessionLockInfo()
	if err != nil {
		unlockSessionFile(file)
		_ = file.Close()
		releaseLocal()
		return nil, err
	}
	if err := writeSessionLockInfo(file, info); err != nil {
		unlockSessionFile(file)
		_ = file.Close()
		releaseLocal()
		return nil, err
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			unlockSessionFile(file)
			_ = file.Close()
			releaseLocal()
		})
	}, nil
}

func acquireLocalSessionLock(lock *sessionLocalLock, deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-lock.permit:
		return true
	case <-timer.C:
		return false
	}
}

func sessionLifecycleBusyError(name, path string) error {
	return fmt.Errorf("session %q lifecycle is busy; another open/close operation still holds %s", name, path)
}

func newSessionLockInfo() (sessionLockInfo, error) {
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return sessionLockInfo{}, fmt.Errorf("generate session lock token: %w", err)
	}
	return sessionLockInfo{
		PID:        os.Getpid(),
		Token:      hex.EncodeToString(tokenBytes[:]),
		AcquiredAt: time.Now().UTC(),
	}, nil
}

func writeSessionLockInfo(file *os.File, info sessionLockInfo) error {
	body, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("encode session lock: %w", err)
	}
	body = append(body, '\n')
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate session lock: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek session lock: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("write session lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync session lock: %w", err)
	}
	return nil
}
