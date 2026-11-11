package agents

import (
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// BundleTools are tools as a bundle's manifest lists them: each with the
// first sentence of its description.
func BundleTools(tools []*sdk.Tool) []BundleTool {
	out := make([]BundleTool, 0, len(tools))
	for _, t := range tools {
		desc := t.Description
		if i := strings.Index(desc, ". "); i >= 0 {
			desc = desc[:i+1]
		}
		out = append(out, BundleTool{Name: t.Name, Description: desc})
	}
	return out
}
