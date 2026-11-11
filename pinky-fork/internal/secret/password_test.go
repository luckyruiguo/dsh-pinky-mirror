package secret

import (
	"errors"
	"syscall"
	"testing"
)

func TestPasswordVerdict(t *testing.T) {
	const (
		errInvalidLogonHours = syscall.Errno(1328) // ERROR_INVALID_LOGON_HOURS
		errAccountDisabled   = syscall.Errno(1331) // ERROR_ACCOUNT_DISABLED
		errAccountExpired    = syscall.Errno(1793) // ERROR_ACCOUNT_EXPIRED
		errMustChange        = syscall.Errno(1907) // ERROR_PASSWORD_MUST_CHANGE
		errLockedOut         = syscall.Errno(1909) // ERROR_ACCOUNT_LOCKED_OUT
	)
	local := windowsAccount{Known: true, Local: true}
	microsoft := windowsAccount{Known: true, Local: true, Microsoft: true}
	domain := windowsAccount{Known: true}
	other := errors.New("boom")
	tests := []struct {
		name    string
		check   passwordCheck
		blank   error // what Blank answers
		wantOK  bool
		wantErr []error // the error wraps all of these; nil: no error
		notErr  error   // the error does not wrap this
		asked   bool    // Blank was called
	}{
		{name: "signed in", check: passwordCheck{Me: local}, wantOK: true},
		{name: "signed in, empty", check: passwordCheck{Empty: true, Me: local}, wantOK: true},
		{name: "wrong password", check: passwordCheck{Logon: errLogonFailure, Me: local}},
		{name: "restriction, password typed", check: passwordCheck{Logon: errAccountRestriction, Me: local},
			wantErr: []error{errAccountRestriction, ErrAccountRestricted}},
		{name: "disabled, password typed", check: passwordCheck{Logon: errAccountDisabled, Me: local},
			wantErr: []error{errAccountDisabled}, notErr: ErrAccountRestricted},

		// Nothing typed for a Microsoft account: it has a password.
		{name: "Microsoft account, empty", check: passwordCheck{Logon: errLogonFailure, Empty: true, DialogIsMe: true, Me: microsoft},
			wantErr: []error{ErrMicrosoftAccount}},
		{name: "Microsoft account, empty, restriction", check: passwordCheck{Logon: errAccountRestriction, Empty: true, Me: microsoft},
			wantErr: []error{ErrMicrosoftAccount}, notErr: ErrAccountRestricted},
		{name: "Microsoft account, typed wrong", check: passwordCheck{Logon: errLogonFailure, Me: microsoft}},

		// Issue #41: the dialog's name got the blank-password policy's
		// error; the signed-in account decides.
		{name: "blank, policy on", check: passwordCheck{Logon: errAccountRestriction, Empty: true, Me: local},
			blank: errAccountRestriction, wantOK: true, asked: true},
		{name: "blank, policy off", check: passwordCheck{Logon: errAccountRestriction, Empty: true, Me: local},
			blank: nil, wantOK: true, asked: true},
		{name: "restriction, signed-in account has a password", check: passwordCheck{Logon: errAccountRestriction, Empty: true, Me: local},
			blank: errLogonFailure, asked: true},
		{name: "restriction, signed-in account refused", check: passwordCheck{Logon: errAccountRestriction, Empty: true, Me: local},
			blank: errAccountDisabled, asked: true, wantErr: []error{errAccountRestriction, errAccountDisabled, ErrAccountRestricted}},
		{name: "restriction, not a local account", check: passwordCheck{Logon: errAccountRestriction, Empty: true, Me: domain},
			wantErr: []error{errAccountRestriction, ErrAccountRestricted}},
		{name: "restriction, account unknown", check: passwordCheck{Logon: errAccountRestriction, Empty: true},
			wantErr: []error{errAccountRestriction, ErrAccountRestricted}},

		// A wrong empty password for the signed-in account's own name
		// needs no second try.
		{name: "wrong, empty, own name", check: passwordCheck{Logon: errLogonFailure, Empty: true, DialogIsMe: true, Me: local}},
		// A name that does not resolve to it (.\user, an unknown domain):
		// the signed-in account decides.
		{name: "wrong, empty, other name, blank", check: passwordCheck{Logon: errLogonFailure, Empty: true, Me: local},
			blank: errAccountRestriction, wantOK: true, asked: true},
		{name: "wrong, empty, other name, has a password", check: passwordCheck{Logon: errLogonFailure, Empty: true, Me: local},
			blank: errLogonFailure, asked: true},
		{name: "wrong, empty, other name, refused", check: passwordCheck{Logon: errLogonFailure, Empty: true, Me: local},
			blank: errLockedOut, asked: true},
		{name: "wrong, empty, other name, not local", check: passwordCheck{Logon: errLogonFailure, Empty: true, Me: domain}},
		{name: "wrong, empty, other name, unknown", check: passwordCheck{Logon: errLogonFailure, Empty: true}},

		// The account's other restrictions are errors, without a second
		// try: Windows checks them before the blank-password policy.
		{name: "disabled, empty", check: passwordCheck{Logon: errAccountDisabled, Empty: true, Me: local},
			wantErr: []error{errAccountDisabled}},
		{name: "logon hours, empty", check: passwordCheck{Logon: errInvalidLogonHours, Empty: true, Me: local},
			wantErr: []error{errInvalidLogonHours}},
		{name: "must change, empty", check: passwordCheck{Logon: errMustChange, Empty: true, Me: local},
			wantErr: []error{errMustChange}},
		{name: "account expired, empty", check: passwordCheck{Logon: errAccountExpired, Empty: true, Me: local},
			wantErr: []error{errAccountExpired}},
		{name: "locked out, empty", check: passwordCheck{Logon: errLockedOut, Empty: true, Me: local},
			wantErr: []error{errLockedOut}},
		{name: "other error, empty", check: passwordCheck{Logon: other, Empty: true, Me: local},
			wantErr: []error{other}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked := false
			tt.check.Blank = func() error {
				asked = true
				return tt.blank
			}
			ok, step, err := passwordVerdict(tt.check)
			if step == "" {
				t.Errorf("no step")
			}
			if ok != tt.wantOK {
				t.Errorf("ok = %v (%s), want %v", ok, step, tt.wantOK)
			}
			if tt.wantErr == nil && err != nil {
				t.Errorf("err = %v, want none", err)
			}
			if tt.wantErr != nil && err == nil {
				t.Errorf("no error, want one (%s)", step)
			}
			for _, want := range tt.wantErr {
				if err != nil && !errors.Is(err, want) {
					t.Errorf("err = %v, want one that wraps %v", err, want)
				}
			}
			if tt.notErr != nil && errors.Is(err, tt.notErr) {
				t.Errorf("err = %v, want one that does not wrap %v", err, tt.notErr)
			}
			if asked != tt.asked {
				t.Errorf("Blank called = %v, want %v", asked, tt.asked)
			}
		})
	}
}
