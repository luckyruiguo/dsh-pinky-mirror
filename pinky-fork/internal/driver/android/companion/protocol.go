package companion

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DIYgod/pinky/internal/driver"
)

// ProtocolVersion is the protocol this client speaks. The app reports its
// own in ping; see docs/notes/companion-protocol.md.
const ProtocolVersion = 1

// Info is the ping result.
type Info struct {
	Protocol     int    `json:"protocol"`
	Version      string `json:"version"`
	VersionCode  int    `json:"version_code"`
	SDK          int    `json:"sdk"`
	Release      string `json:"release"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Time         int64  `json:"time"` // phone clock, Unix milliseconds
	UptimeMS     int64  `json:"uptime_ms"`
	Displays     []int  `json:"displays"`
	// Features lists additions to protocol 1 this app has on this phone,
	// such as FeatureDisplays. Apps built before round 3 send none.
	Features            []string `json:"features,omitempty"`
	Connections         int      `json:"connections"` // open connections, this one included
	IncludeNotImportant bool     `json:"include_not_important"`
	IdleDisableMS       int64    `json:"idle_disable_ms"`
}

// FeatureDisplays means tree, set_text, events and wait_idle take displays
// other than 0, and events name their display (Android 11 and later). An
// app without it ignores the display parameter of set_text, which would
// then write on the main display.
const FeatureDisplays = "displays"

// FeatureSetTextFallback means set_text does more than ACTION_SET_TEXT:
// when the app refuses it, the text goes in through the field's input
// connection (Android 13 and later) or as a paste, and the error lists
// why each way was refused. The app waits for the text to land, reports
// SetTextResult.Method and Password, and does not submit a field that
// reads otherwise than asked. An app without it reads the field back at
// once, so its Verified may miss text that lands a moment later.
const FeatureSetTextFallback = "set_text_fallback"

// How set_text wrote the text (SetTextResult.Method).
const (
	MethodSetText         = "set_text"         // ACTION_SET_TEXT
	MethodInputConnection = "input_connection" // committed through the field's input connection
	MethodPaste           = "paste"            // ACTION_PASTE, the clipboard cleared afterwards
)

// Has reports whether the app has a feature.
func (i *Info) Has(feature string) bool {
	for _, f := range i.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// Tree is a tree response: every window on one display, top-most first.
type Tree struct {
	Display      Display  `json:"display"`
	Locked       bool     `json:"locked"`        // the keyguard is showing
	SecureLocked bool     `json:"secure_locked"` // and needs a PIN, pattern or password
	Windows      []Window `json:"windows"`
	Nodes        int      `json:"nodes"`
	Truncated    bool     `json:"truncated"`
	Time         int64    `json:"time"`       // phone clock, Unix milliseconds
	TookMS       int64    `json:"took_ms"`    // time the phone spent on the request
	WindowsMS    int64    `json:"windows_ms"` // of which listing the windows
}

// Display describes the display a tree was read from. Width and Height are
// in pixels for the current rotation.
type Display struct {
	ID         int `json:"id"`
	Width      int `json:"width"`
	Height     int `json:"height"`
	DensityDPI int `json:"density_dpi"`
	Rotation   int `json:"rotation"` // degrees: 0, 90, 180 or 270
}

// Window types as the protocol names them.
const (
	WindowApplication          = "application"
	WindowInputMethod          = "input_method"
	WindowSystem               = "system"
	WindowAccessibilityOverlay = "accessibility_overlay"
	WindowSplitScreenDivider   = "split_screen_divider"
	WindowMagnificationOverlay = "magnification_overlay"
)

// Window is one accessibility window.
type Window struct {
	ID     int    `json:"id"`
	Type   string `json:"type"`
	Layer  int    `json:"layer"`
	Title  string `json:"title,omitempty"`
	Bounds Bounds `json:"bounds"`
	// Region is the window's touchable region, sent on Android 13 and
	// later when it is more than the bounds' rectangle: a floating
	// toolbar's window spans the screen's lower half but covers only the
	// toolbar and a strip. Windows below are hidden by the region.
	Region  []Bounds `json:"region,omitempty"`
	Active  bool     `json:"active,omitempty"`
	Focused bool     `json:"focused,omitempty"`
	PIP     bool     `json:"pip,omitempty"`
	Package string   `json:"package,omitempty"`
	// App is the package's display name. The app loads names in the
	// background, so the first tree after an app opens may lack it.
	App  string `json:"app,omitempty"`
	Root *Node  `json:"root,omitempty"`
	// Timing of this window: fetching its root (a round trip to the app)
	// and walking its nodes, and how many nodes it had.
	RootMS int64 `json:"root_ms"`
	WalkMS int64 `json:"walk_ms"`
	Nodes  int   `json:"nodes"`
}

// Bounds is a rectangle as [left, top, right, bottom] in screen pixels.
type Bounds [4]int

// Node is one accessibility node. Boolean fields are only sent when true.
type Node struct {
	Class         string  `json:"class,omitempty"`
	Text          string  `json:"text,omitempty"` // never sent for password fields
	Desc          string  `json:"desc,omitempty"`
	Hint          string  `json:"hint,omitempty"`
	ID            string  `json:"id,omitempty"`    // resource id, "pkg:id/name"
	State         string  `json:"state,omitempty"` // state description (API 30+)
	Role          string  `json:"role,omitempty"`  // role description, if the app set one
	Bounds        Bounds  `json:"bounds"`
	Clickable     bool    `json:"clickable,omitempty"`
	LongClickable bool    `json:"long_clickable,omitempty"`
	Checkable     bool    `json:"checkable,omitempty"`
	Checked       bool    `json:"checked,omitempty"`
	Editable      bool    `json:"editable,omitempty"`
	Scrollable    bool    `json:"scrollable,omitempty"`
	Focusable     bool    `json:"focusable,omitempty"`
	Focused       bool    `json:"focused,omitempty"`
	Selected      bool    `json:"selected,omitempty"`
	Password      bool    `json:"password,omitempty"`
	Heading       bool    `json:"heading,omitempty"`
	Disabled      bool    `json:"disabled,omitempty"`
	Invisible     bool    `json:"invisible,omitempty"` // only with DumpOptions.Invisible
	Handle        int64   `json:"node"`                // stable while the view lives
	Children      []*Node `json:"children,omitempty"`
}

// SetTextResult is the set_text result.
type SetTextResult struct {
	Window int    `json:"window"`
	Node   int64  `json:"node"`
	Text   string `json:"text"` // the field's text afterwards; empty for passwords
	// Verified reports whether the field now holds exactly the requested
	// text. It is false when the app changed it (length limits, filters)
	// and always false for password fields, whose text is unreadable.
	Verified bool `json:"verified"`
	// Submitted reports whether the IME action ran (Android 11+). The
	// client falls back to the Enter key when it did not, unless the field
	// reads otherwise than asked (FeatureSetTextFallback).
	Submitted bool `json:"submitted"`
	// Method is how the text went in, one of the Method constants; empty
	// from apps without FeatureSetTextFallback, which only use
	// ACTION_SET_TEXT.
	Method string `json:"method,omitempty"`
	// Password reports a password field, whose text cannot be read back:
	// Text and Verified say nothing then (FeatureSetTextFallback).
	Password bool `json:"password,omitempty"`
}

// Error is an error the companion app returned.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("companion: %s: %s", e.Code, e.Message) }

// Unwrap maps protocol error codes onto the driver's errors.
func (e *Error) Unwrap() error {
	switch e.Code {
	case "not_found":
		return driver.ErrNotFound
	case "unsupported":
		return driver.ErrUnsupported
	}
	return nil
}

// ErrNotRunning means the accessibility service is off or the app is not
// installed, so nothing listens on the companion socket.
var ErrNotRunning = errors.New("companion: the accessibility service is not running")

type request struct {
	ID  int64  `json:"id"`
	Cmd string `json:"cmd"`
}

type response struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
	Event  string          `json:"event"`
}
