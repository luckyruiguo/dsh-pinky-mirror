//go:build darwin

package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// testService keeps the tests' items apart from Pinky's own.
const testService = "app.pinky.desktop.test"

// testKeychain returns a store on the test service and an account no
// earlier run used, and deletes the account's item when the test ends.
// The keychain's own prompts are turned off, so a locked keychain skips
// the test rather than waiting for a person.
func testKeychain(t *testing.T) (Store, string) {
	t.Helper()
	if err := setInteractionAllowed(false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { setInteractionAllowed(true) })
	var b [6]byte
	rand.Read(b[:])
	account := "test:" + hex.EncodeToString(b[:])
	s := newKeychain(testService)
	t.Cleanup(func() {
		if err := s.Delete(account); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		if s.Has(account) {
			t.Errorf("cleanup: %s is still saved", account)
		}
	})
	return s, account
}

// skipIfLocked skips the test when the keychain would have asked the
// person to unlock it.
func skipIfLocked(t *testing.T, err error) {
	t.Helper()
	var se *statusError
	if errors.As(err, &se) && se.status == errSecInteractionNotAllowed {
		t.Skip("the login keychain is locked:", err)
	}
}

func TestKeychainSaveHasDelete(t *testing.T) {
	s, account := testKeychain(t)
	if s.Has(account) {
		t.Fatalf("Has(%s) before Save", account)
	}
	err := s.Save(account, "1234")
	skipIfLocked(t, err)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Has(account) {
		t.Fatalf("Has(%s) = false after Save", account)
	}
	// Saving again replaces the secret rather than failing as a
	// duplicate.
	if err := s.Save(account, "567890"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if got, err := s.(keychain).read(mustSecurity(t), account); err != nil || got != "567890" {
		t.Fatalf("read after second Save = %q, %v; want 567890", got, err)
	}
	if err := s.Delete(account); err != nil {
		t.Fatal(err)
	}
	if s.Has(account) {
		t.Fatalf("Has(%s) after Delete", account)
	}
	// Deleting nothing is not an error.
	if err := s.Delete(account); err != nil {
		t.Fatalf("Delete of a deleted item: %v", err)
	}
}

func TestKeychainKeepsServicesApart(t *testing.T) {
	s, account := testKeychain(t)
	err := s.Save(account, "4321")
	skipIfLocked(t, err)
	if err != nil {
		t.Fatal(err)
	}
	if System().Has(account) {
		t.Errorf("Pinky's own service sees the test item %s", account)
	}
}

func TestKeychainRevealMissing(t *testing.T) {
	s, account := testKeychain(t)
	// Nothing is saved, so Reveal fails before it asks anyone.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Reveal(ctx, account, "test Pinky's keychain"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reveal of a missing item = %v, want ErrNotFound", err)
	}
}

func TestKeychainUTF8(t *testing.T) {
	s, account := testKeychain(t)
	const secret = "pïn 密码 🔒"
	err := s.Save(account, secret)
	skipIfLocked(t, err)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.(keychain).read(mustSecurity(t), account); err != nil || got != secret {
		t.Fatalf("read = %q, %v; want %q", got, err, secret)
	}
}

func mustSecurity(t *testing.T) *security {
	t.Helper()
	s, err := loadSecurity()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
