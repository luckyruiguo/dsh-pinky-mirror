//go:build !darwin && !windows && !linux

package secret

// system is replaced per platform (Keychain with Touch ID on macOS).
func system() Store { return unsupported{} }
