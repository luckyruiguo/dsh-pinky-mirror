package mcp

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/gateway"
)

// Files a call saved for the agent on Pinky's computer (gateway.SavedFile:
// phone_observe { save }, phone_release { export }) reach an agent on this
// computer as paths, which it reads with its own file tools. One on
// another computer, over HTTP, gets a download link for each instead, the
// way phone_files upload gives upload links: it works once, for
// downloadTTL, and its token stands for the agent token, so a shell
// command can fetch the file without it.

// DownloadPath is where download links point, followed by the link's
// token.
const DownloadPath = "/download/"

// downloadTTL is how long a download link works. It works once.
const downloadTTL = 10 * time.Minute

// downloadLink is a saved file a link stands for.
type downloadLink struct {
	path    string
	expires time.Time
}

// result renders a gateway result for an agent, with download links for
// the files it saved when the agent is on another computer.
func (s *Server) result(req *sdk.CallToolRequest, remote bool, res *gateway.Result) *sdk.CallToolResult {
	if !remote || len(res.Saved) == 0 {
		return render(res)
	}
	base, where := "", " at the address you reach Pinky's /mcp at"
	if req != nil && req.Extra != nil {
		if b := req.Extra.Header.Get(linkBaseHeader); b != "" {
			base, where = b, " (if this address does not reach Pinky from your machine, use the one you reach its /mcp at, with the same path)"
		}
	}
	it := "it"
	if len(res.Saved) > 1 {
		it = "them"
	}
	var b strings.Builder
	b.WriteString(res.Message)
	fmt.Fprintf(&b, "\nPinky keeps the files on its own computer. Download %s from your own shell%s within %s; each link works once:",
		it, where, approxMinutes(downloadTTL))
	for _, f := range res.Saved {
		fmt.Fprintf(&b, "\ncurl -fsS -o %s '%s'", shellQuote(path.Base(f.Name)), base+DownloadPath+s.newDownload(f.Path))
	}
	cp := *res
	cp.Saved, cp.Message = nil, strings.TrimSpace(b.String())
	return render(&cp)
}

// newDownload returns the token of a new link to the file at p.
func (s *Server) newDownload(p string) string {
	token := rand.Text()
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for t, d := range s.downloads {
		if now.After(d.expires) {
			delete(s.downloads, t)
		}
	}
	s.downloads[token] = &downloadLink{path: p, expires: now.Add(downloadTTL)}
	return token
}

// takeDownload returns the file a link's token stands for and forgets it,
// or nil for an unknown, used or expired one.
func (s *Server) takeDownload(token string) *downloadLink {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.downloads[token]
	delete(s.downloads, token)
	if d == nil || time.Now().After(d.expires) {
		return nil
	}
	return d
}

// forgetDownloads drops every download link, as when the person turns
// remote agents off.
func (s *Server) forgetDownloads() {
	s.mu.Lock()
	clear(s.downloads)
	s.mu.Unlock()
}

// serveDownload sends the file a download link stands for, once.
func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request) {
	reply := func(code int, msg string) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(code)
		fmt.Fprintln(w, msg)
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		reply(http.StatusMethodNotAllowed, "Fetch the file with GET, as curl does.")
		return
	}
	d := s.takeDownload(strings.TrimPrefix(r.URL.Path, DownloadPath))
	if d == nil {
		reply(http.StatusNotFound, "This download link is unknown, used or expired. Save the file again for a new link.")
		return
	}
	f, size, err := openRegular(d.path)
	if err != nil {
		reply(http.StatusNotFound, "The file is no longer on Pinky's computer.")
		return
	}
	defer f.Close()
	name := filepath.Base(d.path)
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" || strings.HasPrefix(ct, "text/html") {
		// A report opens from the agent's disk, never from Pinky's
		// address.
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	io.Copy(w, f)
}

// openRegular opens p when it is a regular file, not a link to one.
func openRegular(p string) (*os.File, int64, error) {
	li, err := os.Lstat(p)
	if err != nil {
		return nil, 0, err
	}
	if !li.Mode().IsRegular() {
		return nil, 0, errors.New("not a regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil || !os.SameFile(li, fi) {
		f.Close()
		return nil, 0, errors.New("the file changed")
	}
	return f, fi.Size(), nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
