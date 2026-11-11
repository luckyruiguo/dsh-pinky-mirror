package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/egoist/mygo"

	"github.com/DIYgod/pinky/internal/secret"
	"github.com/DIYgod/pinky/ui"
	"github.com/DIYgod/pinky/ui/demogw"
)

// runGateway runs the client against the real gateway on the demo phones.
// Claude Code connects to Pixel 9 (fake) a moment after start, which the
// gateway asks the person to allow, then reorders lunch and asks to pay.
// The Demo menu starts more agents.
func runGateway(theme, page string, dev, hidden, withVideo bool, timeout time.Duration) error {
	dir, err := os.MkdirTemp("", "pinky-ui-demo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	w, err := demogw.New(demogw.Options{DataDir: dir, Video: withVideo, ApprovalTimeout: timeout})
	if err != nil {
		return err
	}
	defer w.Gateway.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := func(name, device string, run func(*demogw.Agent, context.Context) error) {
		go func() {
			ag := w.NewAgent(name, device)
			ag.Log = log.Printf
			defer ag.Close()
			if err := run(ag, ctx); err != nil && !errors.Is(err, demogw.ErrEnded) && ctx.Err() == nil {
				log.Printf("%s: %v", name, err)
			}
		}()
	}
	lunch := func() { start("Claude Code", "fake-pixel-9", (*demogw.Agent).ReorderLunch) }
	signIn := func() { start("Claude Code", "fake-iphone-17", (*demogw.Agent).SignIn) }
	go func() {
		time.Sleep(2 * time.Second)
		lunch()
	}()

	settings := ui.DefaultSettings()
	settings.DeveloperMode = dev
	click := func(f func()) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) { f() }
	}
	menu := &mygo.MenuItem{Label: "Demo", Submenu: []*mygo.MenuItem{
		{Label: "Claude Code: Reorder Lunch on Pixel", Click: click(lunch)},
		{Label: "Claude Code: Sign In on iPhone (Handoff)", Click: click(signIn)},
		{Label: "Codex: Reorder Lunch on Pixel (New Agent)", Click: click(func() {
			start("Codex", "fake-pixel-9", (*demogw.Agent).ReorderLunch)
		})},
		mygo.Separator(),
		{Label: "Lock iPhone", Click: click(func() { w.IPhone.SetLocked(true) })},
		{Label: "Unlock iPhone", Click: click(func() { w.IPhone.SetLocked(false) })},
		{Label: "Unplug iPhone", Click: click(func() { w.IPhone.SetOffline(true) })},
		{Label: "Plug In iPhone", Click: click(func() { w.IPhone.SetOffline(false) })},
	}}
	return ui.Run(w.Gateway, ui.Options{
		Version:   "0.0.0-demo",
		Theme:     theme,
		Settings:  &settings,
		StartPage: page,
		Hidden:    hidden,
		Menus:     []*mygo.MenuItem{menu},
		Secrets:   &secret.Memory{},
		// The demo's gateway serves no HTTP; the token only fills Settings.
		AgentToken: demoToken,
	})
}
