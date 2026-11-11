package adb

import (
	"testing"
)

// TestOwnCommandLine: a server Pinky started runs an adb by its full path
// with the pipe at 3; one adb's own start-server started runs "adb" with
// the pipe at another number, and another port's is not the one asked for.
func TestOwnCommandLine(t *testing.T) {
	for _, tc := range []struct {
		line string
		own  bool
	}{
		{"/Users/me/Library/Android/sdk/platform-tools/adb -L tcp:5037 fork-server server --reply-fd 3", true},
		{"/Users/me/Library/Caches/Pinky/adb/64df412b2df9aab8/adb -L tcp:5037 fork-server server --reply-fd 3", true},
		{"/Users/me/Library/Application Support/My SDK/adb -L tcp:5037 fork-server server --reply-fd 3", true},
		{"/opt/homebrew/bin/adb -L tcp:5037 fork-server server --reply-fd 3", true},
		{"adb -L tcp:5037 fork-server server --reply-fd 4", false}, // adb start-server
		{"adb -L tcp:5037 fork-server server --reply-fd 3", false}, // a name, not a path
		{"/usr/bin/adb -L tcp:5037 fork-server server --reply-fd 4", false},
		{"/usr/bin/adb -L tcp:5038 fork-server server --reply-fd 3", false}, // another port
		{"/usr/bin/adb -L tcp:5037 fork-server server --reply-fd 3 --one-device x", false},
		{"/usr/bin/scrcpy -L tcp:5037 fork-server server --reply-fd 3", false},
		{"/usr/bin/adb -P 5037 start-server", false},
	} {
		if got := ownCommandLine(tc.line, "5037"); got != tc.own {
			t.Errorf("ownCommandLine(%q) = %v, want %v", tc.line, got, tc.own)
		}
	}
}

func TestParseProcesses(t *testing.T) {
	out := `    1 /sbin/launchd
57274 adb -L tcp:5037 fork-server server --reply-fd 4
  812 /Users/me/sdk/adb -L tcp:5038 fork-server server --reply-fd 3
 9001 /Users/me/My SDK/adb -L tcp:5037 fork-server server --reply-fd 3
 9002 grep -L tcp:5037 fork-server server
x /bin/zsh
`
	got := parseProcesses(out, "5037")
	if len(got) != 2 || got[0].pid != 57274 || got[1].pid != 9001 || got[1].command != "/Users/me/My SDK/adb -L tcp:5037 fork-server server --reply-fd 3" {
		t.Fatalf("parseProcesses = %+v", got)
	}
}
