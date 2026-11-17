package browser

import (
	"sync"
	"time"
)

// processExitStopCheck bounds how long a stopped exit watcher lingers and is
// the poll period where no kernel exit notification exists.
const processExitStopCheck = time.Second

func onceFunc(f func()) func() {
	var once sync.Once
	return func() { once.Do(f) }
}

// pollProcessExit is the portable fallback: it checks liveness once per
// second, so a closed browser frees its watcher within about one second.
func pollProcessExit(pid int) (<-chan struct{}, func()) {
	exited := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		ticker := time.NewTicker(processExitStopCheck)
		defer ticker.Stop()
		for {
			if !processAlive(pid) {
				close(exited)
				return
			}
			select {
			case <-stopped:
				return
			case <-ticker.C:
			}
		}
	}()
	return exited, onceFunc(func() { close(stopped) })
}
