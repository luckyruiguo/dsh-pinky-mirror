package tailnet

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestRunCommandTerminal(t *testing.T) {
	// Inspect the actual child environment without a shell, which can set
	// its own TERM and hide the missing variable in a Finder launch.
	t.Setenv("PINKY_TEST_INHERITED", "kept")
	for _, tt := range []struct {
		name, term, want string
		unset            bool
	}{
		{name: "unset", unset: true, want: "dumb"},
		{name: "empty", want: "dumb"},
		{name: "existing", term: "xterm-256color", want: "xterm-256color"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TERM", tt.term)
			if tt.unset {
				if err := os.Unsetenv("TERM"); err != nil {
					t.Fatal(err)
				}
			}
			out, err := runCommand(context.Background(), "/usr/bin/env")
			if err != nil {
				t.Fatal(err)
			}
			env := strings.Split(strings.TrimSpace(string(out)), "\n")
			var terms []string
			for _, v := range env {
				if strings.HasPrefix(v, "TERM=") {
					terms = append(terms, v)
				}
			}
			if !slices.Equal(terms, []string{"TERM=" + tt.want}) {
				t.Fatalf("child TERM = %q; want %q", terms, tt.want)
			}
			if !slices.Contains(env, "PINKY_TEST_INHERITED=kept") {
				t.Fatal("child did not inherit the parent environment")
			}
			if got, set := os.LookupEnv("TERM"); got != tt.term || set == tt.unset {
				t.Fatalf("parent TERM changed to %q (set: %v)", got, set)
			}
		})
	}
}
