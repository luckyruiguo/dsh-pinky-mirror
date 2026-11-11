package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// The agents Pinky knows, in the order Settings › Agents lists them. Where
// each keeps its MCP servers and skills is from its documentation, as of
// November 2026; docs/mcp.md has the table.

// mcpEntry is the usual entry: {"command": …, "args": […]}.
func mcpEntry(srv Server) any {
	return object{{"command", srv.Command}, {"args", srv.Args}}
}

// stdioEntry is mcpEntry with its type, as VS Code's mcp.json has it.
func stdioEntry(srv Server) any {
	return object{{"type", "stdio"}, {"command", srv.Command}, {"args", srv.Args}}
}

// claudeEntry is what claude mcp add writes.
func claudeEntry(srv Server) any {
	return object{{"type", "stdio"}, {"command", srv.Command}, {"args", srv.Args}, {"env", object{}}}
}

// copilotEntry is what Copilot's global settings take: a local server, all
// of its tools.
func copilotEntry(srv Server) any {
	return object{{"type", "local"}, {"command", srv.Command}, {"args", srv.Args}, {"env", object{}}, {"tools", []string{"*"}}}
}

func fixed(name string) func(*Env) string { return func(*Env) string { return name } }

// userConfig is the folder where apps keep their settings: Application
// Support on macOS, %APPDATA% on Windows, ~/.config (XDG_CONFIG_HOME) on
// Linux.
func userConfig(e *Env) string {
	switch e.GOOS {
	case "darwin":
		return filepath.Join(e.Home, "Library", "Application Support")
	case "windows":
		return appData(e)
	}
	return xdgConfig(e)
}

// xdgConfig is ~/.config, or XDG_CONFIG_HOME.
func xdgConfig(e *Env) string {
	if x := e.getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return x
	}
	return filepath.Join(e.Home, ".config")
}

func appData(e *Env) string {
	if a := e.getenv("APPDATA"); a != "" {
		return a
	}
	return filepath.Join(e.Home, "AppData", "Roaming")
}

func localAppData(e *Env) string {
	if l := e.getenv("LOCALAPPDATA"); l != "" {
		return l
	}
	return filepath.Join(e.Home, "AppData", "Local")
}

// hasApp reports whether a Mac application is installed, by one of its
// bundles' names.
func hasApp(e *Env, names ...string) bool {
	for _, dir := range e.Apps {
		for _, name := range names {
			if exists(filepath.Join(dir, name+".app")) {
				return true
			}
		}
	}
	return false
}

func home(parts ...string) func(e *Env) string {
	return func(e *Env) string { return filepath.Join(append([]string{e.Home}, parts...)...) }
}

// sharedSkills is ~/.agents/skills, the folder of skills that most agents
// read (agentskills.io): Codex, Cursor, VS Code and Copilot CLI, Gemini
// CLI, Devin. Claude Code reads only its own.
func sharedSkills(e *Env) string { return filepath.Join(e.Home, ".agents", "skills") }

// --- Claude Code ---

// claudeDir is Claude Code's folder, ~/.claude or CLAUDE_CONFIG_DIR.
func claudeDir(e *Env) string {
	if d := e.getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(e.Home, ".claude")
}

// claudeJSON holds Claude Code's user-scope MCP servers.
func claudeJSON(e *Env) string {
	if d := e.getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(e.Home, ".claude.json")
}

var claudeCLI = &cli{
	name: "claude",
	at: func(e *Env) []string {
		if e.GOOS == "windows" {
			return []string{filepath.Join(e.Home, ".local", "bin", "claude.exe"),
				filepath.Join(localAppData(e), "Microsoft", "WinGet", "Links", "claude.exe")}
		}
		return []string{filepath.Join(e.Home, ".local", "bin", "claude"), filepath.Join(e.Home, ".claude", "local", "claude")}
	},
	add: func(srv Server) []string {
		return append([]string{"mcp", "add", "--scope", "user", Name, "--", srv.Command}, srv.Args...)
	},
	remove: []string{"mcp", "remove", "--scope", "user", Name},
}

// --- Claude Desktop ---

// claudeDesktopData is Claude Desktop's folder of user data, which holds
// claude_desktop_config.json and its extensions. On Windows it depends on
// how Claude was installed (the installer, MSIX, the Microsoft Store): the
// one whose settings changed last.
func claudeDesktopData(e *Env) string {
	if e.GOOS != "windows" {
		return filepath.Join(userConfig(e), "Claude")
	}
	local := localAppData(e)
	candidates := []string{filepath.Join(appData(e), "Claude")}
	if m, _ := filepath.Glob(filepath.Join(local, "Claude-Data", "*")); len(m) > 0 {
		candidates = append(candidates, m...)
	}
	candidates = append(candidates, filepath.Join(local, "Claude-Data"))
	if m, _ := filepath.Glob(filepath.Join(local, "Packages", "*Claude*", "LocalCache", "Roaming", "Claude")); len(m) > 0 {
		candidates = append(candidates, m...)
	}
	best, bestTime := "", int64(-1)
	for _, c := range candidates {
		fi, err := os.Stat(filepath.Join(c, "claude_desktop_config.json"))
		if err != nil {
			if best == "" && exists(filepath.Join(c, "Claude Extensions")) {
				best = c
			}
			continue
		}
		if t := fi.ModTime().UnixNano(); t > bestTime {
			best, bestTime = c, t
		}
	}
	if best == "" {
		return candidates[0]
	}
	return best
}

// claudeDesktopExtension finds Pinky among Claude Desktop's extensions:
// one whose manifest names it pinky, and whether it is turned on.
func claudeDesktopExtension(s *Setup) (installed, enabled bool) {
	data := claudeDesktopData(s.Env)
	manifests, _ := filepath.Glob(filepath.Join(data, "Claude Extensions", "*", "manifest.json"))
	sort.Strings(manifests)
	for _, m := range manifests {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var man struct{ Name string }
		if json.Unmarshal(b, &man) != nil || man.Name != Name {
			continue
		}
		id := filepath.Base(filepath.Dir(m))
		var set struct {
			IsEnabled *bool `json:"isEnabled"`
		}
		if b, err := os.ReadFile(filepath.Join(data, "Claude Extensions Settings", id+".json")); err == nil {
			json.Unmarshal(b, &set)
		}
		return true, set.IsEnabled == nil || *set.IsEnabled
	}
	return false, false
}

// --- Codex ---

func codexHome(e *Env) string {
	if d := e.getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(e.Home, ".codex")
}

// codexApps are the Mac apps that carry Codex: the ChatGPT app (Codex's
// desktop app joined it), and Codex.app as it was.
var codexApps = []string{"ChatGPT", "Codex"}

var codexCLI = &cli{
	name: "codex",
	at: func(e *Env) []string {
		var at []string
		for _, dir := range e.Apps {
			for _, app := range codexApps {
				at = append(at, filepath.Join(dir, app+".app", "Contents", "Resources", "codex-cli", "bin", "codex"))
			}
		}
		if e.GOOS == "windows" {
			at = append(at, filepath.Join(localAppData(e), "Programs", "OpenAI", "Codex", "bin", "codex.exe"))
		}
		return at
	},
	add: func(srv Server) []string {
		return append([]string{"mcp", "add", Name, "--", srv.Command}, srv.Args...)
	},
	remove: []string{"mcp", "remove", Name},
}

// --- VS Code and Copilot CLI ---

// copilotHome is ~/.copilot, or COPILOT_HOME.
func copilotHome(e *Env) string {
	if d := e.getenv("COPILOT_HOME"); d != "" {
		return d
	}
	return filepath.Join(e.Home, ".copilot")
}

// copilotPlaces: Copilot's global settings, which VS Code and Copilot CLI
// both read, then VS Code's own mcp.json of old, where an entry stays.
func copilotPlaces(e *Env) []place {
	return []place{
		{filepath.Join(copilotHome(e), "mcp-config.json"), jsonStore{path: []string{"mcpServers"}, entry: copilotEntry}},
		{filepath.Join(userConfig(e), "Code", "User", "mcp.json"), jsonStore{path: []string{"servers"}, entry: stdioEntry}},
	}
}

var copilotCLI = &cli{
	name: "copilot",
	add: func(srv Server) []string {
		return append([]string{"mcp", "add", Name, "--", srv.Command}, srv.Args...)
	},
	remove: []string{"mcp", "remove", Name},
}

// copilotSkills: Copilot CLI reads ~/.agents/skills only without
// COPILOT_HOME.
func copilotSkills(e *Env) string {
	if d := e.getenv("COPILOT_HOME"); d != "" {
		return filepath.Join(d, "skills")
	}
	return sharedSkills(e)
}

// --- Windsurf, now Devin Desktop ---

// devinDir is the folder Devin Desktop and its CLI keep their settings
// in; Windsurf, as it was called until June 2026, kept them in
// ~/.codeium/windsurf, which Devin still reads.
func devinDir(e *Env) string {
	if e.GOOS == "windows" {
		return filepath.Join(appData(e), "devin")
	}
	return filepath.Join(xdgConfig(e), "devin")
}

func windsurfDir(e *Env) string { return filepath.Join(e.Home, ".codeium", "windsurf") }

// isDevin: Devin Desktop or its CLI is here, not only Windsurf as it was.
func isDevin(e *Env) bool {
	return exists(devinDir(e)) || hasApp(e, "Devin", "Devin Desktop") || e.lookPath("devin") != ""
}

// --- Gemini CLI ---

func geminiDir(e *Env) string {
	if d := e.getenv("GEMINI_CLI_HOME"); d != "" {
		return filepath.Join(d, ".gemini")
	}
	return filepath.Join(e.Home, ".gemini")
}

var geminiCLI = &cli{
	name: "gemini",
	add: func(srv Server) []string {
		return append([]string{"mcp", "add", "--scope", "user", Name, srv.Command}, srv.Args...)
	},
	remove: []string{"mcp", "remove", "--scope", "user", Name},
}

func one(file func(*Env) string, s store) func(*Env) []place {
	return func(e *Env) []place { return []place{{file(e), s}} }
}

var catalog = []*agent{
	{
		id: "claude-code", name: fixed("Claude Code"), url: "https://claude.com/product/claude-code",
		found: func(e *Env) bool {
			return claudeCLI.find(e) != "" || exists(claudeJSON(e))
		},
		places:  one(claudeJSON, jsonStore{path: []string{"mcpServers"}, entry: claudeEntry}),
		cli:     claudeCLI,
		skills:  func(e *Env) string { return filepath.Join(claudeDir(e), "skills") },
		restart: RestartSession,
	},
	{
		id: "claude-desktop", name: fixed("Claude Desktop"), url: "https://claude.com/download",
		found: func(e *Env) bool {
			return hasApp(e, "Claude") || exists(claudeDesktopData(e)) ||
				(e.GOOS == "windows" && exists(filepath.Join(localAppData(e), "AnthropicClaude")))
		},
		places: one(func(e *Env) string { return filepath.Join(claudeDesktopData(e), "claude_desktop_config.json") },
			jsonStore{path: []string{"mcpServers"}, entry: mcpEntry}),
		restart:   RestartApp,
		bundle:    &bundleApp{macID: "com.anthropic.claudefordesktop"},
		extension: claudeDesktopExtension,
	},
	{
		id: "codex", name: fixed("Codex"), url: "https://developers.openai.com/codex",
		found: func(e *Env) bool {
			return codexCLI.find(e) != "" || exists(codexHome(e)) || hasApp(e, codexApps...)
		},
		places:  one(func(e *Env) string { return filepath.Join(codexHome(e), "config.toml") }, tomlStore{key: []string{"mcp_servers"}}),
		cli:     codexCLI,
		skills:  sharedSkills,
		restart: RestartSession,
	},
	{
		id: "cursor", name: fixed("Cursor"), url: "https://cursor.com/download",
		found: func(e *Env) bool {
			return hasApp(e, "Cursor") || exists(filepath.Join(e.Home, ".cursor", "mcp.json")) ||
				(e.GOOS == "windows" && exists(filepath.Join(localAppData(e), "Programs", "cursor", "Cursor.exe")))
		},
		places:  one(home(".cursor", "mcp.json"), jsonStore{path: []string{"mcpServers"}, entry: mcpEntry}),
		skills:  sharedSkills,
		restart: RestartApp,
	},
	{
		id: "vscode", name: fixed("VS Code"), url: "https://code.visualstudio.com/download",
		found: func(e *Env) bool {
			return hasApp(e, "Visual Studio Code") || exists(filepath.Join(userConfig(e), "Code", "User"))
		},
		places: copilotPlaces,
		skills: sharedSkills,
	},
	{
		id: "copilot", name: fixed("Copilot CLI"), url: "https://github.com/features/copilot/cli",
		found:   func(e *Env) bool { return copilotCLI.find(e) != "" },
		places:  copilotPlaces,
		cli:     copilotCLI,
		skills:  copilotSkills,
		restart: RestartSession,
	},
	{
		id: "windsurf",
		name: func(e *Env) string {
			if isDevin(e) {
				return "Devin Desktop"
			}
			return "Windsurf"
		},
		url: "https://windsurf.com/download",
		found: func(e *Env) bool {
			return isDevin(e) || hasApp(e, "Windsurf") || exists(windsurfDir(e))
		},
		places: func(e *Env) []place {
			devin := place{filepath.Join(devinDir(e), "mcp_config.json"), jsonStore{path: []string{"mcpServers"}, entry: mcpEntry}}
			windsurf := place{filepath.Join(windsurfDir(e), "mcp_config.json"), jsonStore{path: []string{"mcpServers"}, entry: mcpEntry}}
			if isDevin(e) {
				return []place{devin, windsurf}
			}
			return []place{windsurf, devin}
		},
		skills: func(e *Env) string {
			if isDevin(e) {
				return sharedSkills(e)
			}
			return filepath.Join(windsurfDir(e), "skills")
		},
		restart: RestartApp,
	},
	{
		id: "gemini", name: fixed("Gemini CLI"), url: "https://github.com/google-gemini/gemini-cli",
		// ~/.gemini is Antigravity's too: only the command says Gemini CLI
		// is here.
		found:   func(e *Env) bool { return geminiCLI.find(e) != "" },
		places:  one(func(e *Env) string { return filepath.Join(geminiDir(e), "settings.json") }, jsonStore{path: []string{"mcpServers"}, entry: mcpEntry}),
		cli:     geminiCLI,
		skills:  sharedSkills,
		restart: RestartSession,
	},
}
