//go:build windows

package secret

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// confirmPresence asks the person to confirm it is them: with Windows
// Hello (face, fingerprint or the Hello PIN) when it is set up for the
// account, otherwise with the account's password in Windows' credential
// dialog. reason is the dialog's message. It returns ErrCancelled when
// they cancel or fail.
func confirmPresence(ctx context.Context, reason string) error {
	err := helloVerify(ctx, reason)
	if errors.Is(err, errHelloUnavailable) {
		log().Info("confirm it's you: Windows Hello is not available, asking for the password", "why", err.Error())
		err = passwordVerify(ctx, reason)
		if err != nil && !errors.Is(err, ErrCancelled) && ctx.Err() == nil {
			log().Warn("confirm it's you: the password check failed", "err", err.Error())
		}
		return err
	}
	switch {
	case err == nil:
		log().Info("confirm it's you: confirmed with Windows Hello")
	case errors.Is(err, ErrCancelled), ctx.Err() != nil:
		log().Info("confirm it's you: Windows Hello was cancelled")
	default:
		log().Warn("confirm it's you: Windows Hello failed", "err", err.Error())
	}
	return err
}

// errHelloUnavailable reports that Windows Hello cannot confirm the person
// here: it is not set up, not allowed, or not reachable from this program.
var errHelloUnavailable = errors.New("secret: Windows Hello is not available")

// Windows Hello is reached through the WinRT class UserConsentVerifier,
// called through its COM interfaces with syscall, as Pinky has no cgo.

var (
	modcombase                 = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = modcombase.NewProc("RoInitialize")
	procRoUninitialize         = modcombase.NewProc("RoUninitialize")
	procRoGetActivationFactory = modcombase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = modcombase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = modcombase.NewProc("WindowsDeleteString")
)

var (
	// IUserConsentVerifierStatics (windows.security.credentials.ui.idl).
	iidUserConsentVerifierStatics = windows.GUID{Data1: 0xaf4f3f91, Data2: 0x564c, Data3: 0x4ddc, Data4: [8]byte{0xb8, 0xb5, 0x97, 0x34, 0x47, 0x62, 0x7c, 0x65}}
	// IUserConsentVerifierInterop (UserConsentVerifierInterop.h), for
	// desktop programs, whose dialog needs an owner window: Windows 11.
	iidUserConsentVerifierInterop = windows.GUID{Data1: 0x39e050c3, Data2: 0x4e74, Data3: 0x441a, Data4: [8]byte{0x8d, 0xc0, 0xb8, 0x11, 0x04, 0xdf, 0x94, 0x9c}}
	// IAsyncOperation<UserConsentVerificationResult>, from its WinRT
	// signature (TestAsyncOperationIID derives it).
	iidAsyncOpVerificationResult = windows.GUID{Data1: 0xfd596ffd, Data2: 0x2318, Data3: 0x558f, Data4: [8]byte{0x9d, 0xbe, 0xd2, 0x1d, 0xf4, 0x37, 0x64, 0xa5}}
	// IAsyncInfo.
	iidAsyncInfo = windows.GUID{Data1: 0x00000036, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
)

const userConsentVerifierClass = "Windows.Security.Credentials.UI.UserConsentVerifier"

// Vtable slots, after IInspectable's six.
const (
	slotRelease                           = 2
	slotQueryInterface                    = 0
	slotCheckAvailabilityAsync            = 6 // IUserConsentVerifierStatics
	slotRequestVerificationAsync          = 7 // IUserConsentVerifierStatics
	slotRequestVerificationForWindowAsync = 6 // IUserConsentVerifierInterop
	slotGetResults                        = 8 // IAsyncOperation<T>
	slotAsyncStatus                       = 7 // IAsyncInfo.get_Status
	slotAsyncErrorCode                    = 8 // IAsyncInfo.get_ErrorCode
	slotAsyncCancel                       = 9 // IAsyncInfo.Cancel
)

// AsyncStatus, UserConsentVerifierAvailability and
// UserConsentVerificationResult values.
const (
	asyncStarted   = 0
	asyncCompleted = 1
	asyncCanceled  = 2
	asyncError     = 3

	consentAvailable = 0

	consentVerified         = 0
	consentDeviceBusy       = 4
	consentRetriesExhausted = 5
	consentCanceled         = 6
)

const (
	roInitMultithreaded = 1          // RO_INIT_MULTITHREADED
	rpcEChangedMode     = 0x80010106 // RPC_E_CHANGED_MODE
)

// comObject is a COM interface pointer.
type comObject struct{ p unsafe.Pointer }

// call calls the method in a vtable slot. Pointers the arguments carry
// stay alive and in place for the call, as for syscall.SyscallN.
//
//go:uintptrescapes
func (o comObject) call(slot int, args ...uintptr) uint32 {
	vtbl := *(*unsafe.Pointer)(o.p)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(o.p)}, args...)...)
	return uint32(r)
}

func (o comObject) release() {
	if o.p != nil {
		o.call(slotRelease)
	}
}

func (o comObject) queryInterface(iid *windows.GUID) (comObject, error) {
	out := new(unsafe.Pointer)
	if hr := o.call(slotQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(out))); failed(hr) {
		return comObject{}, hresult(hr)
	}
	return comObject{*out}, nil
}

func failed(hr uint32) bool { return int32(hr) < 0 }

func hresult(hr uint32) error { return syscall.Errno(hr) }

// hstring is a WinRT string, deleted with free.
type hstring uintptr

func newHString(s string) (hstring, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	h := new(hstring)
	if r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(h))); failed(uint32(r)) {
		return 0, hresult(uint32(r))
	}
	return *h, nil
}

func (h hstring) free() {
	if h != 0 {
		procWindowsDeleteString.Call(uintptr(h))
	}
}

// winrt runs fn on a thread of its own in WinRT's multithreaded apartment.
func winrt(fn func() error) error {
	if err := modcombase.Load(); err != nil {
		return fmt.Errorf("%w: %v", errHelloUnavailable, err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, _, _ := procRoInitialize.Call(roInitMultithreaded)
	switch hr := uint32(r); {
	case !failed(hr): // S_OK, or S_FALSE when it already was
		defer procRoUninitialize.Call()
	case hr == rpcEChangedMode: // already a single-threaded apartment: use it
	default:
		return fmt.Errorf("%w: RoInitialize: %v", errHelloUnavailable, hresult(hr))
	}
	return fn()
}

func activationFactory(class hstring, iid *windows.GUID) (comObject, error) {
	out := new(unsafe.Pointer)
	if r, _, _ := procRoGetActivationFactory.Call(uintptr(class), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(out))); failed(uint32(r)) {
		return comObject{}, hresult(uint32(r))
	}
	return comObject{*out}, nil
}

// await waits for an IAsyncOperation whose result is a 32-bit enum and
// returns the result, releasing the operation. When ctx ends first it
// cancels the operation, which takes its dialog away.
func await(ctx context.Context, op comObject) (int32, error) {
	defer op.release()
	info, err := op.queryInterface(&iidAsyncInfo)
	if err != nil {
		return 0, err
	}
	defer info.release()
	status := new(int32)
	cancelled := false
	for {
		if hr := info.call(slotAsyncStatus, uintptr(unsafe.Pointer(status))); failed(hr) {
			return 0, hresult(hr)
		}
		switch *status {
		case asyncStarted:
			select {
			case <-ctx.Done():
				if !cancelled {
					info.call(slotAsyncCancel)
					cancelled = true
				}
				time.Sleep(20 * time.Millisecond)
			case <-time.After(50 * time.Millisecond):
			}
		case asyncCompleted:
			result := new(int32)
			if hr := op.call(slotGetResults, uintptr(unsafe.Pointer(result))); failed(hr) {
				return 0, hresult(hr)
			}
			return *result, nil
		case asyncCanceled:
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return 0, ErrCancelled
		default: // asyncError
			code := new(int32)
			info.call(slotAsyncErrorCode, uintptr(unsafe.Pointer(code)))
			return 0, hresult(uint32(*code))
		}
	}
}

// helloAvailable reports whether Windows Hello can confirm the person:
// nil, or errHelloUnavailable with the reason.
func helloAvailable(ctx context.Context) error {
	return winrt(func() error {
		class, err := newHString(userConsentVerifierClass)
		if err != nil {
			return err
		}
		defer class.free()
		statics, err := activationFactory(class, &iidUserConsentVerifierStatics)
		if err != nil {
			return fmt.Errorf("%w: %v", errHelloUnavailable, err)
		}
		defer statics.release()
		return checkAvailability(ctx, statics)
	})
}

func checkAvailability(ctx context.Context, statics comObject) error {
	op := new(unsafe.Pointer)
	if hr := statics.call(slotCheckAvailabilityAsync, uintptr(unsafe.Pointer(op))); failed(hr) {
		return fmt.Errorf("%w: %v", errHelloUnavailable, hresult(hr))
	}
	avail, err := await(ctx, comObject{*op})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", errHelloUnavailable, err)
	}
	if avail != consentAvailable {
		return fmt.Errorf("%w (UserConsentVerifierAvailability %d)", errHelloUnavailable, avail)
	}
	return nil
}

// helloVerify asks the person through Windows Hello, owned by Pinky's
// window when it is in front.
func helloVerify(ctx context.Context, reason string) error {
	return winrt(func() error {
		class, err := newHString(userConsentVerifierClass)
		if err != nil {
			return err
		}
		defer class.free()
		statics, err := activationFactory(class, &iidUserConsentVerifierStatics)
		if err != nil {
			return fmt.Errorf("%w: %v", errHelloUnavailable, err)
		}
		defer statics.release()
		if err := checkAvailability(ctx, statics); err != nil {
			return err
		}
		msg, err := newHString(reason)
		if err != nil {
			return err
		}
		defer msg.free()

		op := new(unsafe.Pointer)
		if hwnd := ownWindow(); hwnd != 0 {
			if interop, err := activationFactory(class, &iidUserConsentVerifierInterop); err == nil {
				if hr := interop.call(slotRequestVerificationForWindowAsync, uintptr(hwnd), uintptr(msg), uintptr(unsafe.Pointer(&iidAsyncOpVerificationResult)), uintptr(unsafe.Pointer(op))); failed(hr) {
					*op = nil
				}
				interop.release()
			}
		}
		if *op == nil {
			if hr := statics.call(slotRequestVerificationAsync, uintptr(msg), uintptr(unsafe.Pointer(op))); failed(hr) {
				// Desktop programs without a window to own the dialog
				// may be refused: ask for the password instead.
				return fmt.Errorf("%w: %v", errHelloUnavailable, hresult(hr))
			}
		}
		result, err := await(ctx, comObject{*op})
		if err != nil {
			return err
		}
		switch result {
		case consentVerified:
			return nil
		case consentCanceled, consentRetriesExhausted:
			return ErrCancelled
		case consentDeviceBusy:
			return errors.New("secret: Windows Hello is busy; try again")
		}
		// Not set up or not allowed after all.
		return fmt.Errorf("%w (UserConsentVerificationResult %d)", errHelloUnavailable, result)
	})
}

// ownWindow returns the window in front when it is one of Pinky's, to own
// the system's dialog, or 0.
func ownWindow() windows.HWND {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid != windows.GetCurrentProcessId() {
		return 0
	}
	return hwnd
}

// The password fallback: Windows' credential dialog for the signed-in
// account, whose answer LogonUser checks.

var (
	modcredui                              = windows.NewLazySystemDLL("credui.dll")
	procCredUIPromptForWindowsCredentialsW = modcredui.NewProc("CredUIPromptForWindowsCredentialsW")
	procCredUnPackAuthenticationBufferW    = modcredui.NewProc("CredUnPackAuthenticationBufferW")
	procCredUIParseUserNameW               = modcredui.NewProc("CredUIParseUserNameW")
	procCredIsProtectedW                   = modadvapi32.NewProc("CredIsProtectedW")
	procCredUnprotectW                     = modadvapi32.NewProc("CredUnprotectW")
	procLogonUserW                         = modadvapi32.NewProc("LogonUserW")
	modnetapi32                            = windows.NewLazySystemDLL("netapi32.dll")
	procNetUserModalsGet                   = modnetapi32.NewProc("NetUserModalsGet")
)

// credUIInfoW is CREDUI_INFOW.
type credUIInfoW struct {
	Size        uint32
	Parent      windows.HWND
	MessageText *uint16
	CaptionText *uint16
	Banner      uintptr
}

const (
	creduiwinEnumerateCurrentUser = 0x200 // CREDUIWIN_ENUMERATE_CURRENT_USER
	logon32LogonNetwork           = 3     // LOGON32_LOGON_NETWORK
	logon32ProviderDefault        = 0     // LOGON32_PROVIDER_DEFAULT
	passwordTries                 = 3
)

// passwordVerify asks for the signed-in account's password and checks it.
// The dialog cannot be taken away early: when ctx ends while it shows,
// the answer is dropped once the person closes it.
func passwordVerify(ctx context.Context, reason string) error {
	if err := modcredui.Load(); err != nil {
		return fmt.Errorf("secret: no way to confirm the person here: %v: %w", err, ErrUnsupported)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	message, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return err
	}
	caption, _ := windows.UTF16PtrFromString("Pinky")
	info := &credUIInfoW{Parent: ownWindow(), MessageText: message, CaptionText: caption}
	info.Size = uint32(unsafe.Sizeof(*info))
	me := signedInUser()

	authError := uintptr(0)
	for try := 0; try < passwordTries; try++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		pkg := new(uint32)
		out := new(unsafe.Pointer)
		outSize := new(uint32)
		r, _, _ := procCredUIPromptForWindowsCredentialsW.Call(
			uintptr(unsafe.Pointer(info)), authError, uintptr(unsafe.Pointer(pkg)),
			0, 0, uintptr(unsafe.Pointer(out)), uintptr(unsafe.Pointer(outSize)),
			0, creduiwinEnumerateCurrentUser)
		switch syscall.Errno(r) {
		case 0:
		case windows.ERROR_CANCELLED:
			return ErrCancelled
		default:
			return fmt.Errorf("secret: the credential dialog failed: %w", syscall.Errno(r))
		}
		ok, err := checkPassword(*out, *outSize, me)
		clear(unsafe.Slice((*byte)(*out), *outSize))
		windows.CoTaskMemFree(*out)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if ok {
			return nil
		}
		authError = uintptr(windows.ERROR_LOGON_FAILURE)
	}
	if me.Microsoft {
		// Wrong every time: most likely the PIN they sign in with.
		return ErrMicrosoftAccount
	}
	return ErrCancelled
}

// checkPassword unpacks the dialog's answer and asks Windows whether the
// password is the signed-in account's (passwordVerdict), and logs what
// decided, never the password. The copies are wiped.
func checkPassword(buf unsafe.Pointer, size uint32, me *windowsUser) (bool, error) {
	const n = 1024
	name := make([]uint16, n)
	domain := make([]uint16, n)
	password := make([]uint16, n)
	defer clear(password)
	nameLen, domainLen, passwordLen := new(uint32), new(uint32), new(uint32)
	*nameLen, *domainLen, *passwordLen = n, n, n
	if r, _, err := procCredUnPackAuthenticationBufferW.Call(0, uintptr(buf), uintptr(size),
		uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(nameLen)),
		uintptr(unsafe.Pointer(&domain[0])), uintptr(unsafe.Pointer(domainLen)),
		uintptr(unsafe.Pointer(&password[0])), uintptr(unsafe.Pointer(passwordLen))); r == 0 {
		log().Warn("confirm with the password: could not read the dialog's answer", "err", errText(err))
		return false, fmt.Errorf("secret: read the credential dialog's answer: %w", err)
	}
	// The dialog may hand back the password protected (CredProtect), as
	// "@@D…": even an empty one is then a long string. LogonUserW reads it
	// either way, but whether it is empty only shows once unprotected.
	protection, err := unprotect(password)
	if err != nil {
		log().Warn("confirm with the password: could not read the dialog's answer", "protected", protection, "err", errText(err))
		return false, fmt.Errorf("secret: read the credential dialog's answer: %w", err)
	}
	// The name comes as DOMAIN\user or user@domain; LogonUser takes the
	// parts apart.
	user := make([]uint16, n)
	userDomain := make([]uint16, n)
	if r, _, _ := procCredUIParseUserNameW.Call(uintptr(unsafe.Pointer(&name[0])),
		uintptr(unsafe.Pointer(&user[0])), n, uintptr(unsafe.Pointer(&userDomain[0])), n); r != 0 {
		copy(user, name)
		clear(userDomain)
		// .\user does not parse; for LogonUser, "." is this computer's
		// own account database.
		if name[0] == '.' && name[1] == '\\' {
			clear(user)
			copy(user, name[2:])
			userDomain[0] = '.'
		}
	}
	if userDomain[0] == 0 && domain[0] != 0 {
		copy(userDomain, domain)
	}
	var domainArg *uint16
	if userDomain[0] != 0 {
		domainArg = &userDomain[0]
	}
	c := passwordCheck{
		Logon: logonUser(&user[0], domainArg, &password[0]),
		Empty: password[0] == 0,
	}
	dialogUser, dialogDomain := windows.UTF16ToString(user), windows.UTF16ToString(userDomain)
	if strings.EqualFold(dialogDomain, "MicrosoftAccount") {
		me.microsoft("dialog")
	}
	c.Me = me.windowsAccount
	attrs := []any{
		"empty", c.Empty,
		"protected", protection,
		"dialog_name", windows.UTF16ToString(name),
		"dialog_domain", windows.UTF16ToString(domain),
		"user", dialogUser,
		"domain", dialogDomain,
		"logon", errText(c.Logon),
	}
	if c.Empty && c.Logon != nil {
		by, why := me.named(dialogUser, dialogDomain)
		c.DialogIsMe = by != ""
		attrs = append(attrs, "dialog_is_me", by)
		if why != "" {
			attrs = append(attrs, "dialog_lookup", why)
		}
	}
	if c.Logon != nil {
		attrs = append(attrs, me.attrs()...)
	}
	var blank error
	asked := false
	c.Blank = func() error {
		asked = true
		blank = me.blankLogon()
		return blank
	}
	ok, step, err := passwordVerdict(c)
	if !asked && !c.Empty && errors.Is(c.Logon, errAccountRestriction) {
		// Not part of the decision, which a typed password never takes
		// to the account's blank-password check: whether the account has
		// no password tells what the person should do, for the log.
		c.Blank()
	}
	if asked {
		attrs = append(attrs, "blank_logon", errText(blank))
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
	}
	log().Log(context.Background(), level, "confirm with the password", append([]any{"result", step}, attrs...)...)
	return ok, err
}

// credProtectionType is CRED_PROTECTION_TYPE.
const credUnprotected = 0 // CredUnprotected

// unprotect replaces a password CredProtect protected with the password
// itself, in place, wiping the copy it makes. protection says what it
// found, for the log: "no", or the CRED_PROTECTION_TYPE, or why it could
// not tell.
func unprotect(password []uint16) (protection string, err error) {
	if err := procCredIsProtectedW.Find(); err != nil {
		return "unknown: " + errText(err), nil
	}
	kind := new(uint32)
	if r, _, err := procCredIsProtectedW.Call(uintptr(unsafe.Pointer(&password[0])), uintptr(unsafe.Pointer(kind))); r == 0 {
		// Not a string it reads as protected: a plain password.
		return "unknown: " + errText(err), nil
	}
	if *kind == credUnprotected {
		return "no", nil
	}
	protection = fmt.Sprintf("type %d", *kind)
	n := slices.Index(password, 0)
	if n < 0 {
		return protection, errors.New("the password has no end")
	}
	plain := make([]uint16, len(password))
	defer clear(plain)
	// As the process and as the thread: the dialog protected it in this
	// logon session, as one or the other.
	for _, asSelf := range []uintptr{0, 1} {
		size := new(uint32)
		*size = uint32(len(plain))
		r, _, e := procCredUnprotectW.Call(asSelf, uintptr(unsafe.Pointer(&password[0])), uintptr(n+1),
			uintptr(unsafe.Pointer(&plain[0])), uintptr(unsafe.Pointer(size)))
		if r != 0 {
			clear(password)
			copy(password, plain)
			return protection, nil
		}
		err = fmt.Errorf("CredUnprotectW: %w", e)
	}
	return protection, err
}

// logonUser asks Windows whether password signs the account in, with a
// network logon, and returns nil when it does. domain is nil for none.
func logonUser(user, domain, password *uint16) error {
	token := new(windows.Handle)
	r, _, err := procLogonUserW.Call(uintptr(unsafe.Pointer(user)), uintptr(unsafe.Pointer(domain)),
		uintptr(unsafe.Pointer(password)), logon32LogonNetwork, logon32ProviderDefault,
		uintptr(unsafe.Pointer(token)))
	if r == 0 {
		return err
	}
	windows.CloseHandle(*token)
	return nil
}

// windowsUser is the account Pinky runs as, as Windows names it from the
// thread's token: the facts passwordVerdict decides on, and what the log
// needs to say why.
type windowsUser struct {
	windowsAccount
	sid           *windows.SID
	name, domain  string   // LookupAccountSid
	accountDomain string   // this computer's account database's name
	localBy       string   // what showed the account local
	microsoftBy   []string // what showed it a Microsoft account
	problems      []string // lookups that failed
	computer      []string // this computer's names
}

// microsoftAccountAuthority is S-1-11. Windows adds the Microsoft
// account's own SID, S-1-11-96-…, to the token of a Microsoft account's
// sign-in (whoami /groups lists it as MicrosoftAccount\<e-mail>).
var microsoftAccountAuthority = windows.SidIdentifierAuthority{Value: [6]byte{0, 0, 0, 0, 0, 11}}

// signedInUser asks Windows about the account Pinky runs as. Nothing in
// it signs in or counts as a failed sign-in.
func signedInUser() *windowsUser {
	u := &windowsUser{computer: computerNames()}
	token := windows.GetCurrentThreadEffectiveToken()
	tu, err := token.GetTokenUser()
	if err != nil {
		u.problem("token user", err)
		return u
	}
	if u.sid, err = tu.User.Sid.Copy(); err != nil {
		u.problem("token user", err)
		return u
	}
	if u.name, u.domain, _, err = u.sid.LookupAccount(""); err != nil {
		u.problem("account name", err)
		return u
	}
	u.Known = true
	// Local: the SID is in this computer's account domain. Comparing
	// names would not do: the computer has a NetBIOS name of at most 15
	// characters and a DNS name, and either may differ from the other.
	accountDomain, name, err := localAccountDomain()
	switch {
	case err == nil:
		u.accountDomain = name
		if inDomain(u.sid, accountDomain) {
			u.Local, u.localBy = true, "account domain"
		}
	default:
		u.problem("account domain", err)
		for _, c := range u.computer {
			if strings.EqualFold(c, u.domain) {
				u.Local, u.localBy = true, "computer name"
			}
		}
	}
	if u.Local {
		switch connected, err := microsoftConnected(u.name); {
		case err != nil:
			u.problem("account database", err)
		case connected:
			u.microsoft("account database")
		}
	}
	if groups, err := token.GetTokenGroups(); err != nil {
		u.problem("token groups", err)
	} else {
		for _, g := range groups.AllGroups() {
			if g.Sid != nil && g.Sid.IdentifierAuthority() == microsoftAccountAuthority {
				u.microsoft("token")
				break
			}
		}
	}
	return u
}

func (u *windowsUser) problem(what string, err error) {
	u.problems = append(u.problems, what+": "+errText(err))
}

func (u *windowsUser) microsoft(by string) {
	u.Microsoft = true
	if !slices.Contains(u.microsoftBy, by) {
		u.microsoftBy = append(u.microsoftBy, by)
	}
}

// named reports whether the dialog named this account, without signing
// in: by is "sid" when the name resolves to its SID, "name" when the name
// does not resolve (as with a computer name Windows does not look up) but
// its user part is this account's name, and "" when the dialog named
// another account or none. lookup is the lookup's error, for the log.
func (u *windowsUser) named(user, domain string) (by, lookup string) {
	if u.sid == nil || user == "" {
		return "", ""
	}
	name := user
	if domain != "" && domain != "." {
		name = domain + `\` + user
	}
	sid, _, _, err := windows.LookupSID("", name)
	switch {
	case err == nil && windows.EqualSid(sid, u.sid):
		return "sid", ""
	case err == nil:
		return "", ""
	case strings.EqualFold(user, u.name):
		return "name", errText(err)
	}
	return "", errText(err)
}

// blankLogon asks Windows whether an empty password signs the account in,
// by its name in this computer's account database ("."): nil, or
// ERROR_ACCOUNT_RESTRICTION under the blank-password policy, when it has
// no password.
func (u *windowsUser) blankLogon() error {
	name, err := windows.UTF16PtrFromString(u.name)
	if err != nil {
		return err
	}
	dot, _ := windows.UTF16PtrFromString(".")
	empty := uint16(0)
	return logonUser(name, dot, &empty)
}

// attrs are the account's facts for the log.
func (u *windowsUser) attrs() []any {
	return []any{
		"me", u.domain + `\` + u.name,
		"local", u.Local,
		"local_by", u.localBy,
		"account_domain", u.accountDomain,
		"computer", strings.Join(u.computer, ","),
		"microsoft", u.Microsoft,
		"microsoft_by", strings.Join(u.microsoftBy, ","),
		"problems", strings.Join(u.problems, "; "),
	}
}

// userModalsInfo2 is USER_MODALS_INFO_2.
type userModalsInfo2 struct {
	DomainName *uint16
	DomainID   *windows.SID
}

// localAccountDomain returns the SID and name of this computer's own
// account database, which any user may read.
func localAccountDomain() (*windows.SID, string, error) {
	if err := procNetUserModalsGet.Find(); err != nil {
		return nil, "", err
	}
	var buf *byte
	if r, _, _ := procNetUserModalsGet.Call(0, 2, uintptr(unsafe.Pointer(&buf))); r != 0 {
		return nil, "", syscall.Errno(r)
	}
	defer windows.NetApiBufferFree(buf)
	info := (*userModalsInfo2)(unsafe.Pointer(buf))
	if info.DomainID == nil {
		return nil, "", errors.New("no domain SID")
	}
	sid, err := info.DomainID.Copy()
	return sid, windows.UTF16PtrToString(info.DomainName), err
}

// inDomain reports whether sid is an account of the domain whose SID is
// domain: the domain's SID and one more part, the account's.
func inDomain(sid, domain *windows.SID) bool {
	n := uint32(domain.SubAuthorityCount())
	if uint32(sid.SubAuthorityCount()) != n+1 || sid.IdentifierAuthority() != domain.IdentifierAuthority() {
		return false
	}
	for i := range n {
		if sid.SubAuthority(i) != domain.SubAuthority(i) {
			return false
		}
	}
	return true
}

// userInfo24 is USER_INFO_24.
type userInfo24 struct {
	InternetIdentity      int32 // BOOL
	Flags                 uint32
	InternetProviderName  *uint16
	InternetPrincipalName *uint16
	UserSID               *windows.SID
}

// microsoftConnected reports whether the local account by this name is
// connected to a Microsoft account.
func microsoftConnected(name string) (bool, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	var buf *byte
	if err := windows.NetUserGetInfo(nil, n, 24, &buf); err != nil {
		return false, err
	}
	defer windows.NetApiBufferFree(buf)
	return (*userInfo24)(unsafe.Pointer(buf)).InternetIdentity != 0, nil
}

// computerNames are this computer's NetBIOS and DNS names, for the log
// and for when the account database cannot be read.
func computerNames() []string {
	var names []string
	for _, t := range []uint32{windows.ComputerNameNetBIOS, windows.ComputerNameDnsHostname,
		windows.ComputerNamePhysicalNetBIOS, windows.ComputerNamePhysicalDnsHostname} {
		buf := make([]uint16, 256)
		n := uint32(len(buf))
		if err := windows.GetComputerNameEx(t, &buf[0], &n); err != nil || n == 0 {
			continue
		}
		name := windows.UTF16ToString(buf[:n])
		if !slices.ContainsFunc(names, func(s string) bool { return strings.EqualFold(s, name) }) {
			names = append(names, name)
		}
	}
	return names
}

// errText is an error for the log: Win32 errors with their number, which
// does not depend on Windows' language.
func errText(err error) string {
	var errno syscall.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &errno):
		return fmt.Sprintf("%d %s", uint32(errno), err.Error())
	}
	return err.Error()
}
