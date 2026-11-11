//go:build darwin

package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The macOS store keeps each secret as a generic password of the login
// keychain (the file-based one), under Service and the account. Pinky.app
// is signed ad hoc, so it cannot use the data protection keychain, whose
// items need a keychain-access-groups entitlement and a real signing
// identity; nor can it attach a biometry access control to the item.
// Reveal therefore asks the person itself, through LocalAuthentication
// (Touch ID, or the Mac's password), before it reads the item.
//
// The item's access list trusts the program that created it, as macOS
// does by default: other programs, and a rebuilt Pinky with a new ad hoc
// signature, get the system's own "wants to use your confidential
// information" prompt before they can read the PIN.

func system() Store { return newKeychain(Service) }

// newKeychain returns the keychain store for a service, so tests can keep
// their items apart from Pinky's.
func newKeychain(service string) Store { return keychain{service: service} }

type keychain struct{ service string }

// Security framework result codes.
const (
	errSecSuccess               = 0
	errSecUserCanceled          = -128
	errSecAuthFailed            = -25293
	errSecDuplicateItem         = -25299
	errSecItemNotFound          = -25300
	errSecInteractionNotAllowed = -25308
)

const kCFStringEncodingUTF8 = 0x08000100

// cf is a CoreFoundation object (CFTypeRef).
type cf uintptr

// security holds the CoreFoundation and Security functions and constants
// the store uses, loaded once.
type security struct {
	CFRelease                         func(cf)
	CFDictionaryCreateMutable         func(alloc cf, capacity int, keyCallBacks, valueCallBacks uintptr) cf
	CFDictionarySetValue              func(d, key, value cf)
	CFStringCreateWithBytes           func(alloc cf, b *byte, n int, encoding uint32, external bool) cf
	CFStringGetLength                 func(s cf) int
	CFStringGetMaximumSizeForEncoding func(n int, encoding uint32) int
	CFStringGetCString                func(s cf, buf *byte, size int, encoding uint32) bool
	CFDataCreate                      func(alloc cf, b *byte, n int) cf
	CFDataGetLength                   func(d cf) int
	CFDataGetBytePtr                  func(d cf) unsafe.Pointer

	SecItemAdd                           func(attrs cf, result *cf) int32
	SecItemCopyMatching                  func(query cf, result *cf) int32
	SecItemUpdate                        func(query, attrs cf) int32
	SecItemDelete                        func(query cf) int32
	SecCopyErrorMessageString            func(status int32, reserved uintptr) cf
	SecKeychainSetUserInteractionAllowed func(allowed bool) int32

	keyCallBacks, valueCallBacks uintptr // &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks
	cfTrue                       cf

	kSecClass, kSecClassGenericPassword                  cf
	kSecAttrService, kSecAttrAccount                     cf
	kSecAttrLabel, kSecAttrDescription                   cf
	kSecValueData, kSecReturnData                        cf
	kSecMatchLimit, kSecMatchLimitOne                    cf
	kSecUseAuthenticationUI, kSecUseAuthenticationUIFail cf
}

var loadSecurity = sync.OnceValues(func() (*security, error) {
	cfLib, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("secret: load CoreFoundation: %w", err)
	}
	secLib, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("secret: load the Security framework: %w", err)
	}
	s := &security{}
	var missing []string
	fn := func(lib uintptr, fptr any, name string) {
		sym, err := purego.Dlsym(lib, name)
		if err != nil || sym == 0 {
			missing = append(missing, name)
			return
		}
		purego.RegisterFunc(fptr, sym)
	}
	// constant reads a global CFTypeRef the framework exports.
	constant := func(lib uintptr, name string) cf {
		sym, err := purego.Dlsym(lib, name)
		if err != nil || sym == 0 {
			missing = append(missing, name)
			return 0
		}
		return **(**cf)(unsafe.Pointer(&sym))
	}
	address := func(lib uintptr, name string) uintptr {
		sym, err := purego.Dlsym(lib, name)
		if err != nil || sym == 0 {
			missing = append(missing, name)
		}
		return sym
	}

	fn(cfLib, &s.CFRelease, "CFRelease")
	fn(cfLib, &s.CFDictionaryCreateMutable, "CFDictionaryCreateMutable")
	fn(cfLib, &s.CFDictionarySetValue, "CFDictionarySetValue")
	fn(cfLib, &s.CFStringCreateWithBytes, "CFStringCreateWithBytes")
	fn(cfLib, &s.CFStringGetLength, "CFStringGetLength")
	fn(cfLib, &s.CFStringGetMaximumSizeForEncoding, "CFStringGetMaximumSizeForEncoding")
	fn(cfLib, &s.CFStringGetCString, "CFStringGetCString")
	fn(cfLib, &s.CFDataCreate, "CFDataCreate")
	fn(cfLib, &s.CFDataGetLength, "CFDataGetLength")
	fn(cfLib, &s.CFDataGetBytePtr, "CFDataGetBytePtr")
	s.keyCallBacks = address(cfLib, "kCFTypeDictionaryKeyCallBacks")
	s.valueCallBacks = address(cfLib, "kCFTypeDictionaryValueCallBacks")
	s.cfTrue = constant(cfLib, "kCFBooleanTrue")

	fn(secLib, &s.SecItemAdd, "SecItemAdd")
	fn(secLib, &s.SecItemCopyMatching, "SecItemCopyMatching")
	fn(secLib, &s.SecItemUpdate, "SecItemUpdate")
	fn(secLib, &s.SecItemDelete, "SecItemDelete")
	fn(secLib, &s.SecCopyErrorMessageString, "SecCopyErrorMessageString")
	s.kSecClass = constant(secLib, "kSecClass")
	s.kSecClassGenericPassword = constant(secLib, "kSecClassGenericPassword")
	s.kSecAttrService = constant(secLib, "kSecAttrService")
	s.kSecAttrAccount = constant(secLib, "kSecAttrAccount")
	s.kSecAttrLabel = constant(secLib, "kSecAttrLabel")
	s.kSecAttrDescription = constant(secLib, "kSecAttrDescription")
	s.kSecValueData = constant(secLib, "kSecValueData")
	s.kSecReturnData = constant(secLib, "kSecReturnData")
	s.kSecMatchLimit = constant(secLib, "kSecMatchLimit")
	s.kSecMatchLimitOne = constant(secLib, "kSecMatchLimitOne")
	if len(missing) > 0 {
		return nil, fmt.Errorf("secret: the Security framework lacks %v", missing)
	}
	// Deprecated, and only refinements: without them Has may ask to
	// unlock a locked keychain, and the tests cannot turn prompts off.
	if sym, err := purego.Dlsym(secLib, "SecKeychainSetUserInteractionAllowed"); err == nil && sym != 0 {
		purego.RegisterFunc(&s.SecKeychainSetUserInteractionAllowed, sym)
	}
	ui, err1 := purego.Dlsym(secLib, "kSecUseAuthenticationUI")
	fail, err2 := purego.Dlsym(secLib, "kSecUseAuthenticationUIFail")
	if err1 == nil && err2 == nil && ui != 0 && fail != 0 {
		s.kSecUseAuthenticationUI = **(**cf)(unsafe.Pointer(&ui))
		s.kSecUseAuthenticationUIFail = **(**cf)(unsafe.Pointer(&fail))
	}
	return s, nil
})

// objects collects the CoreFoundation objects a call creates, to release
// them together.
type objects struct {
	s    *security
	list []cf
}

func (o *objects) keep(v cf) cf {
	if v != 0 {
		o.list = append(o.list, v)
	}
	return v
}

func (o *objects) release() {
	for _, v := range o.list {
		o.s.CFRelease(v)
	}
	o.list = nil
}

func (o *objects) str(v string) cf {
	// CoreFoundation returns NULL for bytes that are not UTF-8, which a
	// dictionary cannot hold.
	b := []byte(strings.ToValidUTF8(v, "�"))
	var p *byte
	if len(b) > 0 {
		p = &b[0]
	}
	return o.keep(o.s.CFStringCreateWithBytes(0, p, len(b), kCFStringEncodingUTF8, false))
}

func (o *objects) data(v []byte) cf {
	var p *byte
	if len(v) > 0 {
		p = &v[0]
	}
	return o.keep(o.s.CFDataCreate(0, p, len(v)))
}

// dict returns a new mutable dictionary of the key and value pairs.
func (o *objects) dict(kv ...cf) cf {
	d := o.keep(o.s.CFDictionaryCreateMutable(0, 0, o.s.keyCallBacks, o.s.valueCallBacks))
	for i := 0; i+1 < len(kv); i += 2 {
		o.s.CFDictionarySetValue(d, kv[i], kv[i+1])
	}
	return d
}

func (o *objects) set(d, key, value cf) { o.s.CFDictionarySetValue(d, key, value) }

// query returns a dictionary that finds the account's item.
func (k keychain) query(o *objects, account string) cf {
	s := o.s
	return o.dict(
		s.kSecClass, s.kSecClassGenericPassword,
		s.kSecAttrService, o.str(k.service),
		s.kSecAttrAccount, o.str(account),
	)
}

// goString copies a CFString.
func (s *security) goString(v cf) string {
	if v == 0 {
		return ""
	}
	size := s.CFStringGetMaximumSizeForEncoding(s.CFStringGetLength(v), kCFStringEncodingUTF8) + 1
	buf := make([]byte, size)
	if !s.CFStringGetCString(v, &buf[0], size, kCFStringEncodingUTF8) {
		return ""
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

// statusError turns a Security result code into an error.
func (s *security) statusError(op string, status int32) error {
	switch status {
	case errSecSuccess:
		return nil
	case errSecItemNotFound:
		return ErrNotFound
	case errSecUserCanceled, errSecAuthFailed:
		// The person denied the system's own keychain prompt.
		return ErrCancelled
	}
	e := &statusError{op: op, status: status}
	if m := s.SecCopyErrorMessageString(status, 0); m != 0 {
		e.msg = s.goString(m)
		s.CFRelease(m)
	}
	return e
}

// statusError is a keychain call's failure.
type statusError struct {
	op     string
	status int32 // OSStatus
	msg    string
}

func (e *statusError) Error() string {
	msg := e.msg
	if msg == "" {
		msg = "unknown error"
	}
	return fmt.Sprintf("secret: keychain %s: %s (OSStatus %d)", e.op, msg, e.status)
}

// Supported reports whether the Security framework loaded.
func (k keychain) Supported() bool {
	_, err := loadSecurity()
	return err == nil
}

func (k keychain) Has(account string) bool {
	s, err := loadSecurity()
	if err != nil {
		return false
	}
	o := &objects{s: s}
	defer o.release()
	q := k.query(o, account)
	o.set(q, s.kSecMatchLimit, s.kSecMatchLimitOne)
	// Never ask the person here, not even to unlock the keychain: a
	// keychain that needs them reads as nothing saved.
	if s.kSecUseAuthenticationUI != 0 {
		o.set(q, s.kSecUseAuthenticationUI, s.kSecUseAuthenticationUIFail)
	}
	return s.SecItemCopyMatching(q, nil) == errSecSuccess
}

func (k keychain) Save(account, secret string) error {
	s, err := loadSecurity()
	if err != nil {
		return err
	}
	o := &objects{s: s}
	defer o.release()
	value := o.data([]byte(secret))
	update := o.dict(s.kSecValueData, value)
	status := s.SecItemUpdate(k.query(o, account), update)
	if status != errSecItemNotFound {
		return s.statusError("update", status)
	}
	add := k.query(o, account)
	o.set(add, s.kSecValueData, value)
	o.set(add, s.kSecAttrLabel, o.str("Pinky ("+account+")"))
	o.set(add, s.kSecAttrDescription, o.str("Pinky secret"))
	status = s.SecItemAdd(add, nil)
	if status == errSecDuplicateItem {
		// Saved by another thread or process since the update.
		status = s.SecItemUpdate(k.query(o, account), update)
		return s.statusError("update", status)
	}
	return s.statusError("add", status)
}

func (k keychain) Delete(account string) error {
	s, err := loadSecurity()
	if err != nil {
		return err
	}
	o := &objects{s: s}
	defer o.release()
	if err := s.statusError("delete", s.SecItemDelete(k.query(o, account))); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (k keychain) Reveal(ctx context.Context, account, reason string) (string, error) {
	s, err := loadSecurity()
	if err != nil {
		return "", err
	}
	if err := k.find(s, account); err != nil {
		return "", err
	}
	if err := confirmPresence(ctx, reason); err != nil {
		return "", err
	}
	return k.read(s, account)
}

// Get implements Getter: it reads the secret without asking for Touch ID.
func (k keychain) Get(ctx context.Context, account string) (string, error) {
	s, err := loadSecurity()
	if err != nil {
		return "", err
	}
	return k.read(s, account)
}

// find checks that the account has an item, before Reveal asks the person
// about it. Unlike Has it may ask to unlock the keychain.
func (k keychain) find(s *security, account string) error {
	o := &objects{s: s}
	defer o.release()
	q := k.query(o, account)
	o.set(q, s.kSecMatchLimit, s.kSecMatchLimitOne)
	return s.statusError("find", s.SecItemCopyMatching(q, nil))
}

// read returns the account's secret.
func (k keychain) read(s *security, account string) (string, error) {
	o := &objects{s: s}
	defer o.release()
	q := k.query(o, account)
	o.set(q, s.kSecReturnData, s.cfTrue)
	o.set(q, s.kSecMatchLimit, s.kSecMatchLimitOne)
	var data cf
	if err := s.statusError("read", s.SecItemCopyMatching(q, &data)); err != nil {
		return "", err
	}
	if data == 0 {
		return "", errors.New("secret: keychain read: no data")
	}
	defer s.CFRelease(data)
	n := s.CFDataGetLength(data)
	if n == 0 {
		return "", nil
	}
	return string(unsafe.Slice((*byte)(s.CFDataGetBytePtr(data)), n)), nil
}

// setInteractionAllowed turns the keychain's own prompts (such as one to
// unlock the keychain) on or off for the whole process, so tests fail
// rather than wait for a person.
func setInteractionAllowed(allowed bool) error {
	s, err := loadSecurity()
	if err != nil {
		return err
	}
	if s.SecKeychainSetUserInteractionAllowed == nil {
		return errors.New("secret: this macOS cannot turn the keychain's prompts off")
	}
	return s.statusError("set interaction", s.SecKeychainSetUserInteractionAllowed(allowed))
}
