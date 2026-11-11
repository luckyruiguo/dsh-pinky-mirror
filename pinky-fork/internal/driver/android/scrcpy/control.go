package scrcpy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

// Control message types (ControlMessage.java).
const (
	TypeInjectKeycode           = 0
	TypeInjectText              = 1
	TypeInjectTouchEvent        = 2
	TypeInjectScrollEvent       = 3
	TypeBackOrScreenOn          = 4
	TypeExpandNotificationPanel = 5
	TypeExpandSettingsPanel     = 6
	TypeCollapsePanels          = 7
	TypeGetClipboard            = 8
	TypeSetClipboard            = 9
	TypeSetDisplayPower         = 10
	TypeRotateDevice            = 11
	TypeStartApp                = 16
	TypeResetVideo              = 17
)

// Limits the server enforces.
const (
	// MaxInjectText is the longest text, in UTF-8 bytes, one inject-text
	// message may carry.
	MaxInjectText = 300
	// MaxClipboardText is the longest clipboard text, in UTF-8 bytes.
	MaxClipboardText = 1<<18 - 14
	// MaxStartAppName is the longest app name or package start-app accepts.
	MaxStartAppName = 255
)

// Android KeyEvent and MotionEvent actions.
const (
	ActionDown = 0
	ActionUp   = 1
	ActionMove = 2
)

// Pointer ids with a special meaning to the server.
const (
	PointerMouse         int64 = -1
	PointerGenericFinger int64 = -2
	PointerVirtualFinger int64 = -3
)

// Copy keys for GetClipboard.
const (
	CopyKeyNone = 0
	CopyKeyCopy = 1
	CopyKeyCut  = 2
)

// Position is a point in the coordinate space of a frame of the given size.
// The server ignores positional events whose frame size is not the current
// video size.
type Position struct {
	X, Y          int32
	Width, Height uint16
}

func putPosition(b []byte, p Position) {
	binary.BigEndian.PutUint32(b[0:], uint32(p.X))
	binary.BigEndian.PutUint32(b[4:], uint32(p.Y))
	binary.BigEndian.PutUint16(b[8:], p.Width)
	binary.BigEndian.PutUint16(b[10:], p.Height)
}

// InjectKeycode encodes a key event. Meta is a KeyEvent meta state.
func InjectKeycode(action uint8, keycode, repeat, meta int32) []byte {
	b := make([]byte, 14)
	b[0] = TypeInjectKeycode
	b[1] = action
	binary.BigEndian.PutUint32(b[2:], uint32(keycode))
	binary.BigEndian.PutUint32(b[6:], uint32(repeat))
	binary.BigEndian.PutUint32(b[10:], uint32(meta))
	return b
}

// InjectText encodes text the server types with the virtual keyboard's key
// map. It must be at most MaxInjectText bytes; see SplitText.
func InjectText(text string) ([]byte, error) {
	if len(text) > MaxInjectText {
		return nil, fmt.Errorf("scrcpy: inject text too long (%d bytes)", len(text))
	}
	b := make([]byte, 5+len(text))
	b[0] = TypeInjectText
	binary.BigEndian.PutUint32(b[1:], uint32(len(text)))
	copy(b[5:], text)
	return b, nil
}

// SplitText cuts text into pieces of at most max bytes, on rune boundaries.
func SplitText(text string, max int) []string {
	var out []string
	for len(text) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			cut = max
		}
		out = append(out, text[:cut])
		text = text[cut:]
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

// InjectTouch encodes a touch event. Pressure is 0 to 1.
func InjectTouch(action uint8, pointer int64, pos Position, pressure float64, actionButton, buttons int32) []byte {
	b := make([]byte, 32)
	b[0] = TypeInjectTouchEvent
	b[1] = action
	binary.BigEndian.PutUint64(b[2:], uint64(pointer))
	putPosition(b[10:], pos)
	binary.BigEndian.PutUint16(b[22:], u16FixedPoint(pressure))
	binary.BigEndian.PutUint32(b[24:], uint32(actionButton))
	binary.BigEndian.PutUint32(b[28:], uint32(buttons))
	return b
}

// InjectScroll encodes a scroll event; hscroll and vscroll are -16 to 16.
func InjectScroll(pos Position, hscroll, vscroll float64, buttons int32) []byte {
	b := make([]byte, 21)
	b[0] = TypeInjectScrollEvent
	putPosition(b[1:], pos)
	binary.BigEndian.PutUint16(b[13:], uint16(i16FixedPoint(hscroll/16)))
	binary.BigEndian.PutUint16(b[15:], uint16(i16FixedPoint(vscroll/16)))
	binary.BigEndian.PutUint32(b[17:], uint32(buttons))
	return b
}

// BackOrScreenOn encodes BACK, or POWER when the screen is off.
func BackOrScreenOn(action uint8) []byte { return []byte{TypeBackOrScreenOn, action} }

// GetClipboard asks for the clipboard text, after pressing COPY or CUT if
// copyKey says so. The answer is a DeviceClipboard message.
func GetClipboard(copyKey uint8) []byte { return []byte{TypeGetClipboard, copyKey} }

// SetClipboard sets the clipboard and, with paste, presses PASTE. A
// non-zero sequence asks for a DeviceAckClipboard with that sequence.
func SetClipboard(sequence uint64, text string, paste bool) ([]byte, error) {
	if len(text) > MaxClipboardText {
		return nil, fmt.Errorf("scrcpy: clipboard text too long (%d bytes)", len(text))
	}
	b := make([]byte, 14+len(text))
	b[0] = TypeSetClipboard
	binary.BigEndian.PutUint64(b[1:], sequence)
	if paste {
		b[9] = 1
	}
	binary.BigEndian.PutUint32(b[10:], uint32(len(text)))
	copy(b[14:], text)
	return b, nil
}

// SetDisplayPower turns the main display on or off without locking.
func SetDisplayPower(on bool) []byte {
	if on {
		return []byte{TypeSetDisplayPower, 1}
	}
	return []byte{TypeSetDisplayPower, 0}
}

// StartApp starts an app on the session's display. Name is a package, or
// "?" and a display name; a leading "+" force-stops the app first.
func StartApp(name string) ([]byte, error) {
	if len(name) > MaxStartAppName {
		return nil, fmt.Errorf("scrcpy: app name too long (%d bytes)", len(name))
	}
	b := make([]byte, 2+len(name))
	b[0] = TypeStartApp
	b[1] = byte(len(name))
	copy(b[2:], name)
	return b, nil
}

// ResetVideo restarts the encoder, so the stream sends a new session
// header, codec configuration and key frame.
func ResetVideo() []byte { return []byte{TypeResetVideo} }

func u16FixedPoint(f float64) uint16 {
	if f <= 0 || math.IsNaN(f) {
		return 0
	}
	if f >= 1 {
		return 0xffff
	}
	return uint16(f * 0x1p16)
}

func i16FixedPoint(f float64) int16 {
	if math.IsNaN(f) {
		return 0
	}
	if f >= 1 {
		return 0x7fff
	}
	if f <= -1 {
		return -0x8000
	}
	return int16(f * 0x1p15)
}

// Device message types (DeviceMessage.java).
const (
	DeviceClipboard    = 0
	DeviceAckClipboard = 1
	DeviceUhidOutput   = 2
)

// DeviceMessage is a message from the server on the control socket.
type DeviceMessage struct {
	Type     uint8
	Text     string // DeviceClipboard
	Sequence uint64 // DeviceAckClipboard
	ID       uint16 // DeviceUhidOutput
	Data     []byte // DeviceUhidOutput
}

// maxDeviceMessage bounds what ReadDeviceMessage accepts.
const maxDeviceMessage = 1 << 18

// ReadDeviceMessage reads one device message.
func ReadDeviceMessage(r io.Reader) (DeviceMessage, error) {
	var t [1]byte
	if _, err := io.ReadFull(r, t[:]); err != nil {
		return DeviceMessage{}, err
	}
	m := DeviceMessage{Type: t[0]}
	switch m.Type {
	case DeviceClipboard:
		var n [4]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return m, unexpected(err)
		}
		size := binary.BigEndian.Uint32(n[:])
		if size > maxDeviceMessage {
			return m, fmt.Errorf("scrcpy: clipboard message too large (%d)", size)
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(r, buf); err != nil {
			return m, unexpected(err)
		}
		m.Text = string(buf)
	case DeviceAckClipboard:
		var s [8]byte
		if _, err := io.ReadFull(r, s[:]); err != nil {
			return m, unexpected(err)
		}
		m.Sequence = binary.BigEndian.Uint64(s[:])
	case DeviceUhidOutput:
		var h [4]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return m, unexpected(err)
		}
		m.ID = binary.BigEndian.Uint16(h[:2])
		m.Data = make([]byte, binary.BigEndian.Uint16(h[2:]))
		if _, err := io.ReadFull(r, m.Data); err != nil {
			return m, unexpected(err)
		}
	default:
		return m, fmt.Errorf("scrcpy: unknown device message type %d", m.Type)
	}
	return m, nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
