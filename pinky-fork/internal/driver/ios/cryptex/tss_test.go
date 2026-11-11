package cryptex

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"howett.net/plist"
)

// appleTSS stands in for Apple's signing server (gs.apple.com): it answers
// every request with body and keeps the requests.
type appleTSS struct {
	body string

	mu       sync.Mutex
	requests []map[string]interface{}
}

func (a *appleTSS) RoundTrip(r *http.Request) (*http.Response, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var req map[string]interface{}
	if _, err := plist.Unmarshal(b, &req); err != nil {
		return nil, fmt.Errorf("the request is no plist: %w", err)
	}
	a.mu.Lock()
	a.requests = append(a.requests, req)
	a.mu.Unlock()
	if r.URL.String() != tssURL {
		return nil, fmt.Errorf("request to %s", r.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(a.body)), Header: http.Header{}, Request: r}, nil
}

// mounterSignedBy is fake's mounter, with its tickets from tss.
func mounterSignedBy(fake *fakeCryptexd, tss *appleTSS) *CryptexDeveloperDiskImageMounter {
	client := tssClient{h: &http.Client{Transport: tss, Timeout: 10 * time.Second}}
	m := fake.mounter()
	m.sign = func(req map[string]interface{}) ([]byte, error) {
		return client.requestTicket(req, "Cryptex1,Ticket")
	}
	return m
}

// appleRefusal is Apple's answer, on 2026-11-08, to a Cryptex1 ticket
// request for Xcode 26.0.1's developer disk image (17B48), and for any
// image it does not sign.
const appleRefusal = "STATUS=94&MESSAGE=This device isn't eligible for the requested build."

// TestCryptexMountRefusedByApple installs nothing when Apple's signing
// server refuses the image, and says what Apple answered.
func TestCryptexMountRefusedByApple(t *testing.T) {
	img := writeTestImage(t)
	fake := &fakeCryptexd{t: t, window: 1000, resetFinished: true}
	tss := &appleTSS{body: appleRefusal}
	err, ended := within(10*time.Second, func() error { return mounterSignedBy(fake, tss).MountImage(img.dir) })
	if !ended {
		t.Fatal("MountImage did not end within 10 s")
	}
	want := `MountImage: failed to get Cryptex1 ticket from Apple: unexpected status in response 94: "This device isn't eligible for the requested build."`
	if err == nil || err.Error() != want {
		t.Fatalf("MountImage = %v, want %s", err, want)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var routines []interface{}
	for _, r := range fake.requests {
		routines = append(routines, r["routine"])
	}
	if got := fmt.Sprint(routines); got != "[read-personalization-id get-nonce]" {
		t.Errorf("routines %s; nothing is installed without a ticket", got)
	}
	if len(fake.uploads) != 0 {
		t.Errorf("uploaded %d files without a ticket", len(fake.uploads))
	}
	if len(tss.requests) != 1 {
		t.Fatalf("%d requests to Apple, want 1", len(tss.requests))
	}
	// What Apple decides by: the image's digests, which differ between
	// the builds it signs and those it refuses.
	for _, key := range []string{cryptexInfoPlistKey, cryptexTrustCacheKey, cryptexVolumeKey} {
		entry, _ := tss.requests[0][key].(map[string]interface{})
		if digest, _ := entry["Digest"].([]byte); len(digest) != 48 {
			t.Errorf("the request lacks the digest of %s: %v", key, tss.requests[0][key])
		}
	}
}

// TestCryptexMountWithAppleTicket installs the image with the ticket Apple's
// signing server answers with, in the form gs.apple.com sends it.
func TestCryptexMountWithAppleTicket(t *testing.T) {
	img := writeTestImage(t)
	fake := &fakeCryptexd{t: t, window: 1000, resetFinished: true}
	ticket := bytes.Repeat([]byte("apple ticket "), 300)
	b, err := plist.MarshalIndent(map[string]interface{}{"Cryptex1,Ticket": ticket}, plist.XMLFormat, "\t")
	if err != nil {
		t.Fatal(err)
	}
	tss := &appleTSS{body: "STATUS=0&MESSAGE=SUCCESS&REQUEST_STRING=" + string(b)}
	err, ended := within(10*time.Second, func() error { return mounterSignedBy(fake, tss).MountImage(img.dir) })
	if !ended {
		t.Fatal("MountImage did not end within 10 s")
	}
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !bytes.Equal(fake.uploads["im4m"], ticket) {
		t.Errorf("installed with a ticket of %d bytes, want Apple's %d", len(fake.uploads["im4m"]), len(ticket))
	}
}
