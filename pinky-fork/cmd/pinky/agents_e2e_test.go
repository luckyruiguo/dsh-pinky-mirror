//go:build e2e && !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky"
	"github.com/DIYgod/pinky/internal/agents"
)

// fakeClaude stands in for Claude Code's command: it logs its arguments
// and writes ~/.claude.json as claude mcp add and remove would.
const fakeClaude = `#!/bin/sh
echo "$@" >> "$HOME/claude-calls.txt"
case "$1 $2" in
"mcp add")
	shift 6
	printf '{"numStartups": 7, "mcpServers": {"pinky": {"type": "stdio", "command": "%s", "args": ["%s"], "env": {}}}}\n' "$1" "$2" > "$HOME/.claude.json"
	;;
"mcp remove")
	printf '{"numStartups": 7, "mcpServers": {}}\n' > "$HOME/.claude.json"
	;;
esac
`

// TestAgentSetupInTheApp drives the e2e build of the desktop app over
// MyGo's automation socket, with a home folder of its own that holds fake
// agents: Claude Code (a fake claude command), Codex (config.toml) and
// Cursor (mcp.json, with comments and another server). Nothing changes
// until a Connect is clicked; Connect adds Pinky by this app's path, keeps
// a copy of the file, leaves the rest of it as it was and installs the
// skill; Remove takes Pinky out again, and leaves the skill in
// ~/.agents/skills while Codex reads it. It opens windows, so it needs a display with the
// screen unlocked:
//
//	go test -tags e2e -run TestAgentSetupInTheApp ./cmd/pinky
func TestAgentSetupInTheApp(t *testing.T) {
	if testing.Short() {
		t.Skip("opens the app")
	}
	bin := filepath.Join(t.TempDir(), "pinky")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	self, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	home := pinkyHome(t)
	userHome := filepath.Join(home, "user")
	fakeBin := filepath.Join(home, "bin")
	cursorJSON := filepath.Join(userHome, ".cursor", "mcp.json")
	codexTOML := filepath.Join(userHome, ".codex", "config.toml")
	const cursorBefore = "{\n  // My servers\n  \"mcpServers\": {\n    \"github\": {\"command\": \"gh-mcp\"}\n  }\n}\n"
	const codexBefore = "model = \"gpt-6\" # mine\n"
	for path, data := range map[string]string{
		cursorJSON: cursorBefore, codexTOML: codexBefore, filepath.Join(fakeBin, "claude"): fakeClaude,
	} {
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	backend := newFakeBackend(t)
	app := startApp(t, bin, home, backend.URL, filepath.Join(home, "opened.txt"),
		"HOME="+userHome, agents.PathEnv+"="+fakeBin, "MYGO_LOCALE=en")
	win := app.window(t)

	// With no agent connected, the sidebar's card names the ones found,
	// and nothing has changed yet.
	app.waitText(t, win, "Pinky found Claude Code, Codex and Cursor on this computer.")
	unchanged := func() {
		t.Helper()
		if b, _ := os.ReadFile(cursorJSON); string(b) != cursorBefore {
			t.Fatalf("mcp.json changed before a click:\n%s", b)
		}
		if b, _ := os.ReadFile(codexTOML); string(b) != codexBefore {
			t.Fatalf("config.toml changed before a click:\n%s", b)
		}
		if _, err := os.Stat(filepath.Join(userHome, "claude-calls.txt")); err == nil {
			t.Fatal("claude ran before a click")
		}
	}
	unchanged()
	app.press(t, win, "Choose…")
	app.waitText(t, win, "Not connected")
	unchanged()

	// Cursor: Pinky's entry goes in, the rest stays, a copy is kept.
	app.press(t, win, "Connect Cursor")
	app.waitText(t, win, "Connected. Quit Cursor and open it again")
	got, _ := os.ReadFile(cursorJSON)
	if !strings.Contains(string(got), "// My servers") || !strings.Contains(string(got), `"github": {"command": "gh-mcp"}`) ||
		!strings.Contains(string(got), `"command": "`+self+`"`) {
		t.Fatalf("mcp.json:\n%s", got)
	}
	copies, _ := filepath.Glob(filepath.Join(home, "data", "agents", "backups", "cursor", "*-mcp.json"))
	if len(copies) != 1 {
		t.Fatalf("copies of mcp.json: %v", copies)
	}
	if b, _ := os.ReadFile(copies[0]); string(b) != cursorBefore {
		t.Errorf("the copy:\n%s", b)
	}
	shared := filepath.Join(userHome, ".agents", "skills", "pinky", "SKILL.md")
	if b, _ := os.ReadFile(shared); string(b) != pinky.Skill {
		t.Error("Cursor lacks Pinky's skill in ~/.agents/skills")
	}

	// Claude Code: through its own command, with its skill.
	app.press(t, win, "Connect Claude Code")
	app.waitText(t, win, "Start a new Claude Code session")
	calls, _ := os.ReadFile(filepath.Join(userHome, "claude-calls.txt"))
	if want := "mcp add --scope user pinky -- " + self + " mcp\n"; string(calls) != want {
		t.Errorf("claude ran %q, want %q", calls, want)
	}
	if b, _ := os.ReadFile(filepath.Join(userHome, ".claude", "skills", "pinky", "SKILL.md")); string(b) != pinky.Skill {
		t.Error("Claude Code lacks Pinky's skill")
	}

	// Codex, without its command: config.toml gets the table.
	app.press(t, win, "Connect Codex")
	app.waitText(t, win, "Start a new Codex session")
	if b, _ := os.ReadFile(codexTOML); !strings.HasPrefix(string(b), codexBefore) ||
		!strings.Contains(string(b), "[mcp_servers.pinky]\ncommand = \""+self+"\"\nargs = [\"mcp\"]\n") {
		t.Errorf("config.toml:\n%s", b)
	}

	// Remove takes Pinky out of Cursor: the file is as it was.
	app.press(t, win, "Remove Pinky from Cursor")
	app.waitText(t, win, "Removed.")
	if b, _ := os.ReadFile(cursorJSON); string(b) != cursorBefore {
		t.Errorf("mcp.json after Remove:\n%s", b)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Error("removing Cursor took the skill that Codex reads too")
	}

	// The next start finds them as they are: two connected, one not.
	run(t, bin, home, "quit")
	app.wait(t)
	app = startApp(t, bin, home, backend.URL, filepath.Join(home, "opened.txt"),
		"HOME="+userHome, agents.PathEnv+"="+fakeBin, "MYGO_LOCALE=en")
	win = app.window(t)
	app.waitText(t, win, "Pixel 9 (fake)")
	time.Sleep(time.Second) // the agents are read off the main thread
	if app.hasText(t, win, "Pinky found") {
		t.Error("the card shows with agents connected")
	}
	run(t, bin, home, "quit")
	app.wait(t)
}
