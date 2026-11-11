package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
)

// GitHubAPI is GitHub's REST API. When a release download link on
// github.com fails (as all of them answered 504 for hours on 8 November
// 2026 while the API worked), Pinky gets the same files as assets of
// Repo's releases through it. The checksums in latest.json still decide
// whether a package is right, so the route a file took does not matter.
//
// Unauthenticated, the API allows 60 requests an hour per IP address, so
// it is only asked after the link failed, and with as few requests as can
// be: the release, then the asset.
const GitHubAPI = "https://api.github.com"

// maxReleaseSize bounds a release's description from the API (its notes
// and the list of its assets).
const maxReleaseSize = 4 << 20

// statusError is an HTTP answer other than 200.
type statusError struct {
	URL    string
	Code   int
	Status string
}

func (e *statusError) Error() string { return "GET " + e.URL + ": " + e.Status }

// retryable tells whether a failed fetch may work through another route:
// the server failed (5xx), timed out or stalled, or the connection did. A
// 404, a refused redirect, a bad certificate, a wrong checksum or size,
// and ctx ending are not.
func retryable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.Code >= 500
	}
	if errors.Is(err, ErrStalled) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var op *net.OpError
	var dns *net.DNSError
	return errors.As(err, &op) || errors.As(err, &dns) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

// apiRelease is what the API says about a release.
type apiRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"url"` // the API's URL of the asset
	} `json:"assets"`
}

// assetURLs maps the release's file names to the API's URLs of them.
func (r apiRelease) assetURLs() map[string]string {
	m := make(map[string]string, len(r.Assets))
	for _, a := range r.Assets {
		if a.Name != "" && a.URL != "" {
			m[a.Name] = a.URL
		}
	}
	return m
}

// getAPI fetches from the API, asking for accept: the API's JSON, or an
// asset's bytes (application/octet-stream), which it redirects to GitHub's
// file servers.
func getAPI(ctx context.Context, client *http.Client, raw, version, accept string) (*http.Response, error) {
	return fetch(ctx, client, raw, version, func(h http.Header) {
		h.Set("Accept", accept)
		h.Set("X-GitHub-Api-Version", "2022-11-28")
	})
}

// fetchRelease reads a release's description from the API: the newest
// release when tag is "".
func fetchRelease(ctx context.Context, client *http.Client, api, tag, version string) (apiRelease, error) {
	u := strings.TrimRight(api, "/") + "/repos/" + Repo + "/releases/latest"
	if tag != "" {
		u = strings.TrimRight(api, "/") + "/repos/" + Repo + "/releases/tags/" + url.PathEscape(tag)
	}
	resp, err := getAPI(ctx, client, u, version, "application/vnd.github+json")
	if err != nil {
		return apiRelease{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseSize+1))
	if err != nil {
		return apiRelease{}, err
	}
	if len(data) > maxReleaseSize {
		return apiRelease{}, errors.New("GitHub's description of the release is too large")
	}
	var r apiRelease
	if err := json.Unmarshal(data, &r); err != nil {
		return apiRelease{}, fmt.Errorf("GitHub's description of the release: %w", err)
	}
	return r, nil
}

// fetchFeedAPI reads latest.json from the newest release of Repo through
// the API at api: two requests, the release and the file. The feed
// remembers the API's URLs of the release's other files, so a package
// that falls back too needs one request.
func fetchFeedAPI(ctx context.Context, client *http.Client, api, version string) (Feed, error) {
	rel, err := fetchRelease(ctx, client, api, "", version)
	if err != nil {
		return Feed{}, err
	}
	assets := rel.assetURLs()
	u, ok := assets["latest.json"]
	if !ok {
		return Feed{}, fmt.Errorf("release %s has no latest.json", rel.Tag)
	}
	resp, err := getAPI(ctx, client, u, version, "application/octet-stream")
	if err != nil {
		return Feed{}, err
	}
	f, err := readFeed(resp)
	if err != nil {
		return Feed{}, err
	}
	if rel.Tag == f.Tag || rel.Tag == "v"+f.Version {
		f.apiAssets = assets
	}
	return f, nil
}

// openAssetAPI starts downloading the file name of release f through the
// API at api, looking the release up by its tag unless the feed came
// through the API and knows the file already.
func openAssetAPI(ctx context.Context, client *http.Client, api, version string, f Feed, name string) (*http.Response, error) {
	u := f.apiAssets[name]
	if u == "" {
		tag := f.Tag
		if tag == "" {
			tag = "v" + f.Version
		}
		rel, err := fetchRelease(ctx, client, api, tag, version)
		if err != nil {
			return nil, err
		}
		if u = rel.assetURLs()[name]; u == "" {
			return nil, fmt.Errorf("release %s has no %s", tag, name)
		}
	}
	return getAPI(ctx, client, u, version, "application/octet-stream")
}
