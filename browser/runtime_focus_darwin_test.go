//go:build darwin

package browser

import (
	"os"
	"testing"
)

func TestRuntimeFocusProbeSupportsProcessTreePIDLists(t *testing.T) {
	snapshot, err := runtimeFocusState()
	if err != nil {
		t.Fatal(err)
	}
	windows, err := snapshot.VisibleBrowserWindows([]int{os.Getpid(), 999999})
	if err != nil {
		t.Fatal(err)
	}
	if windows != 0 {
		t.Fatalf("Go test process unexpectedly owns %d visible browser windows", windows)
	}
	if _, err := snapshot.VisibleBrowserWindows([]int{snapshot.FrontmostPID}); err != nil {
		t.Fatalf("frontmost application window query failed: %v", err)
	}
}
