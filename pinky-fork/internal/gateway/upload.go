package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/apppkg"
)

// phone_files upload: a file an agent sends Pinky, saved in the phone's
// local folder for a push or a phone_app install after it. Agents on this
// computer can write the folder with their own file tools; one on another
// computer, over HTTP, cannot. An upload is the first half of a push and
// follows its rules: read-only and watch-only agents and read-only mode
// refuse it. It leaves the phone alone, so it asks nothing; the push or
// install that follows asks the person.

// maxUploadPath bounds an uploaded file's name, folders included.
const maxUploadPath = 1000

// uploadLimit is MaxUpload, smaller in tests.
var uploadLimit int64 = MaxUpload

// uploadName cleans the name of a file to upload, a path inside the Pinky
// folder; why says what is wrong with one that cannot be a file there.
// Names are stricter than the paths other actions take, because the file
// is made on this computer, which may run Windows, and then on the phone:
// no hidden files, no characters that Windows or Android's shared storage
// refuse, no names Windows keeps for devices.
func uploadName(p string) (name, why string) {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	p = strings.TrimPrefix(p, "Pinky/")
	switch {
	case p == "":
		return "", "it is empty"
	case strings.HasPrefix(p, "/"):
		return "", "use a path inside the folder, such as photo.jpg"
	case !utf8.ValidString(p):
		return "", "it is not valid UTF-8"
	case len(p) > maxUploadPath:
		return "", fmt.Sprintf("it is longer than %d bytes", maxUploadPath)
	case strings.HasSuffix(p, "/"):
		return "", "it names a folder, not a file"
	}
	var parts []string
	for _, part := range strings.Split(p, "/") {
		if part == "" {
			continue
		}
		if why := partProblem(part); why != "" {
			return "", why
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "/"), ""
}

func partProblem(part string) string {
	switch {
	case part == "..":
		return ".. would leave the folder"
	case strings.HasPrefix(part, "."):
		return "names that start with a dot are hidden files, which Pinky does not take"
	case len(part) > 255:
		return "a name is longer than 255 bytes"
	case strings.HasSuffix(part, " ") || strings.HasSuffix(part, "."):
		return "a name ends with a space or a dot, which Windows drops"
	}
	for _, r := range part {
		if r < 0x20 || r == 0x7f {
			return "it holds a control character"
		}
		if strings.ContainsRune(`<>:"|?*`, r) {
			return fmt.Sprintf("it holds %q, which Windows and Android do not allow in names", r)
		}
	}
	base, _, _ := strings.Cut(part, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return base + " is a name Windows keeps for a device"
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
		return base + " is a name Windows keeps for a device"
	}
	return ""
}

// upload saves the file an agent sends as rel in dir, the phone's local
// folder, or with Check only checks that it could. canPush says whether
// the phone has a Pinky folder to copy it to.
func (g *Gateway) upload(ctx context.Context, l *lease, req FilesRequest, dir, rel, args string, canPush bool, start time.Time) *Result {
	d := l.dev
	if m := g.scopeOf(l.session).Mode; m == ScopeReadOnly || m == ScopeWatchOnly {
		return errResult(ErrScopeDenied, "This agent may only list and copy from the phone: the person gave it %s access.", strings.ReplaceAll(string(m), "_", "-"))
	}
	if g.policy.ReadOnly(d.id) {
		res := &Result{Status: StatusBlocked, Rule: "Read-only", Message: "Read-only mode is on, so nothing is copied to the phone, and Pinky takes no files for it."}
		if !req.Check {
			g.recordFiles(l, args, "Blocked by “Read-only”: "+receiveText(rel), res, start)
		}
		return res
	}
	old, why, err := uploadTarget(dir, rel)
	switch {
	case err != nil:
		return errResult(ErrInternal, "Reading the folder on Pinky's computer failed: %v", err)
	case why != "":
		return errResult(ErrInvalidArgument, "%s cannot be uploaded: %s.", rel, why)
	case req.Check && old != nil && !req.Replace:
		return existsResult(rel, old)
	case req.Check:
		// The phone, the cleaned name and what to do after, for the link.
		return &Result{Status: StatusSettled, Device: d.id, Files: []FileView{{Path: rel, Where: FileComputer}}, Message: nextHint(rel, d.name(), canPush)}
	case req.Body == nil:
		return errResult(ErrInvalidArgument, "Send the file's contents with it (content), or write the file into %s with your own file tools.", dir)
	}

	dst, err := localPath(dir, rel)
	if err != nil {
		return errResult(ErrInvalidArgument, "%s cannot be uploaded: %v.", rel, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".pinky-*")
	if err != nil {
		return errResult(ErrInternal, "The file cannot be saved in the folder on Pinky's computer: %v", err)
	}
	defer os.Remove(tmp.Name())
	body := &uploadReader{ctx: ctx, r: req.Body}
	n, err := io.Copy(tmp, io.LimitReader(body, uploadLimit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case body.err != nil && ctx.Err() != nil:
		return errResult(ErrInternal, "The call was cancelled. Nothing was saved.")
	case body.err != nil:
		return errResult(ErrInvalidArgument, "The upload broke off after %s: %v. Nothing was saved.", SizeText(n), body.err)
	case err != nil:
		res := errResult(ErrInternal, "Saving %s in the folder on Pinky's computer failed: %v", rel, err)
		g.recordFiles(l, args, "Failed: "+receiveText(rel), res, start)
		return res
	case n > uploadLimit:
		return errResult(ErrInvalidArgument, "The file is larger than %s, more than Pinky takes. Nothing was saved.", SizeText(uploadLimit))
	}

	// Look again: the file may have come or gone while it was sent.
	verb := "Saved"
	if fi, err := os.Lstat(dst); err == nil {
		switch {
		case fi.IsDir():
			return errResult(ErrInvalidArgument, "%s cannot be uploaded: a folder of that name is there.", rel)
		case !req.Replace && fi.Mode().IsRegular() && fi.Size() == n && sameBytes(tmp.Name(), dst):
			res := &Result{Status: StatusSettled, Files: []FileView{{Path: rel, Size: fi.Size(), ModTime: fi.ModTime(), Where: FileComputer}},
				Message: fmt.Sprintf("The folder on Pinky's computer already has this %s; nothing changed. %s", rel, nextHint(rel, d.name(), canPush))}
			g.recordFiles(l, args, fmt.Sprintf("Received %s (%s) from the agent", quoteName(rel), SizeText(n)), res, start)
			return res
		case !req.Replace:
			return existsResult(rel, fi)
		}
		verb = "Replaced"
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		res := errResult(ErrInternal, "Saving %s in the folder on Pinky's computer failed: %v", rel, err)
		g.recordFiles(l, args, "Failed: "+receiveText(rel), res, start)
		return res
	}
	mod := time.Now()
	if fi, err := os.Stat(dst); err == nil {
		mod = fi.ModTime()
	}
	res := &Result{Status: StatusSettled, Files: []FileView{{Path: rel, Size: n, ModTime: mod, Where: FileComputer}},
		Message: fmt.Sprintf("%s %s (%s) in the folder on Pinky's computer. %s", verb, rel, SizeText(n), nextHint(rel, d.name(), canPush))}
	g.recordFiles(l, args, fmt.Sprintf("Received %s (%s) from the agent", quoteName(rel), SizeText(n)), res, start)
	return res
}

// uploadTarget looks at where rel goes in dir without making anything:
// the file there now, if any, or why nothing can go there.
func uploadTarget(dir, rel string) (old fs.FileInfo, why string, err error) {
	parts := strings.Split(rel, "/")
	cur := dir
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, "", nil
		case err != nil:
			return nil, "", err
		case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
			return nil, part + " on the way is not a folder", nil
		}
	}
	fi, err := os.Lstat(filepath.Join(cur, parts[len(parts)-1]))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "", nil
	case err != nil:
		return nil, "", err
	case fi.IsDir():
		return nil, "a folder of that name is there", nil
	}
	return fi, "", nil
}

func existsResult(rel string, fi fs.FileInfo) *Result {
	return errResult(ErrInvalidArgument, "%s is already in the folder on Pinky's computer (%s, %s). Pass replace: true to replace it, or choose another name.",
		rel, SizeText(fi.Size()), fi.ModTime().Format("2006-01-02 15:04"))
}

// nextHint says what an agent can do with a file it uploaded: install an
// app, or copy anything else to the phone's Pinky folder when it has one.
func nextHint(rel, phone string, canPush bool) string {
	switch {
	case apppkg.IsAPK(rel) || apppkg.IsIOSApp(rel):
		return fmt.Sprintf(`Install it with phone_app { action: "install", paths: [%q] }, which the person approves first.`, rel)
	case canPush:
		return fmt.Sprintf(`Copy it to the phone with phone_files { action: "push", paths: [%q] }, which the person approves first.`, rel)
	}
	return fmt.Sprintf("%s has no Pinky folder, so it stays on the computer.", phone)
}

// receiveText is an upload as steps word it when it did not happen:
// "Failed: receive “photo.jpg” from the agent".
func receiveText(rel string) string {
	return "receive " + quoteName(rel) + " from the agent"
}

func quoteName(rel string) string { return "“" + rel + "”" }

// uploadReader reads an upload until the call is cancelled, and keeps the
// error reading failed with, which is the sender's and not the disk's.
type uploadReader struct {
	ctx context.Context
	r   io.Reader
	err error
}

func (u *uploadReader) Read(p []byte) (int, error) {
	if err := u.ctx.Err(); err != nil {
		u.err = err
		return 0, err
	}
	n, err := u.r.Read(p)
	if err != nil && err != io.EOF {
		u.err = err
	}
	return n, err
}

// sameBytes reports whether two files hold the same bytes.
func sameBytes(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		doneA := ea == io.EOF || ea == io.ErrUnexpectedEOF
		doneB := eb == io.EOF || eb == io.ErrUnexpectedEOF
		switch {
		case doneA && doneB:
			return true
		case ea != nil || eb != nil:
			return false
		}
	}
}

// SizeText writes a file size for people and models alike: 812 B, 14 KB,
// 3.2 MB, 200 MB.
func SizeText(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%d KB", (n+500)/1000)
	case n < 1000*1000*1000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + " MB"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e9), ".0") + " GB"
}
