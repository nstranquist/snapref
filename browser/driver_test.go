package browser

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestDriverTimeoutDerivesFromCtxDeadline(t *testing.T) {
	// No deadline → default.
	if got := driverTimeout(context.Background()); got != driverDefaultTimeout {
		t.Fatalf("no-deadline = %v, want %v", got, driverDefaultTimeout)
	}
	// A deadline FURTHER OUT than the default → extends past 30s.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if got := driverTimeout(ctx); got <= driverDefaultTimeout {
		t.Fatalf("90s deadline should extend past default, got %v", got)
	}
	// A short deadline → shortens.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if got := driverTimeout(ctx2); got > 3*time.Second {
		t.Fatalf("2s deadline should shorten, got %v", got)
	}
}

func TestRemoteDriverRenewsLeaseAfterOperation(t *testing.T) {
	setBrowserHome(t)
	started := time.Now().UTC().Add(-time.Hour)
	if err := SaveSession(&Session{
		Name: "driver-lease", PID: os.Getpid(), StartedAt: started,
		LastActivityAt: started, IdleTimeout: (10 * time.Minute).String(),
		LifecyclePolicy: LifecycleIdle,
	}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("operation failed")
	_, err := withRemoteSessionResult(remoteDriver{}, "driver-lease", func(*Session) (struct{}, error) {
		during, loadErr := LoadSession("driver-lease")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		time.Sleep(2 * time.Millisecond)
		started = during.LastActivityAt
		return struct{}{}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("operation error = %v", err)
	}
	after, err := LoadSession("driver-lease")
	if err != nil {
		t.Fatal(err)
	}
	if !after.LastActivityAt.After(started) {
		t.Fatalf("lease was not renewed after operation: during=%s after=%s", started, after.LastActivityAt)
	}
}
