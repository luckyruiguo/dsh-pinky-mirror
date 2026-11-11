package agents

import "testing"

var testServer = Server{Command: `C:\Users\Jane "J" Doe\AppData\Local\Programs\Pinky\Pinky.exe`, Args: []string{"mcp"}}

const testTable = `[mcp_servers.pinky]
command = "C:\\Users\\Jane \"J\" Doe\\AppData\\Local\\Programs\\Pinky\\Pinky.exe"
args = ["mcp"]
`

func TestSetTOMLServer(t *testing.T) {
	key := []string{"mcp_servers", "pinky"}
	cases := []struct{ name, in, want string }{{
		name: "empty",
		want: testTable,
	}, {
		name: "appended after the rest",
		in:   "model = \"gpt-6\" # mine\n\n[mcp_servers.github]\ncommand = \"gh\"\n",
		want: "model = \"gpt-6\" # mine\n\n[mcp_servers.github]\ncommand = \"gh\"\n\n" + testTable,
	}, {
		name: "no line end at the end",
		in:   "model = \"x\"",
		want: "model = \"x\"\n\n" + testTable,
	}, {
		name: "replaces the old one where it is, with its sub-table",
		in:   "[mcp_servers.pinky]\ncommand = \"pinky\"\nargs = [\"mcp\"]\n\n[mcp_servers.pinky.env]\nA = \"1\"\n\n[profiles.x]\nmodel = \"y\"\n",
		want: testTable + "\n[profiles.x]\nmodel = \"y\"\n",
	}, {
		name: "a quoted key",
		in:   "[mcp_servers.\"pinky\"]\ncommand = \"old\"\n[other]\na = 1\n",
		want: testTable + "\n[other]\na = 1\n",
	}, {
		name: "a header inside a multi-line string is not a header",
		in:   "notes = \"\"\"\n[mcp_servers.pinky]\n\"\"\"\n",
		want: "notes = \"\"\"\n[mcp_servers.pinky]\n\"\"\"\n\n" + testTable,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := setTOMLServer([]byte(tc.in), key, testServer)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("got\n%s\nwant\n%s", out, tc.want)
			}
			again, err := setTOMLServer(out, key, testServer)
			if err != nil || string(again) != string(out) {
				t.Errorf("setting it again: %v\n%s", err, again)
			}
		})
	}
	for name, in := range map[string]string{
		"broken":         "model = \n",
		"inline":         "mcp_servers = { pinky = { command = \"x\" } }\n",
		"dotted":         "[mcp_servers]\npinky.command = \"x\"\n",
		"inline servers": "mcp_servers = { github = { command = \"gh\" } }\n",
	} {
		if out, err := setTOMLServer([]byte(in), key, testServer); err == nil {
			t.Errorf("%s: changed %q into %q", name, in, out)
		}
	}
}

func TestDeleteTOMLServer(t *testing.T) {
	key := []string{"mcp_servers", "pinky"}
	cases := []struct {
		name, in, want string
		changed        bool
	}{{
		name: "at the end", changed: true,
		in:   "model = \"x\"\n\n" + testTable,
		want: "model = \"x\"\n",
	}, {
		name: "in the middle, with its sub-table", changed: true,
		in:   "a = 1\n\n" + testTable + "\n[mcp_servers.pinky.env]\nB = \"2\"\n\n[mcp_servers.github]\ncommand = \"gh\"\n",
		want: "a = 1\n\n[mcp_servers.github]\ncommand = \"gh\"\n",
	}, {
		name: "Windows line ends", changed: true,
		in:   "a = 1\r\n\r\n[mcp_servers.pinky]\r\ncommand = \"p\"\r\n",
		want: "a = 1\r\n",
	}, {
		name: "not there",
		in:   "a = 1\n",
		want: "a = 1\n",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := deleteTOMLServer([]byte(tc.in), key)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want || changed != tc.changed {
				t.Errorf("got (%v)\n%q\nwant (%v)\n%q", changed, out, tc.changed, tc.want)
			}
		})
	}
	if _, _, err := deleteTOMLServer([]byte("[mcp_servers]\npinky = { command = \"x\" }\n"), key); err == nil {
		t.Error("removed an inline table")
	}
}

func TestSplitKey(t *testing.T) {
	for in, want := range map[string][]string{
		"mcp_servers.pinky":        {"mcp_servers", "pinky"},
		` mcp_servers . "pin.ky" `: {"mcp_servers", "pin.ky"},
		"a.'b c'":                  {"a", "b c"},
	} {
		got, ok := splitKey(in)
		if !ok || len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("splitKey(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"1, 2", "a..b", `"a`, "a b"} {
		if got, ok := splitKey(in); ok {
			t.Errorf("splitKey(%q) = %q", in, got)
		}
	}
}
