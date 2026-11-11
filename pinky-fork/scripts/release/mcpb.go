package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"

	"github.com/DIYgod/pinky/assets"
	"github.com/DIYgod/pinky/internal/agents"
	"github.com/DIYgod/pinky/internal/mcp"
)

// runMCPB writes Pinky's MCP bundle for Claude Desktop, pinky-<version>.mcpb:
// a double-click installs it in Claude Desktop, where it starts the
// installed Pinky (internal/agents.WriteBundle).
func runMCPB(args []string) error {
	fs := flag.NewFlagSet("release mcpb", flag.ContinueOnError)
	version := fs.String("version", "", "the release's version")
	out := fs.String("out", "", "the bundle to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || *out == "" {
		return errors.New("usage: release mcpb -version <version> -out <file.mcpb>")
	}
	tools, err := mcp.Tools(context.Background(), *version)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	if err := agents.WriteBundle(&b, agents.BundleOptions{Version: *version, Icon: assets.Icon256, Tools: agents.BundleTools(tools)}); err != nil {
		return err
	}
	return os.WriteFile(*out, b.Bytes(), 0o644)
}
