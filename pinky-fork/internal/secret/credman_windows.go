//go:build windows

package secret

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows store keeps each secret as a generic credential of the
// Credential Manager, whose target is "<service>/<account>" (as keytar and
// MyGo name theirs), kept on this computer only rather than roaming with
// the profile. Windows encrypts it for the user (DPAPI). Reveal first asks
// the person to confirm (confirmPresence): Windows Hello where it is set
// up, otherwise the account's password.

func system() Store { return newCredentialStore(Service) }

// newCredentialStore returns the store for a service, so tests can keep
// their credentials apart from Pinky's.
func newCredentialStore(service string) Store { return credStore{service: service} }

type credStore struct{ service string }

var (
	modadvapi32     = windows.NewLazySystemDLL("advapi32.dll")
	procCredWriteW  = modadvapi32.NewProc("CredWriteW")
	procCredReadW   = modadvapi32.NewProc("CredReadW")
	procCredDeleteW = modadvapi32.NewProc("CredDeleteW")
	procCredFree    = modadvapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1       // CRED_TYPE_GENERIC
	credPersistLocalMachine = 2       // CRED_PERSIST_LOCAL_MACHINE
	credMaxBlobSize         = 5 * 512 // CRED_MAX_CREDENTIAL_BLOB_SIZE
	credMaxTargetLength     = 32767   // CRED_MAX_GENERIC_TARGET_NAME_LENGTH
)

// credentialW is CREDENTIALW.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

func (s credStore) target(account string) (*uint16, error) {
	t := s.service + "/" + account
	if len(t) > credMaxTargetLength {
		return nil, fmt.Errorf("secret: account name too long")
	}
	return windows.UTF16PtrFromString(t)
}

func credError(op string, err error) error {
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return ErrNotFound
	}
	return fmt.Errorf("secret: credential manager %s: %w", op, err)
}

// read returns the account's secret, or with keep false only checks that
// one is saved. The copy Windows hands out is wiped before it is freed.
func (s credStore) read(account string, keep bool) (string, error) {
	target, err := s.target(account)
	if err != nil {
		return "", err
	}
	var c *credentialW
	if r, _, err := procCredReadW.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&c))); r == 0 {
		return "", credError("read", err)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(c)))
	if c.CredentialBlob == nil || c.CredentialBlobSize == 0 {
		return "", nil
	}
	blob := unsafe.Slice(c.CredentialBlob, c.CredentialBlobSize)
	v := ""
	if keep {
		v = string(blob)
	}
	clear(blob)
	return v, nil
}

// Supported reports whether Credential Manager's functions are there.
func (s credStore) Supported() bool {
	return procCredReadW.Find() == nil && procCredWriteW.Find() == nil
}

func (s credStore) Has(account string) bool {
	_, err := s.read(account, false)
	return err == nil
}

func (s credStore) Save(account, secret string) error {
	if len(secret) > credMaxBlobSize {
		return fmt.Errorf("secret: %d bytes is more than the Credential Manager keeps (%d)", len(secret), credMaxBlobSize)
	}
	target, err := s.target(account)
	if err != nil {
		return err
	}
	user, err := windows.UTF16PtrFromString(account)
	if err != nil {
		return err
	}
	comment, _ := windows.UTF16PtrFromString("Pinky")
	blob := []byte(secret)
	c := credentialW{
		Type:               credTypeGeneric,
		TargetName:         target,
		Comment:            comment,
		Persist:            credPersistLocalMachine,
		UserName:           user,
		CredentialBlobSize: uint32(len(blob)),
	}
	if len(blob) > 0 {
		c.CredentialBlob = &blob[0]
	}
	r, _, err := procCredWriteW.Call(uintptr(unsafe.Pointer(&c)), 0)
	clear(blob)
	if r == 0 {
		return credError("write", err)
	}
	return nil
}

func (s credStore) Delete(account string) error {
	target, err := s.target(account)
	if err != nil {
		return err
	}
	if r, _, err := procCredDeleteW.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0); r == 0 {
		if err := credError("delete", err); !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// Get implements Getter: it reads the secret without asking for Windows
// Hello.
func (s credStore) Get(ctx context.Context, account string) (string, error) {
	return s.read(account, true)
}

func (s credStore) Reveal(ctx context.Context, account, reason string) (string, error) {
	if _, err := s.read(account, false); err != nil {
		return "", err
	}
	if err := confirmPresence(ctx, reason); err != nil {
		return "", err
	}
	return s.read(account, true)
}
