package adb

import "errors"

// serverProcesses: on Windows Pinky tells no server its own (there is no
// Local Network privacy to lose there), so it stops none.
func serverProcesses(port string) ([]process, error) { return nil, errors.ErrUnsupported }

func stopProcess(pid int) error { return errors.ErrUnsupported }
