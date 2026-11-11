package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/awake"
	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screenstream"
	"github.com/DIYgod/pinky/internal/telemetry"
	"github.com/DIYgod/pinky/internal/update"
	"github.com/DIYgod/pinky/ui"
)

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("pinky daemon", flag.ContinueOnError)
	var c gatewayConfig
	c.flags(fs)
	verbose := fs.Bool("v", false, "log debug messages")
	noUpdates := fs.Bool("no-update-check", false, "do not look for new versions of Pinky")
	httpAddr := fs.String("http", "", `also serve MCP over Streamable HTTP at this address, such as "`+ui.DefaultRemoteAddr+`"; each request needs the agent token (`+"`pinky token`"+`)`)
	screenAddr := fs.String("screen", "", `also serve the phone's live screen as MJPEG (DSH sidebar), such as "127.0.0.1:7466"`)
	screenFPS := fs.Int("screen-fps", 20, "screen stream: pictures a second at most")
	screenWidth := fs.Int("screen-width", 720, "screen stream: the picture's longest side, in pixels")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c.Log = newLogger(os.Stderr, *verbose)
	if dirs, err := api.DefaultDirs(); err == nil && dirs.Ensure() == nil {
		crashes := captureCrashes(dirs, "daemon", c.Log)
		// Not a plain Stop: a panic runs deferred calls, and a Stop removed the
	// crash file before the runtime wrote the panic to it.
	defer func() { crashes.Exit(recover()) }()
		c.Crashes = crashes
	}

	rg, err := startGateway(c)
	if err != nil {
		return err
	}
	// Once the gateway has closed, as the app's settings say then.
	var restarting bool
	defer func() {
		s := loadSettings(filepath.Join(rg.Dirs.Config, "settings.json"), c.Log)
		stopADBAtQuit(s.StopADBOnQuit, c.Fake, restarting, c.Log)
	}()
	defer rg.Close()
	// Agents' copies of Pinky's skill follow this version.
	go refreshSkills(c.Log)
	// Recording, retention, iPhones over Wi-Fi and usage statistics follow
	// the app's settings, when there are any.
	s := loadSettings(filepath.Join(rg.Dirs.Config, "settings.json"), c.Log)
	rg.G.SetRecording(s.Record)
	rg.G.SetRetention(s.KeepDays)
	rg.G.SetWiFi(s.IOSWiFi)
	tctx, tstop := context.WithCancel(context.Background())
	defer tstop()
	tel := startTelemetry(tctx, rg, c.Crashes, s, "daemon", c.Fake, c.Log)
	defer tel.Close()
	if !c.Fake {
		// The computer stays awake as for the app (awake.go); fake phones
		// need none of that.
		actx, astop := context.WithCancel(context.Background())
		adone := make(chan struct{})
		go func() {
			defer close(adone)
			keepDaemonAwake(actx, rg.G, s.KeepAwakeAway, awake.Hold, c.Log)
		}()
		defer func() { astop(); <-adone }()
	}
	if *httpAddr != "" {
		if err := rg.Remote.Set(true, *httpAddr); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "MCP over HTTP at %s\n", rg.Remote.URL())
	}

	// 画面出口：把手机实时画面以 MJPEG 提供给 Web 客户端（DSH 侧边栏插件）。
	// 与 Pinky 自己的 mirror 走同一条数据通路（Gateway.Stream → video.Player），
	// 只是送到 HTTP 而非窗口。默认关闭，加 --screen 开启。
	if *screenAddr != "" {
		ss := screenstream.New(rg.G, screenstream.Options{
			FPS:   *screenFPS,
			Width: *screenWidth,
			Log:   c.Log,
		})
		mux := http.NewServeMux()
		mux.Handle("/screen/", ss)
		srv := &http.Server{Addr: *screenAddr, Handler: mux}
		go func() {
			c.Log.Info("screen stream listening", "addr", *screenAddr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				c.Log.Warn("screen stream stopped", "err", err)
			}
		}()
		defer srv.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rg.srv.Quit = stop
	// The app takes over from a daemon when the person opens it.
	rg.srv.Kind = api.KindDaemon
	upd := newUpdater(rg.Dirs, c.Log)
	if err := startedUpdate(upd, c.Log).Confirm(); err != nil {
		c.Log.Warn("update: confirm", "err", err)
	}
	if !*noUpdates {
		go daemonUpdates(ctx, upd, c.Log)
	}
	err = rg.Serve(ctx)
	restarting = update.Restarting(rg.Dirs.Config)
	c.Log.Info("gateway stopping")
	return err
}

// gatewayConfig is what the daemon and the app share.
type gatewayConfig struct {
	Fake            bool
	ApprovalTimeout time.Duration
	IdleTimeout     time.Duration
	NoScreenshots   bool
	Log             *slog.Logger
	// PIN hands out a phone's saved PIN for unlocking from the computer;
	// nil turns that off (the daemon has no one to confirm it).
	PIN func(ctx context.Context, deviceID string) (string, error)
	// Crashes is the process's crash capture, for usage statistics.
	Crashes *telemetry.Crashes
}

func (c *gatewayConfig) flags(fs *flag.FlagSet) {
	fs.BoolVar(&c.Fake, "fake", false, "use scripted fake phones; their history goes to a separate folder")
	fs.DurationVar(&c.ApprovalTimeout, "approval-timeout", 60*time.Second, "how long the person has to answer an approval")
	fs.DurationVar(&c.IdleTimeout, "idle-timeout", 10*time.Minute, "end a lease after this long without calls")
	fs.BoolVar(&c.NoScreenshots, "no-screenshots", false, "do not store before and after screenshots of each step")
}

func newLogger(w *os.File, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// runningGateway is a gateway with its socket, ready to serve agents and
// clients.
type runningGateway struct {
	Dirs    api.Dirs
	DataDir string
	Policy  *policy.Engine
	G       *gateway.Gateway
	// Remote serves MCP over HTTP while remote agents are on.
	Remote *remoteAgents
	// MCP serves agents, over the socket and HTTP.
	MCP *mcp.Server

	ln  net.Listener
	srv *api.Server
	log *slog.Logger
}

// startGateway takes the per-user socket, loads the rules and starts the
// gateway with real or fake phones.
func startGateway(c gatewayConfig) (*runningGateway, error) {
	log := c.Log
	dirs, err := api.DefaultDirs()
	if err != nil {
		return nil, err
	}
	ln, err := api.Listen(dirs)
	if err != nil {
		var running *api.RunningError
		if errors.As(err, &running) {
			return nil, err
		}
		return nil, fmt.Errorf("listen on %s: %w", dirs.Socket(), err)
	}
	pol, err := policy.Load(dirs.Rules(), dirs.Apps())
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("rules: %w", err)
	}
	var providers []driver.Provider
	dataDir := dirs.Data
	if c.Fake {
		providers, _ = demo.World()
		for cat, e := range demo.PolicyApps() {
			pol.AddApps(cat, e)
		}
		dataDir = filepath.Join(dirs.Data, "fake")
	} else {
		providers = realProviders(log)
		if len(providers) == 0 {
			log.Warn("no device provider could start (adb server, usbmuxd); start an iOS Simulator or Android emulator from the app's Add phone, or use --fake to try Pinky with fake phones")
		}
	}

	g, err := gateway.New(gateway.Options{
		Providers: providers, DataDir: dataDir, Policy: pol, Logger: log,
		ApprovalTimeout: c.ApprovalTimeout, IdleTimeout: c.IdleTimeout, NoScreenshots: c.NoScreenshots,
		// Fake phones cannot hurt anything, so agents need no first approval
		// and socket clients may answer approvals (for scripted tests).
		TrustNewAgents: c.Fake,
		PIN:            c.PIN,
	})
	if err != nil {
		ln.Close()
		return nil, err
	}
	tok, err := api.EnsureToken(dirs.TokenFile())
	if err != nil {
		g.Close()
		ln.Close()
		return nil, fmt.Errorf("agent token: %w", err)
	}
	m := mcp.New(g, version)
	srv := &api.Server{Gateway: g, MCP: m, Token: tok, Trusted: c.Fake, Version: version, Log: log}
	log.Info("gateway ready", "socket", dirs.Socket(), "data", dataDir, "devices", len(g.Devices()), "fake", c.Fake, "pid", os.Getpid())
	return &runningGateway{Dirs: dirs, DataDir: dataDir, Policy: pol, G: g, Remote: newRemoteAgents(m, tok, log), MCP: m,
		ln: ln, srv: srv, log: log}, nil
}

// Serve answers agents and clients on the socket until ctx ends.
func (r *runningGateway) Serve(ctx context.Context) error {
	return r.srv.Serve(ctx, r.ln)
}

// Close stops the gateway and releases the socket.
func (r *runningGateway) Close() {
	if r.Remote != nil {
		r.Remote.Close()
	}
	r.G.Close()
	r.ln.Close()
}
