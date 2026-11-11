package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The shim's own answers while Pinky is closed are well-formed JSON-RPC:
// an empty result to a ping, a tool error the model reads to a tool call,
// an error to anything else.
func TestAwayAnswer(t *testing.T) {
	type answer struct {
		ID     json.RawMessage `json:"id"`
		Result *struct {
			Content []struct {
				Type, Text string
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	parse := func(line []byte) answer {
		t.Helper()
		if !strings.HasSuffix(string(line), "\n") || strings.Count(string(line), "\n") != 1 {
			t.Fatalf("not one line: %q", line)
		}
		var a answer
		if err := json.Unmarshal(line, &a); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		return a
	}
	if a := parse(awayAnswer(`"p1"`, "ping", notRunning)); string(a.ID) != `"p1"` || a.Result == nil || a.Error != nil {
		t.Errorf("ping: %+v", a)
	}
	a := parse(awayAnswer("7", "tools/call", quitDuring))
	if string(a.ID) != "7" || a.Result == nil || !a.Result.IsError || len(a.Result.Content) != 1 || a.Result.Content[0].Text != quitDuring {
		t.Errorf("tools/call: %+v", a)
	}
	a = parse(awayAnswer("8", "tools/list", notRunning))
	if string(a.ID) != "8" || a.Error == nil || a.Error.Message != notRunning || a.Result != nil {
		t.Errorf("tools/list: %+v", a)
	}
}
