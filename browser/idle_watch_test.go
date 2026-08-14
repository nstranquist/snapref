package browser

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestIdleWatchPollIntervalIsBounded(t *testing.T) {
	for _, tt := range []struct {
		idle time.Duration
		want time.Duration
	}{
		{idle: time.Second, want: time.Second},
		{idle: 40 * time.Second, want: 10 * time.Second},
		{idle: 10 * time.Minute, want: time.Minute},
	} {
		if got := IdleWatchPollInterval(tt.idle); got != tt.want {
			t.Errorf("IdleWatchPollInterval(%s) = %s, want %s", tt.idle, got, tt.want)
		}
	}
}

func TestValidateManagedIdleTimeoutProtectsNormalOperations(t *testing.T) {
	if err := ValidateManagedIdleTimeout(30 * time.Second); err == nil {
		t.Fatal("30s managed lease accepted despite 30s default operation budget")
	}
	if err := ValidateManagedIdleTimeout(time.Minute); err != nil {
		t.Fatalf("one-minute managed lease rejected: %v", err)
	}
}

func TestWatchIdleSessionStopsForMissingGeneration(t *testing.T) {
	setBrowserHome(t)
	result, err := WatchIdleSession(context.Background(), IdleWatchOptions{
		Name:        "missing-watch",
		ExpectedPID: os.Getpid(),
		IdleTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (IdleExpiryResult{Done: true, Reason: IdleExpiryMissing}) {
		t.Fatalf("result = %+v", result)
	}
}

func TestWatchIdleSessionHonorsCancellation(t *testing.T) {
	setBrowserHome(t)
	now := time.Now().UTC()
	if err := SaveSession(&Session{
		Name: "active-watch", PID: os.Getpid(), StartedAt: now,
		LastActivityAt: now, IdleTimeout: time.Minute.String(),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WatchIdleSession(ctx, IdleWatchOptions{
		Name: "active-watch", ExpectedPID: os.Getpid(), IdleTimeout: time.Minute,
		PollInterval: time.Millisecond,
	})
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
