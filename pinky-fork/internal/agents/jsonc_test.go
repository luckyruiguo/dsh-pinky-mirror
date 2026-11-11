package agents

import (
	"strings"
	"testing"
)

var testEntry = object{{"command", "/Applications/Pinky.app/Contents/MacOS/Pinky"}, {"args", []string{"mcp"}}}

func TestSetJSONMember(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{{
		name: "empty file",
		in:   "",
		want: "{\n  \"mcpServers\": {\n    \"pinky\": {\n      \"command\": \"/Applications/Pinky.app/Contents/MacOS/Pinky\",\n      \"args\": [\n        \"mcp\"\n      ]\n    }\n  }\n}\n",
	}, {
		name: "other servers, comments and a trailing comma stay",
		in: `{
  // My servers
  "mcpServers": {
    "github": { "command": "gh-mcp", "args": [] }, // keep me
  },
  "theme": "dark" /* the end */
}
`,
		want: `{
  // My servers
  "mcpServers": {
    "github": { "command": "gh-mcp", "args": [] }, // keep me
    "pinky": {
      "command": "/Applications/Pinky.app/Contents/MacOS/Pinky",
      "args": [
        "mcp"
      ]
    }
  },
  "theme": "dark" /* the end */
}
`,
	}, {
		name: "no mcpServers yet, four spaces",
		in:   "{\n    \"preferences\": {\n        \"x\": true\n    }\n}\n",
		want: "{\n    \"preferences\": {\n        \"x\": true\n    },\n    \"mcpServers\": {\n        \"pinky\": {\n            \"command\": \"/Applications/Pinky.app/Contents/MacOS/Pinky\",\n            \"args\": [\n                \"mcp\"\n            ]\n        }\n    }\n}\n",
	}, {
		name: "an empty mcpServers",
		in:   "{\n\t\"mcpServers\": {}\n}",
		want: "{\n\t\"mcpServers\": {\n\t\t\"pinky\": {\n\t\t\t\"command\": \"/Applications/Pinky.app/Contents/MacOS/Pinky\",\n\t\t\t\"args\": [\n\t\t\t\t\"mcp\"\n\t\t\t]\n\t\t}\n\t}\n}",
	}, {
		name: "replaces the old entry only",
		in:   "{\n  \"mcpServers\": {\n    \"pinky\": {\"command\": \"pinky\", \"args\": [\"mcp\"]},\n    \"other\": {\"command\": \"x\"}\n  }\n}\n",
		want: "{\n  \"mcpServers\": {\n    \"pinky\": {\n      \"command\": \"/Applications/Pinky.app/Contents/MacOS/Pinky\",\n      \"args\": [\n        \"mcp\"\n      ]\n    },\n    \"other\": {\"command\": \"x\"}\n  }\n}\n",
	}, {
		name: "one line",
		in:   `{"mcpServers":{"a":{"command":"x"}}}`,
		want: `{"mcpServers":{"a":{"command":"x"}, "pinky": {"command": "/Applications/Pinky.app/Contents/MacOS/Pinky", "args": ["mcp"]}}}`,
	}, {
		name: "one line, empty",
		in:   `{"mcpServers":{}}`,
		want: `{"mcpServers":{"pinky": {"command": "/Applications/Pinky.app/Contents/MacOS/Pinky", "args": ["mcp"]}}}`,
	}, {
		name: "Windows line ends and a byte order mark",
		in:   "\xef\xbb\xbf{\r\n  \"mcpServers\": {\r\n    \"a\": {}\r\n  }\r\n}\r\n",
		want: "\xef\xbb\xbf{\r\n  \"mcpServers\": {\r\n    \"a\": {},\r\n    \"pinky\": {\r\n      \"command\": \"/Applications/Pinky.app/Contents/MacOS/Pinky\",\r\n      \"args\": [\r\n        \"mcp\"\r\n      ]\r\n    }\r\n  }\r\n}\r\n",
	}, {
		name: "comments alone",
		in:   "// written by hand\n",
		want: "// written by hand\n{\n  \"mcpServers\": {\n    \"pinky\": {\n      \"command\": \"/Applications/Pinky.app/Contents/MacOS/Pinky\",\n      \"args\": [\n        \"mcp\"\n      ]\n    }\n  }\n}\n",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := setJSONMember([]byte(tc.in), []string{"mcpServers"}, "pinky", testEntry)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("got\n%s\nwant\n%s", out, tc.want)
			}
			// Setting it again changes nothing.
			again, err := setJSONMember(out, []string{"mcpServers"}, "pinky", testEntry)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(out) {
				t.Errorf("setting it again changed the file:\n%s", again)
			}
		})
	}
}

func TestSetJSONMemberRefuses(t *testing.T) {
	for name, in := range map[string]string{
		"broken":             `{"mcpServers": {"a": }`,
		"unclosed comment":   `{"mcpServers": {} /* `,
		"not an object":      `["mcpServers"]`,
		"servers not object": `{"mcpServers": []}`,
		"twice":              `{"mcpServers": {"pinky": {}, "pinky": {}}}`,
		"text after":         `{} {}`,
		"bad literal":        `{"a": tru}`,
		"line in string":     "{\"a\": \"x\ny\"}",
	} {
		if out, err := setJSONMember([]byte(in), []string{"mcpServers"}, "pinky", testEntry); err == nil {
			t.Errorf("%s: changed %q into %q", name, in, out)
		}
	}
}

func TestDeleteJSONMember(t *testing.T) {
	cases := []struct {
		name, in, want string
		changed        bool
	}{{
		name: "last of several",
		in:   "{\n  \"mcpServers\": {\n    \"a\": {\"command\": \"x\"},\n    \"pinky\": {\n      \"command\": \"p\"\n    }\n  }\n}\n",
		want: "{\n  \"mcpServers\": {\n    \"a\": {\"command\": \"x\"}\n  }\n}\n", changed: true,
	}, {
		name: "first, with comments",
		in:   "{\n  \"mcpServers\": {\n    // Pinky\n    \"pinky\": {\"command\": \"p\"},\n    \"a\": {\"command\": \"x\"} // a\n  }\n}\n",
		want: "{\n  \"mcpServers\": {\n    // Pinky\n    \"a\": {\"command\": \"x\"} // a\n  }\n}\n", changed: true,
	}, {
		name: "the only one",
		in:   "{\n  \"mcpServers\": {\n    \"pinky\": {\"command\": \"p\"}\n  }\n}\n",
		want: "{\n  \"mcpServers\": {\n  }\n}\n", changed: true,
	}, {
		name: "one line, last",
		in:   `{"mcpServers": {"a": 1, "pinky": {"command": "p"}}}`,
		want: `{"mcpServers": {"a": 1 }}`, changed: true,
	}, {
		name: "one line, first",
		in:   `{"mcpServers": {"pinky": {"command": "p"}, "a": 1}}`,
		want: `{"mcpServers": {"a": 1}}`, changed: true,
	}, {
		name: "trailing comma kept",
		in:   "{\n  \"mcpServers\": {\n    \"a\": 1,\n    \"pinky\": 2,\n  },\n}\n",
		want: "{\n  \"mcpServers\": {\n    \"a\": 1,\n  },\n}\n", changed: true,
	}, {
		name: "Windows line ends",
		in:   "{\r\n  \"mcpServers\": {\r\n    \"a\": 1,\r\n    \"pinky\": 2\r\n  }\r\n}\r\n",
		want: "{\r\n  \"mcpServers\": {\r\n    \"a\": 1\r\n  }\r\n}\r\n", changed: true,
	}, {
		name: "not there",
		in:   `{"mcpServers": {"a": 1}}`,
		want: `{"mcpServers": {"a": 1}}`,
	}, {
		name: "no servers",
		in:   `{"theme": "dark"}`,
		want: `{"theme": "dark"}`,
	}, {
		name: "empty file",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := deleteJSONMember([]byte(tc.in), []string{"mcpServers"}, "pinky")
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want || changed != tc.changed {
				t.Errorf("got (%v)\n%s\nwant (%v)\n%s", changed, out, tc.changed, tc.want)
			}
		})
	}
	if _, _, err := deleteJSONMember([]byte(`{"mcpServers": {"pinky": 1`), []string{"mcpServers"}, "pinky"); err == nil {
		t.Error("deleted from a broken file")
	}
}

func TestSetJSONMemberTopLevel(t *testing.T) {
	// Some agents keep servers under two keys ("amp.mcpServers" is one
	// key) or at the top: the path may be empty.
	out, err := setJSONMember([]byte("{\n  \"a\": 1\n}\n"), nil, "pinky", 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"a\": 1,\n  \"pinky\": 2\n}\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
	out, err = setJSONMember([]byte("{}"), []string{"context_servers", "x"}, "pinky", 2)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := readJSONC(out)
	if got, ok := lookup(v, "context_servers", "x", "pinky"); !ok || got.(interface{ String() string }).String() != "2" {
		t.Errorf("nested paths: %s", out)
	}
}

func TestReadJSONC(t *testing.T) {
	v, err := readJSONC([]byte("// c\n{\"a\": {\"b\": [1, 2,], /* x */ \"c\": \"//not a comment\",},}"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := lookup(v, "a", "c"); got != "//not a comment" {
		t.Errorf("a.c = %v", got)
	}
	if _, err := readJSONC([]byte(`{"a": 1,, }`)); err == nil {
		t.Error("read two commas")
	}
	if strings.Contains(spaced(`{"a":"b:c,d","e":[1,2]}`), `"b: c`) {
		t.Error("spaced changed a string")
	}
}
