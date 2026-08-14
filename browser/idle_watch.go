package browser

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MinimumManagedIdleTimeout keeps a normal 30-second browser operation from
// racing its own watchdog. Core expiry tests may use shorter durations, but
// user-facing managed-session adapters should enforce this floor.
const MinimumManagedIdleTimeout = time.Minute

// ValidateManagedIdleTimeout applies the shared CLI lease floor.
func ValidateManagedIdleTimeout(idleTimeout time.Duration) error {
	if idleTimeout < MinimumManagedIdleTimeout {
		return fmt.Errorf("idle timeout must be at least %s (use explicit --keep-alive to disable expiry)", MinimumManagedIdleTimeout)
	}
	return nil
}

// IdleWatchOptions identifies one saved process generation and its lease.
// ExpectedPID prevents a stale watchdog from touching a newer session that
// reused the same name.
type IdleWatchOptions struct {
	Name         string
	ExpectedPID  int
	IdleTimeout  time.Duration
	PurgeProfile bool
	PollInterval time.Duration
}

// IdleWatchPollInterval bounds the wake-up frequency of detached watchdogs.
// They use no busy loop and wake at most once per second for very short test
// leases and at most once per minute for normal leases.
func IdleWatchPollInterval(idleTimeout time.Duration) time.Duration {
	poll := idleTimeout / 4
	if poll > time.Minute {
		poll = time.Minute
	}
	if poll < time.Second {
		poll = time.Second
	}
	return poll
}

// WatchIdleSession blocks until this session generation is missing, replaced,
// dead, unowned, expired, or the context is canceled. Expiry checks remain
// atomic inside ExpireIdleSession's cross-process lifecycle lock.
func WatchIdleSession(ctx context.Context, opts IdleWatchOptions) (IdleExpiryResult, error) {
	if ctx == nil {
		return IdleExpiryResult{}, errors.New("watch context is nil")
	}
	if err := ValidateSessionName(opts.Name); err != nil {
		return IdleExpiryResult{}, err
	}
	if opts.ExpectedPID <= 0 {
		return IdleExpiryResult{}, errors.New("expected PID must be positive")
	}
	if opts.IdleTimeout <= 0 {
		return IdleExpiryResult{}, errors.New("idle timeout must be positive")
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = IdleWatchPollInterval(opts.IdleTimeout)
	}
	for {
		result, err := ExpireIdleSession(
			opts.Name,
			opts.ExpectedPID,
			opts.IdleTimeout,
			opts.PurgeProfile,
			time.Now().UTC(),
		)
		if err != nil || result.Done {
			return result, err
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return IdleExpiryResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}
