package secret

import (
	"errors"
	"fmt"
	"syscall"
)

// The Win32 errors of LogonUserW that the Windows password check reads
// (winerror.h). They are here, without a build tag, so that the decision
// below is tested on every system.
const (
	errLogonFailure       = syscall.Errno(1326) // ERROR_LOGON_FAILURE
	errAccountRestriction = syscall.Errno(1327) // ERROR_ACCOUNT_RESTRICTION
)

// windowsAccount is what Windows says about the account Pinky runs as,
// for the password check.
type windowsAccount struct {
	// Known: Windows named the account (its SID and name).
	Known bool
	// Local: it is an account of this computer's own account database,
	// which the blank-password policy is about. Domain, Entra ID and
	// other accounts are not.
	Local bool
	// Microsoft: it is connected to a Microsoft account, which always
	// has a password, even when the person signs in with a PIN or Windows
	// Hello and never types it.
	Microsoft bool
}

// passwordCheck is what passwordVerdict decides on.
type passwordCheck struct {
	// Logon is LogonUserW's answer for the name the Windows credential
	// dialog returned and the typed password: nil when it signed in.
	Logon error
	// Empty: nothing was typed.
	Empty bool
	// DialogIsMe: the dialog's name resolves to the account Pinky runs
	// as, or does not resolve and its user part is that account's name.
	// Only read when Empty.
	DialogIsMe bool
	// Me is the account Pinky runs as.
	Me windowsAccount
	// Blank asks LogonUserW whether an empty password signs in the
	// account Pinky runs as, by its own name in this computer's account
	// database. passwordVerdict calls it only when it matters.
	Blank func() error
}

// passwordVerdict reads LogonUserW's answer to the password typed in the
// Windows credential dialog: true when it is the password of the account
// Pinky runs as, false with no error when it is wrong (the dialog asks
// again), and an error for anything else. step says what decided, for the
// log.
//
// A local account with a blank password never passes the network logon
// the check uses: Windows' default policy "Accounts: Limit local account
// use of blank passwords to console logon only" (LimitBlankPasswordUse=1)
// refuses it with ERROR_ACCOUNT_RESTRICTION, and so does an interactive
// logon through LogonUserW. Windows gives that error only after the blank
// password has matched. Measured on 2026-11-10 on GitHub's windows-latest
// runner (Windows Server 2025), with the policy on and off: an empty
// password for an account that has a password was ERROR_LOGON_FAILURE in
// every state (enabled, disabled, outside its logon hours, must change
// its password, account expired, with or without "password not
// required"), or ERROR_ACCOUNT_LOCKED_OUT when locked out; a
// blank-password account with another restriction got that restriction's
// error, not this one. docs/notes/platform.md has the measurements.
//
// So an empty password counts as right when Windows, asked about the
// account Pinky runs as by its own name in this computer's account
// database, signs it in or answers ERROR_ACCOUNT_RESTRICTION. The dialog's
// name only decides whether to ask: it comes in many forms (the computer's
// NetBIOS or DNS name, a UPN, a display name), and Windows maps some of
// them to the local account on a workgroup computer where
// LookupAccountName does not, which is what failed in issue #41. Pinky
// asks when the dialog's name got ERROR_ACCOUNT_RESTRICTION, or
// ERROR_LOGON_FAILURE while it names another account or none
// (DialogIsMe). The second sign-in is a failed one for an account with a
// password, and Windows locks an account out after a few (10 in 10
// minutes by default), so the signed-in account's own name never takes
// it. Windows' answer about another account never counts. A Microsoft
// account, a domain account, or a password that is not empty never takes
// this path.
//
// Empty must mean the password itself is empty. The credential dialog may
// return the password protected (CredProtect, "@@D…"), so that an empty
// one comes back as a long string; checkPassword unprotects it first.
// Before it did, issue #41's empty password counted as typed, and Windows,
// which reads protected passwords, answered ERROR_ACCOUNT_RESTRICTION.
//
// An ERROR_ACCOUNT_RESTRICTION this does not resolve wraps
// ErrAccountRestricted, which tells the person what to do.
func passwordVerdict(c passwordCheck) (ok bool, step string, err error) {
	ok, step, err = verdict(c)
	// Whatever else it is, an ERROR_ACCOUNT_RESTRICTION that was not
	// resolved tells the person what to do about it.
	if err != nil && errors.Is(err, errAccountRestriction) && !errors.Is(err, ErrMicrosoftAccount) {
		err = fmt.Errorf("%w: %w", err, ErrAccountRestricted)
	}
	return ok, step, err
}

func verdict(c passwordCheck) (ok bool, step string, err error) {
	if c.Logon == nil {
		return true, "signed in", nil
	}
	wrong := errors.Is(c.Logon, errLogonFailure)
	refused := func() (bool, string, error) {
		if wrong {
			return false, "wrong password", nil
		}
		return false, "refused", fmt.Errorf("secret: check the password: %w", c.Logon)
	}
	if !c.Empty {
		return refused()
	}
	// Nothing was typed.
	if c.Me.Microsoft {
		return false, "Microsoft account", ErrMicrosoftAccount
	}
	if !errors.Is(c.Logon, errAccountRestriction) && !(wrong && !c.DialogIsMe) {
		return refused()
	}
	notBlank := func(step, why string, also error) (bool, string, error) {
		if wrong {
			return false, step, nil
		}
		err := fmt.Errorf("secret: Windows refused the empty password (%w), and Pinky could not confirm that the Windows account it runs as has no password: %s", c.Logon, why)
		if also != nil {
			err = fmt.Errorf("%w: %w", err, also)
		}
		return false, step, err
	}
	switch {
	case !c.Me.Known:
		return notBlank("blank: account unknown", "Windows did not say which account that is", nil)
	case !c.Me.Local:
		return notBlank("blank: not local", "it is not an account of this computer", nil)
	}
	switch err := c.Blank(); {
	case err == nil, errors.Is(err, errAccountRestriction):
		return true, "blank password", nil
	case errors.Is(err, errLogonFailure):
		// The account Pinky runs as has a password: whatever account the
		// dialog's name reached, the empty password is not its.
		return false, "blank: has a password", nil
	default:
		return notBlank("blank: refused", "Windows answered for it", err)
	}
}
