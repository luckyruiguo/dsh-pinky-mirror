//go:build darwin

package secret

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// LocalAuthentication's policy and error codes (LAPolicy, LAError).
const (
	laPolicyDeviceOwnerAuthentication = 2 // Touch ID, Apple Watch or the Mac's password

	laErrorAuthenticationFailed = -1
	laErrorUserCancel           = -2
	laErrorUserFallback         = -3
	laErrorSystemCancel         = -4
	laErrorPasscodeNotSet       = -5
	laErrorAppCancel            = -9
	laErrorNotInteractive       = -1004
)

type localAuth struct {
	class                                objc.Class
	selNew, selRelease, selInvalidate    objc.SEL
	selEvaluate, selCode, selDescription objc.SEL
	selUTF8String                        objc.SEL
}

var loadLocalAuth = sync.OnceValues(func() (*localAuth, error) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/LocalAuthentication.framework/LocalAuthentication", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return nil, fmt.Errorf("secret: load LocalAuthentication: %w", err)
	}
	a := &localAuth{class: objc.GetClass("LAContext")}
	if a.class == 0 {
		return nil, fmt.Errorf("secret: LocalAuthentication has no LAContext")
	}
	a.selNew = objc.RegisterName("new")
	a.selRelease = objc.RegisterName("release")
	a.selInvalidate = objc.RegisterName("invalidate")
	a.selEvaluate = objc.RegisterName("evaluatePolicy:localizedReason:reply:")
	a.selCode = objc.RegisterName("code")
	a.selDescription = objc.RegisterName("localizedDescription")
	a.selUTF8String = objc.RegisterName("UTF8String")
	return a, nil
})

type presenceResult struct {
	ok   bool
	code int
	msg  string
}

// confirmPresence asks the person to confirm with Touch ID or the Mac's
// password. reason completes the system's sentence "Pinky is trying to
// …". It returns ErrCancelled when they cancel or fail, and ctx's error
// when ctx ends first, which takes the prompt away.
func confirmPresence(ctx context.Context, reason string) error {
	a, err := loadLocalAuth()
	if err != nil {
		return err
	}
	s, err := loadSecurity()
	if err != nil {
		return err
	}
	o := &objects{s: s}
	defer o.release()
	text := o.str(reason)

	la := objc.ID(a.class).Send(a.selNew)
	if la == 0 {
		return fmt.Errorf("secret: cannot create an LAContext")
	}
	defer la.Send(a.selRelease)

	done := make(chan presenceResult, 1)
	// The reply runs once, on a queue of the framework's.
	reply := objc.NewBlock(func(_ objc.Block, ok bool, nserr objc.ID) {
		r := presenceResult{ok: ok}
		if !ok && nserr != 0 {
			r.code = objc.Send[int](nserr, a.selCode)
			if d := nserr.Send(a.selDescription); d != 0 {
				r.msg = objc.Send[string](d, a.selUTF8String)
			}
		}
		done <- r
	})
	defer reply.Release()
	la.Send(a.selEvaluate, laPolicyDeviceOwnerAuthentication, objc.ID(text), reply)

	var r presenceResult
	select {
	case r = <-done:
	case <-ctx.Done():
		// Takes the prompt away; the reply follows with LAErrorAppCancel.
		// The framework holds its own copy of the block, so returning
		// before the reply would be safe too; wait a while for tidiness.
		la.Send(a.selInvalidate)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return ctx.Err()
	}
	if r.ok {
		return nil
	}
	switch r.code {
	case laErrorAuthenticationFailed, laErrorUserCancel, laErrorUserFallback, laErrorSystemCancel, laErrorAppCancel:
		// The code tells a person's cancel from the system's (such as a
		// locked screen) in logs.
		return fmt.Errorf("%w (%s, LAError %d)", ErrCancelled, r.msg, r.code)
	case laErrorPasscodeNotSet:
		return fmt.Errorf("secret: this Mac has no password to confirm with: %w", ErrUnsupported)
	case laErrorNotInteractive:
		return fmt.Errorf("secret: cannot ask for Touch ID or the password here (no window session): %w", ErrUnsupported)
	}
	if r.msg == "" {
		r.msg = "authentication failed"
	}
	return fmt.Errorf("secret: %s (LAError %d)", r.msg, r.code)
}
