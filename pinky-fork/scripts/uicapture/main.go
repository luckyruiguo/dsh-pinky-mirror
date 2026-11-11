// Command uicapture checks that Pinky's window shows, through MyGo's
// automation socket, and saves a PNG of each visible window. CI runs it on
// Linux under Xvfb against the e2e build:
//
//	go build -tags e2e -o build/e2e/pinky ./cmd/pinky
//	MYGO_AUTOMATION=$PWD/build/e2e/ui.sock build/e2e/pinky --fake &
//	go run ./scripts/uicapture -socket build/e2e/ui.sock -out build/e2e -expect "Pixel 9 (fake)"
//
// It waits for the socket and a visible window, lets each window settle,
// reads its elements (failing when -expect is given and no element's label
// or value contains it), and writes <out>/window-<id>.png.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	sock := flag.String("socket", "", "the app's MYGO_AUTOMATION socket")
	out := flag.String("out", ".", "folder for the captures")
	expect := flag.String("expect", "", "text an element of some window must show")
	timeout := flag.Duration("timeout", 60*time.Second, "how long to wait for the app")
	flag.Parse()
	if *sock == "" {
		fail("-socket is required")
	}
	deadline := time.Now().Add(*timeout)

	var c *client
	for {
		conn, err := net.Dial("unix", *sock)
		if err == nil {
			c = &client{conn: conn, r: bufio.NewReader(conn)}
			break
		}
		if time.Now().After(deadline) {
			fail("no app on %s: %v", *sock, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	defer c.conn.Close()

	type window struct {
		ID      int    `json:"id"`
		Title   string `json:"title"`
		Kind    string `json:"kind"`
		Visible bool   `json:"visible"`
	}
	var visible []window
	for {
		var res struct{ Windows []window }
		if err := c.call("windows", nil, &res); err != nil {
			fail("windows: %v", err)
		}
		visible = visible[:0]
		for _, w := range res.Windows {
			if w.Visible {
				visible = append(visible, w)
			}
		}
		if len(visible) > 0 {
			break
		}
		if time.Now().After(deadline) {
			fail("no visible window (the app has %d)", len(res.Windows))
		}
		time.Sleep(500 * time.Millisecond)
	}

	found := *expect == ""
	for _, w := range visible {
		params := map[string]any{"window": w.ID}
		if err := c.call("idle", map[string]any{"window": w.ID, "timeout": 10000}, nil); err != nil {
			fmt.Printf("window %d (%s): did not settle: %v\n", w.ID, w.Title, err)
		}
		var tree struct {
			Nodes []struct {
				Role  string `json:"role"`
				Label string `json:"label"`
				Value string `json:"value"`
			} `json:"nodes"`
		}
		if err := c.call("tree", params, &tree); err != nil {
			fmt.Printf("window %d (%s): no elements: %v\n", w.ID, w.Title, err)
		}
		var labels []string
		for _, n := range tree.Nodes {
			if n.Label != "" && len(labels) < 12 {
				labels = append(labels, n.Role+" "+n.Label)
			}
			if *expect != "" && (strings.Contains(n.Label, *expect) || strings.Contains(n.Value, *expect)) {
				found = true
			}
		}
		fmt.Printf("window %d %q (%s): %d elements: %s\n", w.ID, w.Title, w.Kind, len(tree.Nodes), strings.Join(labels, " · "))

		var shot struct {
			PNG    string `json:"png"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		}
		if err := c.call("capture", params, &shot); err != nil {
			fail("capture window %d: %v", w.ID, err)
		}
		png, err := base64.StdEncoding.DecodeString(shot.PNG)
		if err != nil || len(png) == 0 {
			fail("capture window %d: no PNG (%v)", w.ID, err)
		}
		path := filepath.Join(*out, fmt.Sprintf("window-%d.png", w.ID))
		if err := os.WriteFile(path, png, 0o644); err != nil {
			fail("%v", err)
		}
		fmt.Printf("window %d: %dx%d captured to %s\n", w.ID, shot.Width, shot.Height, path)
	}
	if !found {
		fail("no window shows %q", *expect)
	}
}

type client struct {
	conn net.Conn
	r    *bufio.Reader
	id   int
}

// call sends one request and decodes its result into result (when not
// nil).
func (c *client) call(method string, params, result any) error {
	c.id++
	req := map[string]any{"id": c.id, "method": method}
	if params != nil {
		req["params"] = params
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.conn.SetDeadline(time.Now().Add(60 * time.Second))
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return err
	}
	answer, err := c.r.ReadBytes('\n')
	if err != nil {
		return err
	}
	var res struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(answer, &res); err != nil {
		return err
	}
	if res.Error != nil {
		return errors.New(res.Error.Code + ": " + res.Error.Message)
	}
	if result != nil {
		return json.Unmarshal(res.Result, result)
	}
	return nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "uicapture: "+format+"\n", args...)
	os.Exit(1)
}
