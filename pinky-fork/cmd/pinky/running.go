package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/update"
)

// errShownOther means the app found another app running and brought its
// window to the front instead of starting.
var errShownOther = errors.New("Pinky is already open: showed its window")

// takeOverTimeout is how long the app waits for a daemon to hand over the
// socket.
const takeOverTimeout = 15 * time.Second

// takeOver deals with a gateway that already runs for this user when the
// app starts, as when an agent started `pinky daemon` through `pinky mcp`
// before the person opened Pinky.
//
//   - Another app brings its window to the front (unless this one starts
//     in the background), and this one ends with errShownOther.
//   - A daemon, or a gateway too old to say what it is, quits, and this app
//     takes its place. The restart marker keeps the sessions of agents
//     connected through `pinky mcp`: they reconnect to the app.
//
// It returns an error that says what to do when the other Pinky does not
// answer or does not quit.
func takeOver(c gatewayConfig, dirs api.Dirs, background bool, start func(gatewayConfig) (*runningGateway, error)) (*runningGateway, error) {
	ctl, err := api.DialControl(dirs)
	if err != nil {
		c.Log.Warn("another Pinky holds the gateway but does not answer", "err", err)
		return nil, &otherPinkyError{err: err}
	}
	// A gateway that hangs does not hang this one: closing the connection
	// ends the call waiting on it.
	hung := time.AfterFunc(10*time.Second, func() { ctl.Close() })
	defer hung.Stop()
	var v struct {
		Kind    string `json:"kind"`
		Version string `json:"version"`
	}
	if err := ctl.Call("version", nil, &v); err != nil {
		ctl.Close()
		return nil, &otherPinkyError{err: err}
	}
	if v.Kind == api.KindApp {
		if !background {
			allowForeground()
			if err := ctl.Call("show", nil, nil); err != nil {
				c.Log.Warn("show the running Pinky", "err", err)
			}
		}
		ctl.Close()
		return nil, errShownOther
	}
	c.Log.Info("taking over from a Pinky daemon", "its version", v.Version)
	// Agents' shims wait for the app, as for an update's restart.
	upd := newUpdater(dirs, c.Log)
	upd.MarkRestart()
	err = ctl.Call("quit", nil, nil)
	ctl.Close()
	if err != nil {
		update.RestartDone(filepath.Dir(upd.StatePath))
		return nil, &otherPinkyError{err: fmt.Errorf("it did not quit: %w", err)}
	}
	deadline := time.Now().Add(takeOverTimeout)
	for {
		rg, err := start(c)
		var running *api.RunningError
		if !errors.As(err, &running) {
			return rg, err
		}
		if time.Now().After(deadline) {
			update.RestartDone(filepath.Dir(upd.StatePath))
			return nil, &otherPinkyError{err: err}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// otherPinkyError means another Pinky holds the gateway and would not hand
// it over.
type otherPinkyError struct{ err error }

func (e *otherPinkyError) Error() string {
	return "another Pinky is running for this user and did not hand over: " + e.err.Error() +
		"; quit it (`pinky quit`, or end it in Task Manager or Activity Monitor), then open Pinky again"
}

func (e *otherPinkyError) Unwrap() error { return e.err }

// showStartError tells the person why Pinky could not start, in a dialog:
// opened from its icon, Pinky has no terminal to read stderr from.
func showStartError(err error, logPath string) {
	detail := err.Error()
	var other *otherPinkyError
	if errors.As(err, &other) {
		detail = locales.AppStartOtherPinky() + "\n\n" + other.err.Error()
	}
	if logPath != "" {
		detail += "\n\n" + locales.AppStartLog(logPath)
	}
	if !mygo.App.IsPackaged() {
		mygo.App.SetName("Pinky")
	}
	mygo.App.WhenReady(func() {
		go func() {
			mygo.Dialog.Error(locales.AppStartFailed(), detail)
			mygo.App.Quit()
		}()
	})
	_ = mygo.App.Run()
}
