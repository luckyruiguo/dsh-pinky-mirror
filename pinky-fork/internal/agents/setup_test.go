package agents

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testSkill = "---\nname: pinky\ndescription: Use the phone.\n---\n\n# Pinky\n"

// testSetup is a Setup over a home folder of the test's own: no test reads
// or changes the person's agents.
type testSetup struct {
	*Setup
	home string
	bin  string     // the folder of fake commands, on the search path
	ran  [][]string // the commands run, with their arguments
	// effect is what a fake command does, given its name and arguments.
	effect func(name string, args []string) ([]byte, error)
}

func newTestSetup(t *testing.T) *testSetup {
	t.Helper()
	root := t.TempDir()
	ts := &testSetup{home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin")}
	for _, d := range []string{ts.home, ts.bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	clock := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	env := &Env{
		GOOS: "darwin", Home: ts.home,
		Getenv: func(string) string { return "" },
		Path:   func() []string { return []string{ts.bin} },
		Apps:   []string{filepath.Join(root, "Applications")},
		Now: func() time.Time {
			clock = clock.Add(time.Second)
			return clock
		},
	}
	env.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ts.ran = append(ts.ran, append([]string{filepath.Base(name)}, args...))
		if ts.effect != nil {
			return ts.effect(filepath.Base(name), args)
		}
		return nil, nil
	}
	ts.Setup = &Setup{
		Env:    env,
		Server: Server{Command: "/Applications/Pinky.app/Contents/MacOS/Pinky", Args: []string{"mcp"}},
		Skill:  testSkill,
		Dir:    filepath.Join(root, "pinky"),
		Bundle: BundleOptions{Version: "2026.1108.9", Icon: []byte("\x89PNG")},
	}
	return ts
}

func (ts *testSetup) write(t *testing.T, rel, data string) string {
	t.Helper()
	p := filepath.Join(ts.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (ts *testSetup) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ts.home, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeCommand puts an executable of that name on the search path.
func (ts *testSetup) fakeCommand(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ts.bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (ts *testSetup) status(t *testing.T, id string) Status {
	t.Helper()
	for _, st := range ts.Find(context.Background()) {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("no agent %q", id)
	return Status{}
}

func (ts *testSetup) backups(t *testing.T, id string) []string {
	dir := filepath.Join(ts.Dir, "backups", id)
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out = append(out, string(b))
	}
	return out
}

func TestFindNothing(t *testing.T) {
	ts := newTestSetup(t)
	for _, st := range ts.Find(context.Background()) {
		if st.Found || st.State != NotConnected || st.Problem != "" {
			t.Errorf("%s in an empty home: %+v", st.ID, st)
		}
	}
	if len(ts.ran) != 0 {
		t.Errorf("ran %v to find agents", ts.ran)
	}
}

func TestConnectCursor(t *testing.T) {
	ts := newTestSetup(t)
	const mine = "{\n  // mine\n  \"mcpServers\": {\n    \"github\": {\"command\": \"gh\"}\n  }\n}\n"
	ts.write(t, ".cursor/mcp.json", mine)
	st := ts.status(t, "cursor")
	if !st.Found || st.State != NotConnected || st.Settings != filepath.Join(ts.home, ".cursor", "mcp.json") {
		t.Fatalf("before: %+v", st)
	}
	res, err := ts.Connect(context.Background(), "cursor")
	if err != nil {
		t.Fatal(err)
	}
	got := ts.read(t, ".cursor/mcp.json")
	if !strings.Contains(got, "// mine") || !strings.Contains(got, `"github": {"command": "gh"}`) ||
		!strings.Contains(got, `"command": "/Applications/Pinky.app/Contents/MacOS/Pinky"`) {
		t.Errorf("mcp.json:\n%s", got)
	}
	if b := ts.backups(t, "cursor"); len(b) != 1 || b[0] != mine || len(res.Backups) != 1 {
		t.Errorf("backups %q, result %+v", b, res)
	}
	if skill := ts.read(t, ".agents/skills/pinky/SKILL.md"); skill != testSkill {
		t.Errorf("skill %q", skill)
	}
	if st := ts.status(t, "cursor"); st.State != Connected {
		t.Errorf("after: %+v", st)
	}
	// Connecting again changes nothing and keeps no copy.
	if _, err := ts.Connect(context.Background(), "cursor"); err != nil {
		t.Fatal(err)
	}
	if b := ts.backups(t, "cursor"); len(b) != 1 {
		t.Errorf("%d backups after connecting twice", len(b))
	}
	if _, err := ts.Remove(context.Background(), "cursor"); err != nil {
		t.Fatal(err)
	}
	if got := ts.read(t, ".cursor/mcp.json"); got != mine {
		t.Errorf("after Remove:\n%s\nwant\n%s", got, mine)
	}
	if exists(filepath.Join(ts.home, ".agents", "skills", "pinky")) {
		t.Error("the skill stayed")
	}
	if st := ts.status(t, "cursor"); st.State != NotConnected {
		t.Errorf("after Remove: %+v", st)
	}
}

func TestConnectRefusesABrokenFile(t *testing.T) {
	ts := newTestSetup(t)
	const broken = "{\"mcpServers\": {\"github\": {\"command\": \"gh\"}\n"
	ts.write(t, ".cursor/mcp.json", broken)
	if st := ts.status(t, "cursor"); st.Problem == "" {
		t.Errorf("no problem with a broken file: %+v", st)
	}
	if _, err := ts.Connect(context.Background(), "cursor"); err == nil {
		t.Fatal("connected through a broken file")
	}
	if got := ts.read(t, ".cursor/mcp.json"); got != broken {
		t.Errorf("the broken file changed:\n%s", got)
	}
	if b := ts.backups(t, "cursor"); len(b) != 0 {
		t.Errorf("backups of a file left alone: %q", b)
	}
}

func TestOutdatedEntries(t *testing.T) {
	ts := newTestSetup(t)
	// A path in JSON, whose backslashes on Windows need escapes.
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	gone := filepath.Join(ts.home, "Downloads", "Pinky.app", "Contents", "MacOS", "Pinky")
	other := ts.write(t, "dev/pinky", "")
	cases := map[string]Why{
		`{"command": "pinky", "args": ["mcp"]}`:                       WhyCoreutils,
		`{"command": ` + quote(gone) + `, "args": ["mcp"]}`:           WhyMissing,
		`{"command": ` + quote(other) + `, "args": ["mcp"]}`:          WhyOtherPinky,
		`{"command": "npx", "args": ["some-mcp"]}`:                    WhyOtherCommand,
		`{"command": "/Applications/Pinky.app/Contents/MacOS/Pinky"}`: WhyOtherCommand,
	}
	for entry, why := range cases {
		ts.write(t, ".codeium/windsurf/mcp_config.json", `{"mcpServers": {"pinky": `+entry+`}}`)
		st := ts.status(t, "windsurf")
		if st.State != Outdated || st.Why != why {
			t.Errorf("%s: %v, %v; want Outdated, %v", entry, st.State, st.Why, why)
		}
		if _, err := ts.Connect(context.Background(), "windsurf"); err != nil {
			t.Fatal(err)
		}
		if st := ts.status(t, "windsurf"); st.State != Connected {
			t.Errorf("%s, connected again: %+v", entry, st)
		}
	}
	// pinkyctl on the search path works too.
	ts.fakeCommand(t, "pinkyctl")
	ts.write(t, ".codeium/windsurf/mcp_config.json", `{"mcpServers": {"pinky": {"command": "pinkyctl", "args": ["mcp"]}}}`)
	if st := ts.status(t, "windsurf"); st.State != Connected {
		t.Errorf("pinkyctl: %+v", st)
	}
	// An old skill is out of date.
	ts.write(t, ".codeium/windsurf/skills/pinky/SKILL.md", "---\nname: pinky\n---\nold\n")
	if st := ts.status(t, "windsurf"); st.State != Outdated || st.Why != WhySkill {
		t.Errorf("old skill: %+v", st)
	}
}

func TestClaudeCodeThroughItsCommand(t *testing.T) {
	ts := newTestSetup(t)
	ts.fakeCommand(t, "claude")
	ts.write(t, ".claude.json", `{"numStartups": 3, "mcpServers": {"pinky": {"type": "stdio", "command": "pinky", "args": ["mcp"]}}}`)
	// The fake claude changes ~/.claude.json as the real one does.
	ts.effect = func(name string, args []string) ([]byte, error) {
		file := filepath.Join(ts.home, ".claude.json")
		data, _ := os.ReadFile(file)
		var out []byte
		var err error
		switch strings.Join(args[:2], " ") {
		case "mcp add":
			out, err = setJSONMember(data, []string{"mcpServers"}, args[4], object{{"type", "stdio"}, {"command", args[6]}, {"args", args[7:]}, {"env", object{}}})
		case "mcp remove":
			out, _, err = deleteJSONMember(data, []string{"mcpServers"}, args[4])
		}
		if err != nil {
			return nil, err
		}
		return nil, os.WriteFile(file, out, 0o600)
	}
	if st := ts.status(t, "claude-code"); st.State != Outdated || st.Why != WhyCoreutils {
		t.Fatalf("before: %+v", st)
	}
	if _, err := ts.Connect(context.Background(), "claude-code"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"claude", "mcp", "remove", "--scope", "user", "pinky"},
		{"claude", "mcp", "add", "--scope", "user", "pinky", "--", "/Applications/Pinky.app/Contents/MacOS/Pinky", "mcp"},
	}
	if strings.Join(flat(ts.ran), "|") != strings.Join(flat(want), "|") {
		t.Errorf("ran %q, want %q", ts.ran, want)
	}
	if st := ts.status(t, "claude-code"); st.State != Connected {
		t.Errorf("after: %+v", st)
	}
	if ts.read(t, ".claude/skills/pinky/SKILL.md") != testSkill {
		t.Error("no skill")
	}
	if b := ts.backups(t, "claude-code"); len(b) != 1 || !strings.Contains(b[0], `"command": "pinky"`) {
		t.Errorf("backups %q", b)
	}
	ts.ran = nil
	if _, err := ts.Remove(context.Background(), "claude-code"); err != nil {
		t.Fatal(err)
	}
	if len(ts.ran) != 1 || ts.ran[0][2] != "remove" {
		t.Errorf("ran %q", ts.ran)
	}
	if st := ts.status(t, "claude-code"); st.State != NotConnected {
		t.Errorf("after Remove: %+v", st)
	}
	// A command that fails says why, and changes nothing.
	ts.effect = func(string, []string) ([]byte, error) { return []byte("MCP server pinky already exists"), os.ErrExist }
	if _, err := ts.Connect(context.Background(), "claude-code"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("a failed claude mcp add: %v", err)
	}
}

func flat(cmds [][]string) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func TestRemoveFailsWhenSettingsCannotBeRead(t *testing.T) {
	ts := newTestSetup(t)
	ts.fakeCommand(t, "claude")
	file := filepath.Join(ts.home, ".claude.json")
	ts.write(t, ".claude.json", `{"numStartups": 3, "mcpServers": {"pinky": {"type": "stdio", "command": "pinky", "args": ["mcp"]}}}`)
	claudeCLI := func(name string, args []string) ([]byte, error) {
		data, _ := os.ReadFile(file)
		var out []byte
		var err error
		switch strings.Join(args[:2], " ") {
		case "mcp add":
			out, err = setJSONMember(data, []string{"mcpServers"}, args[4], object{{"type", "stdio"}, {"command", args[6]}, {"args", args[7:]}, {"env", object{}}})
		case "mcp remove":
			out, _, err = deleteJSONMember(data, []string{"mcpServers"}, args[4])
		}
		if err != nil {
			return nil, err
		}
		return nil, os.WriteFile(file, out, 0o600)
	}
	ts.effect = claudeCLI
	if _, err := ts.Connect(context.Background(), "claude-code"); err != nil {
		t.Fatal(err)
	}
	ts.effect = func(name string, args []string) ([]byte, error) {
		if strings.Join(args[:2], " ") == "mcp remove" {
			return nil, os.WriteFile(file, []byte("{"), 0o600)
		}
		return nil, nil
	}
	_, err := ts.Remove(context.Background(), "claude-code")
	if err == nil {
		t.Fatal("Remove succeeded though the settings file could not be read afterward")
	}
}

func TestCodexWithoutItsCommand(t *testing.T) {
	ts := newTestSetup(t)
	const mine = "model = \"gpt-6\"\n\n[mcp_servers.github]\ncommand = \"gh\"\n"
	ts.write(t, ".codex/config.toml", mine)
	if _, err := ts.Connect(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	got := ts.read(t, ".codex/config.toml")
	if !strings.HasPrefix(got, mine) || !strings.Contains(got, "[mcp_servers.pinky]\ncommand = \"/Applications/Pinky.app/Contents/MacOS/Pinky\"\nargs = [\"mcp\"]\n") {
		t.Errorf("config.toml:\n%s", got)
	}
	if st := ts.status(t, "codex"); st.State != Connected {
		t.Errorf("after: %+v", st)
	}
	if _, err := ts.Remove(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	if got := ts.read(t, ".codex/config.toml"); got != mine {
		t.Errorf("after Remove:\n%s", got)
	}
}

func TestSymlinkedSettingsStayALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	ts := newTestSetup(t)
	real := ts.write(t, "dotfiles/mcp.json", "{}\n")
	os.MkdirAll(filepath.Join(ts.home, ".cursor"), 0o700)
	link := filepath.Join(ts.home, ".cursor", "mcp.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Connect(context.Background(), "cursor"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link became a file: %v", err)
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), "pinky") {
		t.Errorf("the linked file: %s", b)
	}
}

func TestBackupsAreFew(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	for i := 0; i < keepBackups+5; i++ {
		if _, err := backup(dir, "cursor", "/x/mcp.json", []byte{byte('a' + i)}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "cursor"))
	if len(entries) != keepBackups {
		t.Fatalf("%d backups kept", len(entries))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "cursor", entries[0].Name())); string(b) != string(rune('a'+5)) {
		t.Errorf("the oldest kept is %q", b)
	}
}

func TestSkillOfSomeoneElseStays(t *testing.T) {
	dir := t.TempDir()
	other := "---\nname: pinky-notes\n---\nmine\n"
	os.MkdirAll(filepath.Join(dir, "pinky"), 0o700)
	os.WriteFile(filepath.Join(dir, "pinky", "SKILL.md"), []byte(other), 0o600)
	if err := installSkill(dir, testSkill); err == nil {
		t.Error("replaced someone else's skill")
	}
	if err := removeSkill(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pinky", "SKILL.md")); string(b) != other {
		t.Errorf("removed someone else's skill: %q", b)
	}
}

func TestPathsByOS(t *testing.T) {
	win := &Env{GOOS: "windows", Home: `C:\Users\jane`, Getenv: func(k string) string {
		return map[string]string{"APPDATA": `C:\Users\jane\AppData\Roaming`}[k]
	}}
	if got := filepath.Join(userConfig(win), "Claude"); !strings.HasSuffix(got, filepath.Join("Roaming", "Claude")) {
		t.Errorf("Claude Desktop on Windows: %s", got)
	}
	// Paths the computer the test runs on takes as absolute, as an
	// XDG_CONFIG_HOME must be: /home/jane is not one on Windows.
	root := t.TempDir()
	lin := &Env{GOOS: "linux", Home: filepath.Join(root, "jane"), Getenv: func(k string) string {
		return map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "xdg")}[k]
	}}
	if got := copilotPlaces(lin)[1].file; got != filepath.Join(root, "xdg", "Code", "User", "mcp.json") {
		t.Errorf("VS Code on Linux: %s", got)
	}
	mac := &Env{GOOS: "darwin", Home: "/Users/jane", Getenv: func(k string) string {
		return map[string]string{"CODEX_HOME": "/Users/jane/codex", "CLAUDE_CONFIG_DIR": "/Users/jane/cc"}[k]
	}}
	if codexHome(mac) != "/Users/jane/codex" || claudeJSON(mac) != filepath.Join("/Users/jane/cc", ".claude.json") {
		t.Errorf("CODEX_HOME %s, CLAUDE_CONFIG_DIR %s", codexHome(mac), claudeJSON(mac))
	}
}

func TestSharedSkillStaysWhileUsed(t *testing.T) {
	ts := newTestSetup(t)
	ts.write(t, ".cursor/mcp.json", "{}\n")
	ts.write(t, ".codex/config.toml", "")
	for _, id := range []string{"cursor", "codex"} {
		if _, err := ts.Connect(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	skill := filepath.Join(ts.home, ".agents", "skills", "pinky", "SKILL.md")
	if _, err := ts.Remove(context.Background(), "cursor"); err != nil {
		t.Fatal(err)
	}
	if !exists(skill) {
		t.Fatal("removing Cursor took the skill Codex reads")
	}
	if _, err := ts.Remove(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	if exists(skill) {
		t.Error("the skill stayed after the last agent that reads it")
	}
}

func TestClaudeDesktop(t *testing.T) {
	ts := newTestSetup(t)
	data := filepath.Join(ts.home, "Library", "Application Support", "Claude")
	const prefs = "{\n  \"preferences\": {\n    \"sidebarMode\": \"chat\"\n  }\n}\n"
	ts.write(t, "Library/Application Support/Claude/claude_desktop_config.json", prefs)
	if st := ts.status(t, "claude-desktop"); !st.Found || st.State != NotConnected || !st.Opens {
		t.Fatalf("before: %+v", st)
	}
	// Without an entry, Connect opens Pinky's extension in Claude, which
	// starts this Pinky, and leaves Claude's settings alone.
	res, err := ts.Connect(context.Background(), "claude-desktop")
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(ts.Dir, "Pinky.mcpb")
	if res.Opened != bundle || len(ts.ran) != 1 || strings.Join(ts.ran[0], " ") != "open -b com.anthropic.claudefordesktop "+bundle {
		t.Fatalf("result %+v, ran %q", res, ts.ran)
	}
	if got := ts.read(t, "Library/Application Support/Claude/claude_desktop_config.json"); got != prefs {
		t.Errorf("Claude's settings changed:\n%s", got)
	}
	z, err := zip.OpenReader(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var man struct {
		Server struct {
			MCPConfig struct {
				Command string
				Env     map[string]string
			} `json:"mcp_config"`
		}
	}
	for _, f := range z.File {
		if f.Name == "manifest.json" {
			r, _ := f.Open()
			json.NewDecoder(r).Decode(&man)
			r.Close()
		}
	}
	if man.Server.MCPConfig.Env["PINKY"] != ts.Server.Command {
		t.Errorf("the bundle starts %+v", man.Server.MCPConfig)
	}
	// Installed in Claude: connected, as an extension only Claude removes.
	ext := filepath.Join(data, "Claude Extensions", "local.mcpb.diygod.pinky")
	os.MkdirAll(ext, 0o700)
	os.WriteFile(filepath.Join(ext, "manifest.json"), []byte(`{"name": "pinky", "version": "2026.1108.9"}`), 0o600)
	if st := ts.status(t, "claude-desktop"); st.State != Connected || !st.Extension {
		t.Errorf("installed: %+v", st)
	}
	if _, err := ts.Remove(context.Background(), "claude-desktop"); !errors.Is(err, ErrExtension) {
		t.Errorf("Remove of the extension: %v", err)
	}
	ts.write(t, "Library/Application Support/Claude/Claude Extensions Settings/local.mcpb.diygod.pinky.json", `{"isEnabled": false}`)
	if st := ts.status(t, "claude-desktop"); st.State != Outdated || st.Why != WhyOff {
		t.Errorf("turned off: %+v", st)
	}
	// An entry in its settings counts first, and Connect sets it right
	// there, for when Claude opens next.
	os.RemoveAll(filepath.Join(data, "Claude Extensions"))
	ts.write(t, "Library/Application Support/Claude/claude_desktop_config.json",
		"{\n  \"mcpServers\": {\n    \"pinky\": {\"command\": \"pinky\", \"args\": [\"mcp\"]}\n  },\n  \"preferences\": {}\n}\n")
	ts.ran = nil
	res, err = ts.Connect(context.Background(), "claude-desktop")
	if err != nil {
		t.Fatal(err)
	}
	if res.Opened != "" || res.Restart != RestartApp || len(ts.ran) != 0 {
		t.Errorf("result %+v, ran %q", res, ts.ran)
	}
	if st := ts.status(t, "claude-desktop"); st.State != Connected || st.Extension {
		t.Errorf("after: %+v", st)
	}
}

func TestClaudeDesktopOnWindows(t *testing.T) {
	dir := t.TempDir()
	e := &Env{GOOS: "windows", Home: dir, Getenv: func(k string) string {
		return map[string]string{"APPDATA": filepath.Join(dir, "Roaming"), "LOCALAPPDATA": filepath.Join(dir, "Local")}[k]
	}}
	if got := claudeDesktopData(e); got != filepath.Join(dir, "Roaming", "Claude") {
		t.Errorf("nothing installed: %s", got)
	}
	// The installer's folder, and an MSIX one whose settings changed last.
	old := filepath.Join(dir, "Roaming", "Claude", "claude_desktop_config.json")
	msix := filepath.Join(dir, "Local", "Claude-Data", "claude_pzs8sxrjxfjjc", "claude_desktop_config.json")
	for i, f := range []string{old, msix} {
		os.MkdirAll(filepath.Dir(f), 0o700)
		os.WriteFile(f, []byte("{}"), 0o600)
		ts := time.Date(2026, 11, 1+i, 0, 0, 0, 0, time.UTC)
		os.Chtimes(f, ts, ts)
	}
	if got := claudeDesktopData(e); got != filepath.Dir(msix) {
		t.Errorf("with MSIX settings: %s", got)
	}
}

func TestVSCodeKeepsItsOldEntry(t *testing.T) {
	ts := newTestSetup(t)
	ts.write(t, "Library/Application Support/Code/User/mcp.json", "{\n  // mine\n  \"servers\": {\n    \"pinky\": {\"type\": \"stdio\", \"command\": \"pinky\", \"args\": [\"mcp\"]}\n  }\n}\n")
	st := ts.status(t, "vscode")
	if !st.Found || st.State != Outdated || !strings.HasSuffix(st.Settings, filepath.Join("Code", "User", "mcp.json")) {
		t.Fatalf("before: %+v", st)
	}
	if _, err := ts.Connect(context.Background(), "vscode"); err != nil {
		t.Fatal(err)
	}
	if got := ts.read(t, "Library/Application Support/Code/User/mcp.json"); !strings.Contains(got, "// mine") || !strings.Contains(got, ts.Server.Command) {
		t.Errorf("mcp.json:\n%s", got)
	}
	if exists(filepath.Join(ts.home, ".copilot", "mcp-config.json")) {
		t.Error("a second entry, in Copilot's settings")
	}
	// Without one, Pinky goes into Copilot's global settings.
	if _, err := ts.Remove(context.Background(), "vscode"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Connect(context.Background(), "vscode"); err != nil {
		t.Fatal(err)
	}
	got := ts.read(t, ".copilot/mcp-config.json")
	if !strings.Contains(got, `"type": "local"`) || !strings.Contains(got, `"tools": [`) {
		t.Errorf("mcp-config.json:\n%s", got)
	}
	// Copilot CLI reads the same file.
	if st := ts.status(t, "copilot"); st.State != Connected || st.Found {
		t.Errorf("Copilot CLI, not installed: %+v", st)
	}
}

func TestWindowsSkipsAppAliases(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(dir, "Microsoft", "WindowsApps")
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{alias, bin} {
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "claude.exe"), nil, 0o700)
	}
	e := &Env{GOOS: "windows", Home: dir, Path: func() []string { return []string{alias, bin} }}
	if got := e.lookPath("claude"); got != filepath.Join(bin, "claude.exe") {
		t.Errorf("lookPath(claude) = %s", got)
	}
}

func TestRefreshSkills(t *testing.T) {
	ts := newTestSetup(t)
	const old = "---\nname: pinky\n---\nold guide\n"
	const mine = "---\nname: pinky-notes\n---\nmy own\n"
	ts.write(t, ".claude/skills/pinky/SKILL.md", old)
	ts.write(t, ".agents/skills/pinky/SKILL.md", old)
	ts.write(t, ".codeium/windsurf/skills/pinky/SKILL.md", mine)

	files, err := ts.RefreshSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(ts.home, ".claude", "skills", "pinky", "SKILL.md"),
		filepath.Join(ts.home, ".agents", "skills", "pinky", "SKILL.md"),
	}
	if strings.Join(files, "\n") != strings.Join(want, "\n") {
		t.Errorf("rewrote %q, want %q (the shared folder once)", files, want)
	}
	for _, p := range []string{".claude/skills/pinky/SKILL.md", ".agents/skills/pinky/SKILL.md"} {
		if got := ts.read(t, p); got != testSkill {
			t.Errorf("%s: %q", p, got)
		}
	}
	if got := ts.read(t, ".codeium/windsurf/skills/pinky/SKILL.md"); got != mine {
		t.Errorf("someone else's skill changed: %q", got)
	}
	// Folders without Pinky's skill get none.
	for _, p := range []string{".copilot/skills", ".config/devin/skills"} {
		if exists(filepath.Join(ts.home, p)) {
			t.Errorf("%s was made", p)
		}
	}

	// Current copies are not written again.
	files, err = ts.RefreshSkills(context.Background())
	if err != nil || len(files) != 0 {
		t.Errorf("second refresh: %q, %v", files, err)
	}
}

func TestRefreshSkillsWithoutSkill(t *testing.T) {
	ts := newTestSetup(t)
	ts.Skill = ""
	ts.write(t, ".claude/skills/pinky/SKILL.md", "---\nname: pinky\n---\nold\n")
	if files, err := ts.RefreshSkills(context.Background()); err != nil || len(files) != 0 {
		t.Errorf("refreshed without a skill: %q, %v", files, err)
	}
}

func TestRefreshedSkillIsConnected(t *testing.T) {
	ts := newTestSetup(t)
	if _, err := ts.Connect(context.Background(), "cursor"); err != nil {
		t.Fatal(err)
	}
	ts.write(t, ".agents/skills/pinky/SKILL.md", "---\nname: pinky\n---\nold\n")
	if st := ts.status(t, "cursor"); st.State != Outdated || st.Why != WhySkill {
		t.Fatalf("old skill: %+v", st)
	}
	if _, err := ts.RefreshSkills(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := ts.status(t, "cursor"); st.State != Connected {
		t.Errorf("after the refresh: %+v", st)
	}
}
