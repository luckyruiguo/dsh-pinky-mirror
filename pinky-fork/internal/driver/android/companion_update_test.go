package android

import (
	"context"
	"errors"
	"testing"

	"github.com/DIYgod/pinky/internal/driver/android/companion"
)

// checksRunner answers installAsks' one shell command.
type checksRunner struct {
	out string
	err error
}

func (r checksRunner) Shell(context.Context, string) (string, error) { return r.out, r.err }
func (r checksRunner) Forward(context.Context, string) (string, error) {
	return "", errors.New("unused")
}
func (r checksRunner) RemoveForward(context.Context, string) error { return nil }
func (r checksRunner) Install(context.Context, string) error       { return nil }

func TestInstallAsks(t *testing.T) {
	for _, c := range []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"no Play services (LineageOS, AOSP emulators)", "gms=0\nadb=null\nverifier=1\n", nil, false},
		{"Play Protect with the default", "gms=1\nadb=null\nverifier=null\n", nil, true},
		{"Play Protect checks USB installs", "gms=1\nadb=1\nverifier=1\n", nil, true},
		{"USB install checks off", "gms=1\nadb=0\nverifier=1\n", nil, false},
		{"the verifier off", "gms=1\nadb=1\nverifier=0\n", nil, false},
		{"unreadable", "", errors.New("adb: closed"), true},
	} {
		asks, why := installAsks(context.Background(), checksRunner{out: c.out, err: c.err})
		if asks != c.want || (asks && why == "") {
			t.Errorf("%s: installAsks = %v, %q", c.name, asks, why)
		}
	}
}

func TestVersionLine(t *testing.T) {
	for v, want := range map[companion.Version]string{
		{Name: "0.1.0", Code: 75}: "0.1.0 (75)",
		{Code: 75}:                "build 75",
		{Name: "0.1.0"}:           "0.1.0",
	} {
		if got := versionLine(v); got != want {
			t.Errorf("versionLine(%+v) = %q, want %q", v, got, want)
		}
	}
}
