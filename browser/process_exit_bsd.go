//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package browser

import (
	"errors"

	"golang.org/x/sys/unix"
)

// watchProcessExit returns a channel closed when pid exits. It registers a
// kqueue EVFILT_PROC NOTE_EXIT filter, so the watcher sleeps in the kernel
// and wakes the moment the browser exits instead of at its next lease poll.
// A pid that is already gone closes the channel at once. stop releases the
// kqueue; it is safe to call more than once.
func watchProcessExit(pid int) (<-chan struct{}, func()) {
	exited := make(chan struct{})
	kq, err := unix.Kqueue()
	if err != nil {
		return pollProcessExit(pid)
	}
	change := unix.Kevent_t{}
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(kq)
		if errors.Is(err, unix.ESRCH) {
			close(exited)
			return exited, func() {}
		}
		return pollProcessExit(pid)
	}
	stopped := make(chan struct{})
	go func() {
		defer unix.Close(kq)
		events := make([]unix.Kevent_t, 1)
		timeout := unix.NsecToTimespec(int64(processExitStopCheck))
		for {
			select {
			case <-stopped:
				return
			default:
			}
			n, err := unix.Kevent(kq, nil, events, &timeout)
			if err != nil && !errors.Is(err, unix.EINTR) {
				return
			}
			if n > 0 {
				close(exited)
				return
			}
		}
	}()
	return exited, onceFunc(func() { close(stopped) })
}
