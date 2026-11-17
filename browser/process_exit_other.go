//go:build !(darwin || freebsd || netbsd || openbsd || dragonfly)

package browser

func watchProcessExit(pid int) (<-chan struct{}, func()) { return pollProcessExit(pid) }
