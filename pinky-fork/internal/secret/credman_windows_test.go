//go:build windows

package secret

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const testService = "app.pinky.desktop.test"

func testCredentialStore(t *testing.T) (Store, string) {
	t.Helper()
	var b [6]byte
	rand.Read(b[:])
	account := "test:" + hex.EncodeToString(b[:])
	s := newCredentialStore(testService)
	t.Cleanup(func() {
		if err := s.Delete(account); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return s, account
}

func TestCredentialSaveHasDelete(t *testing.T) {
	s, account := testCredentialStore(t)
	if s.Has(account) {
		t.Fatalf("Has(%s) before Save", account)
	}
	if err := s.Save(account, "1234"); err != nil {
		t.Fatal(err)
	}
	if !s.Has(account) {
		t.Fatalf("Has(%s) = false after Save", account)
	}
	if err := s.Save(account, "pïn 密码"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if got, err := s.(credStore).read(account, true); err != nil || got != "pïn 密码" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if System().Has(account) {
		t.Errorf("Pinky's own service sees the test credential")
	}
	if err := s.Delete(account); err != nil {
		t.Fatal(err)
	}
	if s.Has(account) {
		t.Fatalf("Has(%s) after Delete", account)
	}
	if err := s.Delete(account); err != nil {
		t.Fatalf("Delete of a deleted credential: %v", err)
	}
}

func TestCredentialRevealMissing(t *testing.T) {
	s, account := testCredentialStore(t)
	if _, err := s.Reveal(context.Background(), account, "test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reveal of a missing credential = %v, want ErrNotFound", err)
	}
}

func TestCredentialTooLarge(t *testing.T) {
	s, account := testCredentialStore(t)
	if err := s.Save(account, string(make([]byte, credMaxBlobSize+1))); err == nil {
		t.Fatal("Save of an oversized secret succeeded")
	}
}

// TestHelloAvailability goes through WinRT (activation factory, the
// vtables, an async operation) without asking anyone: it only asks
// whether Windows Hello is set up.
func TestHelloAvailability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := helloAvailable(ctx)
	if err != nil && !errors.Is(err, errHelloUnavailable) {
		t.Fatalf("helloAvailable = %v, want nil or errHelloUnavailable", err)
	}
	t.Logf("Windows Hello: %v", err)
}

// TestAsyncOperationIID derives the IID of
// IAsyncOperation<UserConsentVerificationResult> from its WinRT signature,
// as the WinRT type system does, and checks the one in the code.
func TestAsyncOperationIID(t *testing.T) {
	piid := func(sig string) windows.GUID {
		ns := []byte{0x11, 0xf4, 0x7a, 0xd5, 0x7b, 0x73, 0x42, 0xc0, 0xab, 0xae, 0x87, 0x8b, 0x1e, 0x16, 0xad, 0xee}
		h := sha1.Sum(append(ns, sig...))
		h[6] = h[6]&0x0f | 0x50
		h[8] = h[8]&0x3f | 0x80
		var g windows.GUID
		g.Data1 = uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])
		g.Data2 = uint16(h[4])<<8 | uint16(h[5])
		g.Data3 = uint16(h[6])<<8 | uint16(h[7])
		copy(g.Data4[:], h[8:16])
		return g
	}
	const asyncOperation = "{9fc2b0bb-e446-44e2-aa61-9cab8f636af2}"
	// A known one first: IAsyncOperation<Boolean>.
	if got, want := piid("pinterface("+asyncOperation+";b1)").String(), "{CDB5EFB3-5788-509D-9BE1-71CCB8A3362A}"; got != want {
		t.Fatalf("IAsyncOperation<Boolean> = %s, want %s", got, want)
	}
	got := piid("pinterface(" + asyncOperation + ";enum(Windows.Security.Credentials.UI.UserConsentVerificationResult;i4))")
	if got != iidAsyncOpVerificationResult {
		t.Fatalf("IAsyncOperation<UserConsentVerificationResult> = %s, code has %s", got, iidAsyncOpVerificationResult)
	}
}
