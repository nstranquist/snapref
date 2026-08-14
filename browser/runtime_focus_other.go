//go:build !darwin

package browser

type runtimeFocusSnapshot struct {
	Frontmost    string
	FrontmostPID int
}

func runtimeFocusRequired() bool                                      { return false }
func runtimeFocusNotApplicable() string                               { return "not-applicable" }
func runtimeFocusState() (runtimeFocusSnapshot, error)                { return runtimeFocusSnapshot{}, nil }
func runtimeApplicationProcesses() (map[int]string, error)            { return map[int]string{}, nil }
func (runtimeFocusSnapshot) VisibleBrowserWindows([]int) (int, error) { return 0, nil }
