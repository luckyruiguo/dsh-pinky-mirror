// Command corectl talks to the standalone prototype's core over its control
// socket, as Pinky's own screens do, for checks from a shell. The socket
// takes only the app's own uid, so run it with run-as:
//
//	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o corectl ./internal/standalone/cmd/corectl
//	adb push corectl /data/local/tmp/
//	adb shell run-as app.pinky.standalone sh -c 'cp /data/local/tmp/corectl . && ./corectl state'
//
// Commands:
//
//	state                      print the state
//	start -task T [-key K] [-base-url U] [-model M]   start a task
//	watch [-decide D] [-after 2s]   print steps and needs until the task ends,
//	                           answering each need with D (allow_once, deny,
//	                           or none to leave it), and time each step
//	stop | dismiss
//	decide NEED DECISION
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const socket = "@pinky_standalone_core"

// The parts of standalone.State it reads. It does not import the core, so
// it builds as a static binary for the phone's shell.
type state struct {
	Agent *struct {
		Running bool   `json:"running"`
		End     string `json:"end"`
		Summary string `json:"summary"`
		Steps   int    `json:"steps"`
		Tokens  int64  `json:"tokens"`
		Seconds int64  `json:"seconds"`
	} `json:"agent"`
	Task *struct {
		Steps []struct {
			Text   string `json:"text"`
			Status string `json:"status"`
			MS     int64  `json:"ms"`
		} `json:"steps"`
	} `json:"task"`
	Needs []struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Rule   string `json:"rule"`
	} `json:"needs"`
}

type reply struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	State json.RawMessage `json:"state"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: corectl state|start|watch|stop|dismiss|decide|preset")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "state":
		var r reply
		if r, err = call(map[string]any{"cmd": "state"}); err == nil {
			var v any
			_ = json.Unmarshal(r.State, &v)
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Println(string(b))
		}
	case "start":
		fs := flag.NewFlagSet("start", flag.ExitOnError)
		task := fs.String("task", "", "the task")
		key := fs.String("key", "fake", "the API key (a fake one for fakemodel)")
		provider := fs.String("provider", "", "anthropic (the default) or openai")
		base := fs.String("base-url", "", "the API's address")
		model := fs.String("model", "", "the model")
		steps := fs.Int("max-steps", 0, "step limit")
		mins := fs.Int("max-minutes", 0, "time limit")
		_ = fs.Parse(args)
		_, err = call(map[string]any{"cmd": "start", "task": *task, "key": *key, "provider": *provider, "base_url": *base, "model": *model,
			"max_steps": *steps, "max_minutes": *mins})
	case "stop", "dismiss":
		_, err = call(map[string]any{"cmd": cmd})
	case "decide":
		if len(args) != 2 {
			err = fmt.Errorf("decide NEED DECISION")
			break
		}
		_, err = call(map[string]any{"cmd": "decide", "need": args[0], "decision": args[1]})
	case "preset":
		var n int
		if len(args) != 1 {
			err = fmt.Errorf("preset 0|1|2|3")
			break
		}
		if n, err = strconv.Atoi(args[0]); err == nil {
			_, err = call(map[string]any{"cmd": "preset", "preset": n})
		}
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		decide := fs.String("decide", "allow_once", "answer to each need: allow_once, allow_task, deny, or none")
		after := fs.Duration("after", 2*time.Second, "wait this long before answering, as a person reading it")
		stopAfter := fs.Duration("stop-after", 0, "press Stop after this long (0: never)")
		_ = fs.Parse(args)
		err = watch(*decide, *after, *stopAfter)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "corectl:", err)
		os.Exit(1)
	}
}

func call(req map[string]any) (reply, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return reply{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return reply{}, err
	}
	var r reply
	if err := json.NewDecoder(conn).Decode(&r); err != nil {
		return reply{}, err
	}
	if !r.OK {
		return r, fmt.Errorf("%s", r.Error)
	}
	return r, nil
}

func watch(decide string, after, stopAfter time.Duration) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(map[string]any{"cmd": "subscribe"}); err != nil {
		return err
	}
	t0 := time.Now()
	in := bufio.NewScanner(conn)
	in.Buffer(make([]byte, 64<<10), 4<<20)
	printed := 0
	seenNeed := map[string]time.Time{}
	stopped := false
	wasRunning := false
	for in.Scan() {
		var r reply
		var st state
		if err := json.Unmarshal(in.Bytes(), &r); err != nil || json.Unmarshal(r.State, &st) != nil {
			continue
		}
		el := time.Since(t0).Seconds()
		if st.Task != nil {
			for ; printed < len(st.Task.Steps); printed++ {
				s := st.Task.Steps[printed]
				fmt.Printf("%7.1fs step %d: %s [%s, %d ms]\n", el, printed+1, s.Text, s.Status, s.MS)
			}
		}
		for _, n := range st.Needs {
			if _, ok := seenNeed[n.ID]; ok {
				continue
			}
			seenNeed[n.ID] = time.Now()
			fmt.Printf("%7.1fs NEED %s: %s · %s (rule %q)\n", el, n.Kind, n.Title, n.Detail, n.Rule)
			if decide != "none" {
				go func(id string) {
					time.Sleep(after)
					d := decide
					t := time.Now()
					if _, err := call(map[string]any{"cmd": "decide", "need": id, "decision": d}); err != nil {
						fmt.Printf("         decide %s: %v\n", id, err)
						return
					}
					fmt.Printf("%7.1fs decided %s (%s, %d ms)\n", time.Since(t0).Seconds(), d, id, time.Since(t).Milliseconds())
				}(n.ID)
			}
		}
		if stopAfter > 0 && !stopped && time.Since(t0) > stopAfter {
			stopped = true
			t := time.Now()
			if _, err := call(map[string]any{"cmd": "stop"}); err != nil {
				return err
			}
			fmt.Printf("%7.1fs STOP pressed (%d ms)\n", time.Since(t0).Seconds(), time.Since(t).Milliseconds())
		}
		if a := st.Agent; a != nil {
			if a.Running {
				wasRunning = true
			} else if wasRunning || time.Since(t0) > 3*time.Second {
				fmt.Printf("%7.1fs ENDED %s: %s (%d steps, %d tokens, %d s)\n", el, a.End, oneLine(a.Summary), a.Steps, a.Tokens, a.Seconds)
				return nil
			}
		}
	}
	return in.Err()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
