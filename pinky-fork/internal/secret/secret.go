// Package secret keeps phone PINs in the operating system's keychain and
// hands one out only after the person confirms with Touch ID (or the Mac's
// password), Windows Hello, or the system's equivalent. Pinky never writes a
// PIN anywhere else, never logs it and never gives it to agents.
//
// It also keeps the API keys of the model services Pinky's own agent uses
// (ModelKeyAccount). Those are read without asking the person (Get): Pinky
// sends them to the service the person chose, never to a phone or an agent.
package secret

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// Errors.
var (
	ErrNotFound    = errors.New("secret: nothing saved")
	ErrUnsupported = errors.New("secret: no supported keychain on this system")
	ErrCancelled   = errors.New("secret: the person cancelled")
	// ErrMicrosoftAccount: Windows asked for the password of an account
	// connected to a Microsoft account and got none, or only wrong ones.
	// Such an account always has a password, even when the person signs
	// in with a PIN or Windows Hello.
	ErrMicrosoftAccount = errors.New("secret: this Windows account is a Microsoft account, which has a password even when you sign in with a PIN or Windows Hello: type the Microsoft account's password")
	// ErrAccountRestricted: Windows would not check the password of the
	// account Pinky runs as (ERROR_ACCOUNT_RESTRICTION), and Pinky could
	// not confirm that the account has no password. Most likely it has
	// none and Windows' blank-password policy is in the way; a password or
	// a Windows Hello PIN for the account makes confirming work.
	ErrAccountRestricted = errors.New("secret: Windows won't check this account's password, most likely because it has no password: set a password or a Windows Hello PIN for it")
)

var logger atomic.Pointer[slog.Logger]

// SetLogger sends what the system's checks decide to l: which way the
// person confirmed, and why a check failed. It never logs a secret or a
// password.
func SetLogger(l *slog.Logger) { logger.Store(l) }

func log() *slog.Logger {
	if l := logger.Load(); l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

// Store holds secrets by account name.
type Store interface {
	// Has reports whether a secret is saved, without asking the person.
	Has(account string) bool
	Save(account, secret string) error
	Delete(account string) error
	// Reveal asks the person to confirm, with reason in the system's
	// prompt ("unlock Mi 10 Pro"), then returns the secret. It returns
	// ErrCancelled when they cancel or fail, and ErrNotFound when nothing
	// is saved.
	Reveal(ctx context.Context, account, reason string) (string, error)
}

// Service is the keychain service (the Credential Manager's target
// prefix on Windows) Pinky's secrets are saved under.
const Service = "app.pinky.desktop"

// System returns the operating system's keychain, or a Store whose methods
// return ErrUnsupported where Pinky has none.
func System() Store { return system() }

// Supported reports whether a store can keep secrets on this system,
// without asking the person anything.
func Supported(s Store) bool {
	if v, ok := s.(interface{ Supported() bool }); ok {
		return v.Supported()
	}
	return true
}

// PINAccount is the account a phone's PIN is saved under.
func PINAccount(deviceID string) string { return "pin:" + deviceID }

// modelKeyPrefix starts the account of every API key of a model service.
const modelKeyPrefix = "model-key:"

// ModelKeyAccount is the account the API key of a model service
// ("anthropic", "openai") is saved under.
func ModelKeyAccount(service string) string { return modelKeyPrefix + service }

// ErrNotModelKey is what Get returns for an account that is not a model
// service's API key: every other secret, a PIN above all, goes through
// Reveal and the person's confirmation.
var ErrNotModelKey = errors.New("secret: only model API keys can be read without confirmation")

// Getter is a Store that can return a secret without asking the person to
// confirm. Only Pinky's API keys are read this way; PINs always go through
// Reveal.
type Getter interface {
	Get(ctx context.Context, account string) (string, error)
}

// Get returns a secret without asking the person, where the store can
// (where the system keeps Pinky's secrets locked, as Linux's Secret Service
// does, it may still ask to unlock them). It returns ErrNotFound when
// nothing is saved, ErrUnsupported for a store that can only Reveal, and
// ErrNotModelKey for an account ModelKeyAccount did not make.
func Get(ctx context.Context, s Store, account string) (string, error) {
	if !strings.HasPrefix(account, modelKeyPrefix) {
		return "", ErrNotModelKey
	}
	if g, ok := s.(Getter); ok {
		return g.Get(ctx, account)
	}
	return "", ErrUnsupported
}

// Memory is a Store in memory, for tests. Confirm stands in for the
// system's prompt; nil confirms.
type Memory struct {
	Confirm func(reason string) error

	mu sync.Mutex
	m  map[string]string
}

// Supported implements the optional method Supported looks for.
func (s *Memory) Supported() bool { return true }

func (s *Memory) Has(account string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[account]
	return ok
}

func (s *Memory) Save(account, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[account] = secret
	return nil
}

func (s *Memory) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, account)
	return nil
}

// Get implements Getter.
func (s *Memory) Get(ctx context.Context, account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[account]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (s *Memory) Reveal(ctx context.Context, account, reason string) (string, error) {
	s.mu.Lock()
	v, ok := s.m[account]
	s.mu.Unlock()
	if !ok {
		return "", ErrNotFound
	}
	if s.Confirm != nil {
		if err := s.Confirm(reason); err != nil {
			return "", err
		}
	}
	return v, nil
}

// unsupported is the Store of systems without a keychain Pinky can use.
type unsupported struct{}

func (unsupported) Supported() bool           { return false }
func (unsupported) Has(string) bool           { return false }
func (unsupported) Save(string, string) error { return ErrUnsupported }
func (unsupported) Delete(string) error       { return ErrUnsupported }
func (unsupported) Reveal(context.Context, string, string) (string, error) {
	return "", ErrUnsupported
}
func (unsupported) Get(context.Context, string) (string, error) { return "", ErrUnsupported }
