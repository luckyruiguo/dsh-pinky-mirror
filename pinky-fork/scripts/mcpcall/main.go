// Command mcpcall drives Pinky as an agent would, for manual and scripted
// checks: it starts `pinky mcp`, then calls tools in order in one MCP
// session and prints each result.
//
//	go run ./scripts/mcpcall phone_connect '{"task":"Check the calculator"}' \
//	    phone_launch '{"app":"Calculator"}' \
//	    phone_tap '{"ref":"$ref:7"}' \
//	    phone_release '{"summary":"done"}'
//
// A string argument "$ref:LABEL" becomes the ref of the element labelled
// LABEL on the last screen a result showed (exact match first, then the
// first label that contains it).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	pinky := flag.String("pinky", "pinky", "the pinky binary")
	name := flag.String("name", "mcpcall", "the MCP client name the gateway sees")
	delay := flag.Duration("delay", 400*time.Millisecond, "pause between calls")
	timeout := flag.Duration("timeout", 90*time.Second, "limit for each call")
	flag.Parse()
	args := flag.Args()
	if len(args)%2 != 0 {
		fail("arguments come in pairs: tool and JSON arguments")
	}

	cl := sdk.NewClient(&sdk.Implementation{Name: *name, Version: "dev"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cs, err := cl.Connect(ctx, &sdk.CommandTransport{Command: exec.Command(*pinky, "mcp")}, nil)
	cancel()
	if err != nil {
		fail("connect: %v", err)
	}
	defer cs.Close()

	known := map[string]int{} // label → ref, from every screen and diff so far
	for i := 0; i < len(args); i += 2 {
		tool := args[i]
		var params map[string]any
		if err := json.Unmarshal([]byte(args[i+1]), &params); err != nil {
			fail("%s: arguments: %v", tool, err)
		}
		for k, v := range params {
			if s, ok := v.(string); ok && strings.HasPrefix(s, "$ref:") {
				ref, ok := findRef(known, strings.TrimPrefix(s, "$ref:"))
				if !ok {
					fail("%s: no element %q on the last screen", tool, strings.TrimPrefix(s, "$ref:"))
				}
				params[k] = ref
			}
		}
		if i > 0 {
			time.Sleep(*delay)
		}
		start := time.Now()
		cctx, cancel := context.WithTimeout(context.Background(), *timeout)
		res, err := cs.CallTool(cctx, &sdk.CallToolParams{Name: tool, Arguments: params})
		cancel()
		if err != nil {
			fail("%s: %v", tool, err)
		}
		var b strings.Builder
		for _, c := range res.Content {
			switch c := c.(type) {
			case *sdk.TextContent:
				b.WriteString(c.Text)
			case *sdk.ImageContent:
				fmt.Fprintf(&b, "[image %s, %d bytes]\n", c.MIMEType, len(c.Data))
			}
		}
		text := b.String()
		learn(known, text)
		fmt.Printf("=== %s %s (%v, error=%v)\n%s\n", tool, args[i+1], time.Since(start).Round(time.Millisecond), res.IsError, strings.TrimRight(text, "\n"))
	}
}

var refRe = regexp.MustCompile(`^([-+~] )?\[(\d+)\] \w+ "([^"]*)"`)

// learn updates the known refs from a result: a whole screen replaces
// them, a diff adds and removes lines.
func learn(known map[string]int, text string) {
	if strings.Contains(text, "\nscreen ") || strings.HasPrefix(text, "screen ") {
		clear(known)
	}
	for _, line := range strings.Split(text, "\n") {
		m := refRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if m[1] == "- " {
			if known[m[3]] == n {
				delete(known, m[3])
			}
			continue
		}
		known[m[3]] = n
	}
}

func findRef(known map[string]int, label string) (int, bool) {
	if n, ok := known[label]; ok {
		return n, true
	}
	for l, n := range known {
		if strings.Contains(l, label) {
			return n, true
		}
	}
	return 0, false
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mcpcall: "+format+"\n", args...)
	os.Exit(1)
}
