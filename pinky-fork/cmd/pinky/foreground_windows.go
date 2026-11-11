package main

import "syscall"

// allowForeground lets the running app bring its window to the front when
// asked by this launch, which the person just started: Windows keeps
// background processes from taking the foreground.
func allowForeground() {
	const asfwAny = 0xFFFFFFFF // ASFW_ANY, (DWORD)-1
	proc := syscall.NewLazyDLL("user32.dll").NewProc("AllowSetForegroundWindow")
	if proc.Find() == nil {
		proc.Call(asfwAny)
	}
}
