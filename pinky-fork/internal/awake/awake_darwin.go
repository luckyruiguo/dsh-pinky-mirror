package awake

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// On a Mac, Hold creates an IOKit assertion of type
// PreventUserIdleSystemSleep named after the reason, as `caffeinate -i`
// does and as the app's NSProcessInfo activity does: pmset -g assertions
// lists it under that name.

const (
	kCFStringEncodingUTF8 = 0x08000100
	kIOPMAssertionLevelOn = 255
	preventIdleSleep      = "PreventUserIdleSystemSleep" // kIOPMAssertionTypePreventUserIdleSystemSleep
)

// iokit holds the CoreFoundation and IOKit functions Hold uses, loaded
// once.
type iokit struct {
	CFStringCreateWithBytes     func(alloc uintptr, b *byte, n int, encoding uint32, external bool) uintptr
	CFRelease                   func(cf uintptr)
	IOPMAssertionCreateWithName func(kind uintptr, level uint32, name uintptr, id *uint32) int32
	IOPMAssertionRelease        func(id uint32) int32
}

var loadIOKit = sync.OnceValues(func() (*iokit, error) {
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("awake: load CoreFoundation: %w", err)
	}
	io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("awake: load IOKit: %w", err)
	}
	k := &iokit{}
	for _, f := range []struct {
		lib  uintptr
		fptr any
		name string
	}{
		{cf, &k.CFStringCreateWithBytes, "CFStringCreateWithBytes"},
		{cf, &k.CFRelease, "CFRelease"},
		{io, &k.IOPMAssertionCreateWithName, "IOPMAssertionCreateWithName"},
		{io, &k.IOPMAssertionRelease, "IOPMAssertionRelease"},
	} {
		sym, err := purego.Dlsym(f.lib, f.name)
		if err != nil || sym == 0 {
			return nil, fmt.Errorf("awake: %s is missing: %v", f.name, err)
		}
		purego.RegisterFunc(f.fptr, sym)
	}
	return k, nil
})

// str makes a CFString, which the caller releases.
func (k *iokit) str(s string) uintptr {
	b := []byte(s)
	var p *byte
	if len(b) > 0 {
		p = unsafe.SliceData(b)
	}
	return k.CFStringCreateWithBytes(0, p, len(b), kCFStringEncodingUTF8, false)
}

func hold(reason string) (func(), error) {
	k, err := loadIOKit()
	if err != nil {
		return nil, err
	}
	kind := k.str(preventIdleSleep)
	if kind == 0 {
		return nil, fmt.Errorf("awake: could not make the assertion's type")
	}
	defer k.CFRelease(kind)
	name := k.str(reason)
	if name == 0 {
		return nil, fmt.Errorf("awake: could not make the assertion's name")
	}
	defer k.CFRelease(name)
	var id uint32
	if r := k.IOPMAssertionCreateWithName(kind, kIOPMAssertionLevelOn, name, &id); r != 0 {
		return nil, fmt.Errorf("awake: IOPMAssertionCreateWithName: IOReturn 0x%08x", uint32(r))
	}
	return func() { k.IOPMAssertionRelease(id) }, nil
}
