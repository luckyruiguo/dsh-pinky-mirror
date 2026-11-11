package agents

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestPathEnvRunsOnlyItsCommands: with PINKY_AGENTS_PATH, the app's tests
// find agents' commands only in its folders, and run no other command,
// such as open, which would open the person's real Claude Desktop.
func TestPathEnvRunsOnlyItsCommands(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	t.Setenv(PathEnv, bin)
	e := SystemEnv()
	if got := e.Path(); !slices.Equal(got, []string{bin}) {
		t.Errorf("search path %q, want %q", got, bin)
	}
	// None of these commands is anywhere, so that a broken check runs
	// nothing either: the first two are refused before they are looked
	// for, the last is looked for.
	ctx := context.Background()
	for _, name := range []string{filepath.Join(root, "elsewhere", "pinky-no-such-command"), "pinky-no-such-command"} {
		if _, err := e.Run(ctx, name); err == nil || !strings.Contains(err.Error(), PathEnv) {
			t.Errorf("%s, outside %s: %v", name, PathEnv, err)
		}
	}
	if _, err := e.Run(ctx, filepath.Join(bin, "pinky-no-such-command")); err == nil || strings.Contains(err.Error(), PathEnv) {
		t.Errorf("a command in %s: %v", PathEnv, err)
	}
}
