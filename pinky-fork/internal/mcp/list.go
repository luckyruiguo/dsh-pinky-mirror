package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools lists the server's tools as agents see them, without a gateway:
// for listings such as the manifest of Pinky's MCP bundle for Claude
// Desktop.
func Tools(ctx context.Context, version string) ([]*sdk.Tool, error) {
	st, ct := sdk.NewInMemoryTransports()
	ss, err := New(nil, version).SDK().Connect(ctx, st, nil)
	if err != nil {
		return nil, err
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "pinky-tools", Version: version}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}
