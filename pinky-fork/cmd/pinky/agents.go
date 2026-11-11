package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DIYgod/pinky"
	"github.com/DIYgod/pinky/assets"
	"github.com/DIYgod/pinky/internal/agents"
	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/ui"
)

// agentSetup finds the agents on this computer and adds this Pinky to them
// from Settings › Agents (internal/agents): copies of the files it changes,
// and the MCP bundle it opens in Claude Desktop, go in <data>/agents.
func agentSetup(dirs api.Dirs) *agents.Setup {
	s := &agents.Setup{
		Env: agents.SystemEnv(), Server: ui.AgentServer(), Skill: pinky.Skill,
		Dir:    filepath.Join(dirs.Data, "agents"),
		Bundle: agents.BundleOptions{Version: version, Icon: assets.Icon256},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if tools, err := mcp.Tools(ctx, version); err == nil {
		s.Bundle.Tools = agents.BundleTools(tools)
	}
	return s
}

// refreshSkills brings the copies of Pinky's skill that an older Pinky
// installed in agents up to this Pinky's text, as the app or the daemon
// starts: Pinky updates often, and agents should read the guide of the
// Pinky they start. Builds from source (version …-dev) and a Pinky in a
// folder of its own (PINKY_HOME: tests, a second copy) leave them alone, so
// a branch's draft of the guide does not replace the released one.
func refreshSkills(log *slog.Logger) {
	if strings.HasSuffix(version, "-dev") || os.Getenv(api.HomeEnv) != "" {
		return
	}
	s := &agents.Setup{Env: agents.SystemEnv(), Skill: pinky.Skill}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	files, err := s.RefreshSkills(ctx)
	for _, f := range files {
		log.Info("agents: updated Pinky's skill", "file", f)
	}
	if err != nil {
		log.Warn("agents: update Pinky's skill", "err", err)
	}
}
