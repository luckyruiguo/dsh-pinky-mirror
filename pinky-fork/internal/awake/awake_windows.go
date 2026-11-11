package awake

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows, Hold makes a power request for the system (not the display)
// with the reason, as MyGo's Power.KeepAwake does for the app: powercfg
// /requests lists it, run by an administrator.

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procPowerCreateRequest = kernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest    = kernel32.NewProc("PowerSetRequest")
	procPowerClearRequest  = kernel32.NewProc("PowerClearRequest")
)

const (
	powerRequestContextSimpleString = 0x1 // POWER_REQUEST_CONTEXT_SIMPLE_STRING
	powerRequestSystemRequired      = 1   // PowerRequestSystemRequired
)

// reasonContext mirrors REASON_CONTEXT with a simple reason string.
type reasonContext struct {
	version, flags uint32
	reason         *uint16
	_              [2]uintptr // the rest of the union
}

func hold(reason string) (func(), error) {
	if err := procPowerCreateRequest.Find(); err != nil {
		return nil, fmt.Errorf("awake: %w (%v)", ErrUnsupported, err)
	}
	r, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return nil, fmt.Errorf("awake: reason: %w", err)
	}
	ctx := reasonContext{flags: powerRequestContextSimpleString, reason: r}
	h, _, callErr := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	runtime.KeepAlive(r)
	if h == 0 || h == ^uintptr(0) {
		return nil, fmt.Errorf("awake: PowerCreateRequest: %v", callErr)
	}
	if ok, _, callErr := procPowerSetRequest.Call(h, powerRequestSystemRequired); ok == 0 {
		windows.CloseHandle(windows.Handle(h))
		return nil, fmt.Errorf("awake: PowerSetRequest: %v", callErr)
	}
	return func() {
		procPowerClearRequest.Call(h, powerRequestSystemRequired)
		windows.CloseHandle(windows.Handle(h))
	}, nil
}
