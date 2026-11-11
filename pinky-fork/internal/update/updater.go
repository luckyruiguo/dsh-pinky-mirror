package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrDevelopment means this build does not update itself: a development
// build (0.0.0-dev) or one without a release version.
var ErrDevelopment = errors.New("development builds don't update themselves")

// Updater checks the feed for the running Pinky and gets what it offers.
// Its methods may be called from any goroutine.
type Updater struct {
	// Version is the running version.
	Version string
	// Feed is the feed's URL; "" is FeedURL().
	Feed string
	// Platform picks the asset; "" is Platform().
	Platform string
	// Client fetches the feed and downloads; nil is NewClient().
	Client *http.Client
	// API is GitHub's REST API, which the feed and the package are fetched
	// through when their links fail (a 5xx, a timeout, no connection). ""
	// is GitHubAPI for the default feed, and no fallback for another Feed
	// or $PINKY_UPDATE_FEED.
	API string
	// Cache keeps a download while it is unpacked and checked.
	Cache string
	// Downloads is where a release the person installs by hand goes, such
	// as ~/Downloads; "" is Cache.
	Downloads string
	// Location is where Pinky is installed; left empty, Where finds it
	// with Locate the first time it is needed (which runs codesign, so
	// not on the way to the window).
	Location Location
	// Installer replaces Pinky where the location can replace itself; nil
	// is NewInstaller, whose helper logs to HelperLog.
	Installer Installer
	HelperLog string
	// StatePath is update.json; "" keeps the state in memory.
	StatePath string
	Log       *slog.Logger

	mu      sync.Mutex
	state   State
	loaded  bool
	locOnce sync.Once
}

// Where returns where Pinky is installed, looking it up once.
func (u *Updater) Where() Location {
	u.locOnce.Do(func() {
		u.mu.Lock()
		empty := u.Location == (Location{})
		u.mu.Unlock()
		if empty {
			l := Locate()
			u.mu.Lock()
			u.Location = l
			u.mu.Unlock()
			u.logger().Info("update: where Pinky runs", "app", l.App, "updates", l.Method, "problem", l.Problem)
		}
	})
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.Location
}

func (u *Updater) logger() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.New(slog.DiscardHandler)
}

// State returns a copy of what the updater remembers.
func (u *Updater) State() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.loadLocked()
	s := u.state
	s.Changes = append([]Change(nil), s.Changes...)
	s.CompanionByHand = maps.Clone(s.CompanionByHand)
	return s
}

// Change edits the state and saves it.
func (u *Updater) Change(fn func(*State)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.loadLocked()
	fn(&u.state)
	if u.StatePath != "" {
		if err := u.state.Save(u.StatePath); err != nil {
			u.logger().Warn("update: save state", "err", err)
		}
	}
}

func (u *Updater) loadLocked() {
	if !u.loaded {
		u.loaded = true
		if u.StatePath != "" {
			u.state = LoadState(u.StatePath)
		}
	}
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return NewClient()
}

func (u *Updater) api() string {
	if u.API != "" {
		return u.API
	}
	if u.Feed == "" && strings.TrimSpace(os.Getenv(FeedEnv)) == "" {
		return GitHubAPI
	}
	return ""
}

func (u *Updater) platform() string {
	if u.Platform != "" {
		return u.Platform
	}
	return Platform()
}

// Check reads the feed and returns a newer release with a build for this
// computer, or nil when Pinky is current. It returns ErrDevelopment for
// development builds, without asking the feed.
func (u *Updater) Check(ctx context.Context) (*Release, error) {
	if !IsRelease(u.Version) {
		return nil, ErrDevelopment
	}
	feedURL := u.Feed
	if feedURL == "" {
		feedURL = FeedURL()
	}
	f, err := FetchFeed(ctx, u.client(), feedURL, u.Version)
	if api := u.api(); api != "" && retryable(ctx, err) {
		u.logger().Warn("update: the feed failed, trying GitHub's API", "err", err)
		f2, err2 := fetchFeedAPI(ctx, u.client(), api, u.Version)
		if err2 != nil {
			return nil, fmt.Errorf("%w (through GitHub's API: %v)", err, err2)
		}
		f, err = f2, nil
	}
	if err != nil {
		return nil, err
	}
	u.Change(func(s *State) { s.Checked, s.Latest = time.Now(), f.Version })
	if !Newer(f.Version, u.Version) {
		return nil, nil
	}
	a, ok := f.Asset(u.platform())
	if !ok {
		return nil, fmt.Errorf("Pinky %s is out, but %w (%s)", f.Version, ErrNoAsset, u.platform())
	}
	return &Release{Feed: f, Asset: a}, nil
}

// Prepared is a release downloaded and checked, ready for its Method:
// Staged to swap in (MethodSelf), or File to run or show.
type Prepared struct {
	Release Release
	Method  Method
	File    string
	Staged  Staged
}

// MethodFor is how a release reaches this installation: Pinky replaces
// itself where it can; an installer runs only when the release's asset is
// one (an .exe or .msi); anything else is downloaded for the person.
func (u *Updater) MethodFor(r Release) Method {
	loc := u.Where()
	m := loc.Method
	switch {
	case m == MethodSelf && !loc.CanReplace():
		m = MethodDownload
	case (m == MethodInstaller || m == MethodSelf && loc.Setup) && !isInstaller(r.Asset.URL):
		m = MethodDownload
	}
	return m
}

func isInstaller(raw string) bool {
	name := strings.ToLower(assetName(raw))
	return strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".msi")
}

// Prepare downloads a release and checks it. Where Pinky replaces itself it
// also unpacks it next to the app and checks its signature, so that what
// is left is the swap; elsewhere the file goes to Downloads.
func (u *Updater) Prepare(ctx context.Context, r Release, progress func(done, total int64)) (*Prepared, error) {
	loc := u.Where()
	m := u.MethodFor(r)
	if m == MethodManager {
		return nil, fmt.Errorf("%s updates this Pinky: %s", loc.Manager, loc.Command)
	}
	dir := u.Cache
	if m == MethodDownload && u.Downloads != "" {
		dir = u.Downloads
	}
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "pinky-updates")
	}
	log := u.logger()
	log.Info("update: downloading", "version", r.Version, "url", r.Asset.URL, "size", r.Asset.Size)
	var alt opener
	if api := u.api(); api != "" {
		alt = func(ctx context.Context) (*http.Response, error) {
			log.Warn("update: the download failed, trying GitHub's API", "version", r.Version)
			return openAssetAPI(ctx, u.client(), api, u.Version, r.Feed, assetName(r.Asset.URL))
		}
	}
	file, err := download(ctx, u.client(), r.Asset, dir, u.Version, progress, alt)
	if err != nil {
		return nil, err
	}
	p := &Prepared{Release: r, Method: m, File: file}
	if m != MethodSelf {
		return p, nil
	}
	in := u.Installer
	if in == nil {
		if in, err = NewInstaller(loc, u.HelperLog, log); err != nil {
			os.Remove(file)
			return nil, err
		}
	}
	staged, err := in.Stage(ctx, file, r)
	os.Remove(file)
	if err != nil {
		return nil, err
	}
	p.Staged = staged
	log.Info("update: ready to install", "version", r.Version)
	return p, nil
}

// Failed records that a version could not be installed, so Pinky does not
// try it again by itself.
func (u *Updater) Failed(version string, err error) {
	u.Change(func(s *State) {
		s.Failed = version
		s.FailedWhy = err.Error()
	})
}
