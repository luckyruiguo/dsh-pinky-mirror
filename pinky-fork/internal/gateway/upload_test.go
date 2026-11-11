package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadName(t *testing.T) {
	ok := map[string]string{
		"photo.jpg":             "photo.jpg",
		" Pictures/cat.png ":    "Pictures/cat.png",
		`Pictures\cat.png`:      "Pictures/cat.png",
		"Pinky/notes/todo.txt":  "notes/todo.txt",
		"a//b.txt":              "a/b.txt",
		"小猫 照片.jpg":             "小猫 照片.jpg",
		"report.v2.final.pdf":   "report.v2.final.pdf",
		"console.log":           "console.log",
		"COM10.txt":             "COM10.txt",
		"no extension":          "no extension",
		"Download/2026-11-08 a": "Download/2026-11-08 a",
		"trimmed ":              "trimmed",
	}
	for in, want := range ok {
		if got, why := uploadName(in); why != "" || got != want {
			t.Errorf("uploadName(%q) = %q, %q; want %q", in, got, why, want)
		}
	}
	for _, in := range []string{
		"", "  ", "/etc/passwd", "../outside.txt", "a/../../b", "..", ".hidden", "a/.git/config", ".pinky-123",
		"Pictures/", "a:b.txt", "C:/Windows/x", "what?.txt", "a*b", "<x>", `say "hi".txt`, "a|b",
		"name.", "folder /a.txt", "folder./a.txt", "CON", "con.txt", "Nul.tar.gz", "aux", "COM1.log", "lpt9",
		"tab\there", "nl\nhere", "bell\x07", string([]byte{0xff, 0xfe}), strings.Repeat("a", 256), strings.Repeat("a/", 600) + "b",
	} {
		if got, why := uploadName(in); why == "" {
			t.Errorf("uploadName(%q) = %q, accepted", in, got)
		}
	}
}

func upload(s *Session, name, data string, replace bool) *Result {
	return s.Files(context.Background(), FilesRequest{Action: "upload", Paths: []string{name}, Body: strings.NewReader(data), Replace: replace})
}

func TestPhoneFilesUpload(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Send a picture")
	dir := g.FilesDir("pixel")

	res := upload(s, "Pictures/cat.png", "meow", false)
	mustStatus(t, res, StatusSettled)
	if res.Folder != dir || len(res.Files) != 1 || res.Files[0].Path != "Pictures/cat.png" || res.Files[0].Size != 4 ||
		!strings.Contains(res.Message, "Saved Pictures/cat.png (4 B)") || !strings.Contains(res.Message, `paths: ["Pictures/cat.png"]`) {
		t.Fatalf("upload %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "Pictures", "cat.png")); err != nil || string(b) != "meow" {
		t.Fatalf("saved %q, %v", b, err)
	}
	if len(x.files) != 0 {
		t.Fatalf("an upload reached the phone: %v", x.files)
	}

	// The same bytes again: nothing changes. Others need replace.
	res = upload(s, "Pictures/cat.png", "meow", false)
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "already has this") {
		t.Fatalf("same upload: %+v", res)
	}
	if res = upload(s, "Pictures/cat.png", "woof", false); res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "replace: true") {
		t.Fatalf("another file of the same name: %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Pictures", "cat.png")); string(b) != "meow" {
		t.Fatalf("replaced without replace: %q", b)
	}
	res = upload(s, "Pictures/cat.png", "woof", true)
	mustStatus(t, res, StatusSettled)
	if !strings.HasPrefix(res.Message, "Replaced Pictures/cat.png") {
		t.Fatalf("replace: %+v", res)
	}

	// Then a push, which asks, copies it to the phone.
	done := make(chan *Result, 1)
	go func() { done <- s.Files(ctx, FilesRequest{Action: "push", Paths: []string{"Pictures/cat.png"}}) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != filesRule || n.Title != "Copy “Pictures/cat.png” to Pixel 9?" {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusSettled)
	if string(x.files["Pictures/cat.png"].data) != "woof" {
		t.Fatalf("phone files %v", x.files)
	}

	// Bad calls write nothing.
	for _, req := range []FilesRequest{
		{Action: "upload", Paths: []string{"../escape.txt"}, Body: strings.NewReader("x")},
		{Action: "upload", Paths: []string{".bashrc"}, Body: strings.NewReader("x")},
		{Action: "upload", Paths: []string{"a.txt", "b.txt"}, Body: strings.NewReader("x")},
		{Action: "upload", Body: strings.NewReader("x")},
		{Action: "upload", Paths: []string{"Pictures"}, Body: strings.NewReader("x")},
		{Action: "upload", Paths: []string{"nothing.txt"}},
	} {
		if res := s.Files(ctx, req); res.Error != ErrInvalidArgument {
			t.Errorf("%+v: %+v", req.Paths, res)
		}
	}
	if res := s.Files(ctx, FilesRequest{Action: "upload", Paths: []string{"nothing.txt"}}); !strings.Contains(res.Message, dir) {
		t.Errorf("no contents, no folder named: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("wrote out of the folder: %v", err)
	}

	var texts []string
	for _, st := range taskSteps(g, s) {
		if st.Tool == "phone_files" {
			texts = append(texts, st.Text)
		}
	}
	want := []string{"Received “Pictures/cat.png” (4 B) from the agent", "Received “Pictures/cat.png” (4 B) from the agent",
		"Received “Pictures/cat.png” (4 B) from the agent", "Waiting for you: Copy “Pictures/cat.png” to Pixel 9",
		"Copied 1 file to the phone: Pictures/cat.png"}
	if strings.Join(texts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps %q", texts)
	}
}

// Check looks, and makes nothing: no folder, no file.
func TestPhoneFilesUploadCheck(t *testing.T) {
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Send a picture")
	res := s.Files(context.Background(), FilesRequest{Action: "upload", Paths: []string{`Pictures\cat.png`}, Check: true})
	mustStatus(t, res, StatusSettled)
	if res.Device != "pixel" || len(res.Files) != 1 || res.Files[0].Path != "Pictures/cat.png" {
		t.Fatalf("check %+v", res)
	}
	if _, err := os.Stat(filepath.Join(g.FilesDir("pixel"), "Pictures")); !os.IsNotExist(err) {
		t.Fatalf("a check made the folder: %v", err)
	}
	mustStatus(t, upload(s, "Pictures/cat.png", "meow", false), StatusSettled)
	res = s.Files(context.Background(), FilesRequest{Action: "upload", Paths: []string{"Pictures/cat.png"}, Check: true})
	if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "already in the folder on Pinky's computer (4 B,") {
		t.Fatalf("check of a name that is taken: %+v", res)
	}
	res = s.Files(context.Background(), FilesRequest{Action: "upload", Paths: []string{"Pictures/cat.png"}, Check: true, Replace: true})
	mustStatus(t, res, StatusSettled)
}

func TestPhoneFilesUploadLimits(t *testing.T) {
	old := uploadLimit
	uploadLimit = 8
	t.Cleanup(func() { uploadLimit = old })
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Send a picture")
	dir := g.FilesDir("pixel")

	mustStatus(t, upload(s, "eight.bin", "12345678", false), StatusSettled)
	if res := upload(s, "nine.bin", "123456789", false); res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "larger than 8 B") {
		t.Fatalf("too large: %+v", res)
	}
	// A sender that breaks off leaves nothing behind, no temporary file
	// either.
	res := s.Files(context.Background(), FilesRequest{Action: "upload", Paths: []string{"half.bin"},
		Body: io.MultiReader(strings.NewReader("1234"), errReader{errors.New("connection reset")})})
	if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "broke off after 4 B") {
		t.Fatalf("broken upload: %+v", res)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "eight.bin" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("left in the folder: %v", names)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// Links in the local folder are not followed: an upload replaces a link
// where its file goes (with replace) and refuses a folder that is a link.
func TestPhoneFilesUploadLinks(t *testing.T) {
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Send a picture")
	dir := g.FilesDir("pixel")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	keep := filepath.Join(outside, "bashrc")
	if err := os.WriteFile(keep, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keep, filepath.Join(dir, "notes.txt")); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "Pictures")); err != nil {
		t.Fatal(err)
	}
	if res := upload(s, "notes.txt", "new", false); res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "already in the folder on Pinky's computer") {
		t.Fatalf("upload over a link: %+v", res)
	}
	mustStatus(t, upload(s, "notes.txt", "new", true), StatusSettled)
	if b, _ := os.ReadFile(keep); string(b) != "keep me" {
		t.Fatalf("wrote through the link: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "notes.txt")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("the link is still there: %v %v", fi, err)
	}
	if res := upload(s, "Pictures/cat.png", "meow", false); res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "not a folder") {
		t.Fatalf("upload into a linked folder: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(outside, "cat.png")); !os.IsNotExist(err) {
		t.Fatalf("wrote through the folder link: %v", err)
	}
}

// Uploading follows push's rules: read-only and watch-only agents and
// read-only mode refuse it; iPhones have no Pinky folder.
func TestPhoneFilesUploadRules(t *testing.T) {
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Send a picture")
	for _, mode := range []ScopeMode{ScopeReadOnly, ScopeWatchOnly} {
		g.SetAgentScope(agent.Name, Scope{Mode: mode})
		if res := upload(s, "cat.png", "meow", false); res.Error != ErrScopeDenied {
			t.Fatalf("%s agent: %+v", mode, res)
		}
	}
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeEverything})
	if err := g.SetReadOnly("pixel", true); err != nil {
		t.Fatal(err)
	}
	if res := upload(s, "cat.png", "meow", false); res.Status != StatusBlocked || res.Rule != "Read-only" {
		t.Fatalf("read-only mode: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(g.FilesDir("pixel"), "cat.png")); !os.IsNotExist(err) {
		t.Fatalf("saved in read-only mode: %v", err)
	}
	steps := taskSteps(g, s)
	if last := steps[len(steps)-1]; last.Text != "Blocked by “Read-only”: receive “cat.png” from the agent" {
		t.Fatalf("step %q", last.Text)
	}

	ig := newIPhoneGateway(t)
	is, _ := connect(t, ig, "Files")
	res := is.Files(context.Background(), FilesRequest{Action: "upload", Paths: []string{"cat.png"}, Body: bytes.NewReader([]byte("meow"))})
	if res.Status != StatusSettled || !strings.HasSuffix(res.Message, "iPhone 17 has no Pinky folder, so it stays on the computer.") {
		t.Fatalf("upload for an iPhone: %+v", res)
	}
	res = is.Files(context.Background(), FilesRequest{Action: "upload", Paths: []string{"build/Tool.ipa"}, Body: bytes.NewReader([]byte("PK"))})
	if res.Status != StatusSettled || !strings.Contains(res.Message, `phone_app { action: "install", paths: ["build/Tool.ipa"] }`) {
		t.Fatalf("upload of an app for an iPhone: %+v", res)
	}
}

func TestSizeText(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 812: "812 B", 14_200: "14 KB", 3_240_000: "3.2 MB", 200_000_000: "200 MB", 1_500_000_000: "1.5 GB"} {
		if got := SizeText(n); got != want {
			t.Errorf("SizeText(%d) = %q, want %q", n, got, want)
		}
	}
}
