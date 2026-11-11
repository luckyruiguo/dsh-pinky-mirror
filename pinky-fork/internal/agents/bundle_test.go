package agents

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteBundle(t *testing.T) {
	var b bytes.Buffer
	tools := []BundleTool{{Name: "phone_tap", Description: "Tap an element."}}
	if err := WriteBundle(&b, BundleOptions{Version: "2026.1108.3", Icon: []byte("\x89PNG"), Tools: tools}); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name], _ = io.ReadAll(r)
		r.Close()
		if f.Name == "server/pinky.sh" && f.Mode().Perm()&0o111 == 0 {
			t.Errorf("the launcher is not executable: %v", f.Mode())
		}
	}
	var m struct {
		ManifestVersion string `json:"manifest_version"`
		Name, Version   string
		Icon            string
		Server          struct {
			Type       string
			EntryPoint string `json:"entry_point"`
			MCPConfig  struct {
				Command string
				Args    []string
			} `json:"mcp_config"`
		}
		Tools []BundleTool
	}
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatalf("manifest.json: %v\n%s", err, files["manifest.json"])
	}
	if m.Name != "pinky" || m.Version != "2026.1108.3" || m.Server.Type != "binary" || len(m.Tools) != 1 {
		t.Errorf("manifest %+v", m)
	}
	for _, name := range []string{m.Icon, m.Server.EntryPoint} {
		if _, ok := files[name]; !ok {
			t.Errorf("the bundle lacks %s", name)
		}
	}
	// The same bundle, byte for byte.
	var again bytes.Buffer
	WriteBundle(&again, BundleOptions{Version: "2026.1108.3", Icon: []byte("\x89PNG"), Tools: tools})
	if !bytes.Equal(again.Bytes(), b.Bytes()) {
		t.Error("two bundles differ")
	}
}

// TestLauncher runs the launcher with a fake Pinky in $PINKY, in
// ~/Applications, and with none. The places it looks in outside the home
// folder move into the test's folder, so that it never starts the Pinky
// installed on this computer.
func TestLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is for macOS")
	}
	dir := t.TempDir()
	text := strings.NewReplacer(" /Applications/", " "+dir+"/Applications/", " /opt/homebrew/bin/", " "+dir+"/brew/",
		" /usr/local/bin/", " "+dir+"/local/", " /usr/bin/", " "+dir+"/usrbin/").Replace(launcher)
	if strings.Contains(text, " /Applications") || strings.Contains(text, " /opt/") || strings.Contains(text, " /usr/") {
		t.Fatalf("the launcher still looks outside the test's folder:\n%s", text)
	}
	sh := filepath.Join(dir, "pinky.sh")
	os.WriteFile(sh, []byte(text), 0o755)
	home := filepath.Join(dir, "home")
	fake := []byte("#!/bin/sh\necho \"started $0 $*\"\n")
	run := func(pinky string) (string, error) {
		cmd := exec.Command("/bin/sh", sh)
		cmd.Env = []string{"PINKY=" + pinky, "HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	// None: it says where to get Pinky. An unset setting reaches the
	// launcher empty or unexpanded.
	for _, p := range []string{"", "${user_config.pinky}"} {
		if out, err := run(p); err == nil || !strings.Contains(out, "Pinky is not installed") {
			t.Errorf("PINKY=%q without Pinky: %v %q", p, err, out)
		}
	}
	// In ~/Applications, then in Applications, then $PINKY first.
	userApp := filepath.Join(home, "Applications", "Pinky.app", "Contents", "MacOS", "Pinky")
	app := filepath.Join(dir, "Applications", "Pinky.app", "Contents", "MacOS", "Pinky")
	custom := filepath.Join(dir, "elsewhere", "Pinky")
	for _, p := range []string{userApp, app, custom} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, fake, 0o755)
	}
	for pinky, want := range map[string]string{"": app, custom: custom} {
		if out, err := run(pinky); err != nil || out != "started "+want+" mcp" {
			t.Errorf("PINKY=%q: %v %q, want %s", pinky, err, out, want)
		}
	}
	os.Remove(app)
	if out, err := run(""); err != nil || out != "started "+userApp+" mcp" {
		t.Errorf("in ~/Applications: %v %q", err, out)
	}
}
