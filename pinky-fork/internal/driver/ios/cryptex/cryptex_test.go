// The captured values, writeTestImage and the HTTP/2 and XPC framing of the
// fake cryptexd are copied from github.com/danielpaulus/go-ios
// v1.3.3-0.20261103131045-6910f3b585ad, ios/imagemounter/cryptex_test.go,
// under go-ios's MIT License (LICENSE in this folder). The fake's behavior
// is Pinky's: it acts as cryptexd on iOS 27.0.1 did on 2026-11-08.

package cryptex

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/http"
	"github.com/danielpaulus/go-ios/ios/xpc"
	"golang.org/x/net/http2"
	"howett.net/plist"
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// values captured from an iPhone while Xcode installed the developer disk image
var (
	capturedNonce = mustHex("0000e565e4633875d66ef7080de57f575dee872fae090e0ba65b602fed0d0155be9fc4c619c2c4f7de129495114bb312ee7f000030000000")
	capturedChip  = map[string]interface{}{
		"img4_chip_bord": uint64(4), "img4_chip_cepo": uint64(1), "img4_chip_chip": uint64(33088),
		"img4_chip_clas": uint64(242), "img4_chip_cpro": uint64(1), "img4_chip_csec": uint64(1),
		"img4_chip_ecid": uint64(7388792203870236), "img4_chip_epro": uint64(1), "img4_chip_esdm": uint64(0),
		"img4_chip_esec": uint64(1), "img4_chip_euou": uint64(0), "img4_chip_fchp": uint64(65296),
		"img4_chip_fpgt": uint64(0), "img4_chip_iuou": uint64(0), "img4_chip_omit": uint64(0),
		"img4_chip_rsch": uint64(0), "img4_chip_sdom": uint64(1), "img4_chip_styp": uint64(255),
		"img4_chip_type": uint64(3),
	}
)

type testImage struct {
	dir   string
	files map[string][]byte
}

// writeTestImage creates a developer disk image 'Restore' directory with a
// personalized DMG identity and a Cryptex1 identity like Xcode's DDI.
func writeTestImage(t *testing.T) testImage {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(path.Join(dir, "Firmware"), 0o755))
	info, err := plist.Marshal(map[string]interface{}{
		"CFBundleIdentifier": ddiCryptexIdentifier,
		"CFBundleVersion":    "27.2.52.0",
	}, plist.XMLFormat)
	must(err)
	files := map[string][]byte{
		// larger than the fake's flow-control windows, so the upload waits
		// for WINDOW_UPDATE frames as it does on a phone
		cryptexDmgKey:        bytes.Repeat([]byte("dmg-"), 20000),
		cryptexVolumeKey:     []byte("root hash"),
		cryptexInfoPlistKey:  info,
		cryptexTrustCacheKey: bytes.Repeat([]byte("tc"), 3000),
	}
	paths := map[string]string{
		cryptexDmgKey:        "c.dmg",
		cryptexVolumeKey:     "Firmware/c.dmg.root_hash",
		cryptexInfoPlistKey:  "Firmware/c.dmg.cryptex_info",
		cryptexTrustCacheKey: "Firmware/c.dmg.trustcache",
	}
	entries := map[string]interface{}{}
	for key, content := range files {
		must(os.WriteFile(path.Join(dir, paths[key]), content, 0o644))
		digest := sha512.Sum384(content)
		entries[key] = map[string]interface{}{
			"Digest": digest[:],
			"Info": map[string]interface{}{
				"Path":        paths[key],
				"Personalize": key != cryptexDmgKey,
			},
		}
	}
	manifest := map[string]interface{}{
		"ProductBuildVersion": "27B52",
		"BuildIdentities": []interface{}{
			map[string]interface{}{
				"ApBoardID": "0x08", "ApChipID": "0x8140",
				"Manifest": map[string]interface{}{"PersonalizedDMG": map[string]interface{}{"Digest": []byte{1}, "Info": map[string]interface{}{"Path": "p.dmg"}}},
			},
			map[string]interface{}{
				"Cryptex1,ChipID":                  "0xFF10",
				"Cryptex1,NonceDomain":             uint64(4),
				"Cryptex1,PreauthorizationVersion": "39.999.999.0.0,0",
				"Cryptex1,ProductClass":            "0xF2",
				"Cryptex1,SubType":                 uint64(2),
				"Cryptex1,Type":                    uint64(3),
				"Cryptex1,UseProductClass":         true,
				"Cryptex1,Version":                 "39.999.999.0.0,0",
				"Manifest":                         entries,
			},
		},
	}
	b, err := plist.Marshal(manifest, plist.XMLFormat)
	must(err)
	must(os.WriteFile(path.Join(dir, "BuildManifest.plist"), b, 0o644))
	return testImage{dir: dir, files: files}
}

// uploadKeys are the five payloads of an install.
var uploadKeys = []string{"image", "trustcache", "im4m", "info", "volumehash"}

// fakeCryptexd emulates cryptexd's remote service on the HTTP/2 + XPC level
// as iOS 27.0.1 behaves: it never opens a file transfer stream for the
// client. It reads each payload once the install request announced it, and
// answers the stream's FileOpen message only after the payload, with a
// FileOpenReply; it replies to the install once all payloads arrived. A
// client that waits for the FileOpenReply before it sends the payload waits
// forever (the phone dropped such a connection after 31 s).
type fakeCryptexd struct {
	t *testing.T
	// window is the flow-control window the fake announces and keeps topping
	// up, small enough that uploads have to wait for WINDOW_UPDATE frames.
	window uint32
	// resetFinished resets each file transfer stream after its payload, as
	// devices do.
	resetFinished bool
	// silent never replies to an install and closes the connection once
	// all payloads arrived, as a cryptexd that fails without a reply.
	silent bool
	// refuse replies to an install with this errno at once and resets the
	// file transfer streams, without reading them.
	refuse uint64

	mu        sync.Mutex
	requests  []map[string]interface{}
	uploads   map[string][]byte
	installed []RemoteCryptex
}

func (f *fakeCryptexd) connect() (*xpc.Connection, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		defer l.Close()
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// Only what the client sent wrong is reported: the connection ends
		// in many ways, some after the test did.
		var pe protocolError
		if err := f.serve(c); errors.As(err, &pe) {
			f.t.Errorf("fake cryptexd: %v", err)
		}
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		return nil, err
	}
	h, err := http.NewHttpConnection(c)
	if err != nil {
		return nil, err
	}
	return ios.CreateXpcConnection(h)
}

// protocolError is a client's mistake the fake noticed.
type protocolError struct{ error }

type fakeStream struct {
	buf        []byte
	transferId uint64
	opened     bool
	ended      bool
	answered   bool
}

func (f *fakeCryptexd) serve(c net.Conn) error {
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(c, preface); err != nil {
		return err
	}
	fr := http2.NewFramer(c, c)
	if err := fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: f.window}); err != nil {
		return err
	}

	streams := map[uint32]*fakeStream{}
	transfers := map[uint64]uint32{} // file transfer id -> stream
	var install map[string]interface{}
	var installId uint64

	reply := func(stream uint32, msg xpc.Message) error {
		buf := bytes.NewBuffer(nil)
		if err := xpc.EncodeMessage(buf, msg); err != nil {
			return err
		}
		return fr.WriteData(stream, false, buf.Bytes())
	}
	// announced reports whether the install request announced a transfer.
	announced := func(id uint64) bool {
		for _, key := range uploadKeys {
			if ft, ok := install[key].(xpc.FileTransfer); ok && ft.MsgId == id {
				return true
			}
		}
		return false
	}

	for {
		frame, err := fr.ReadFrame()
		if err != nil {
			return err
		}
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if !frame.IsAck() {
				if err := fr.WriteSettingsAck(); err != nil {
					return err
				}
			}
		case *http2.DataFrame:
			s, ok := streams[frame.StreamID]
			if !ok {
				s = &fakeStream{}
				streams[frame.StreamID] = s
			}
			s.buf = append(s.buf, frame.Data()...)
			s.ended = frame.StreamEnded()
			if n := uint32(len(frame.Data())); n > 0 {
				if err := fr.WriteWindowUpdate(0, n); err != nil {
					return err
				}
				if err := fr.WriteWindowUpdate(frame.StreamID, n); err != nil {
					return err
				}
			}
			if frame.StreamID > 3 {
				if !s.opened && len(s.buf) >= 24 {
					msg, err := xpc.DecodeMessage(bytes.NewReader(s.buf[:24]))
					if err != nil {
						return err
					}
					if msg.Flags != xpc.AlwaysSetFlag|xpc.FileOpenFlag {
						return protocolError{fmt.Errorf("stream %d opens with flags %#x, want AlwaysSet|FileOpen", frame.StreamID, msg.Flags)}
					}
					s.opened, s.transferId, s.buf = true, msg.Id, s.buf[24:]
					transfers[msg.Id] = frame.StreamID
					if f.refuse != 0 {
						if err := fr.WriteRSTStream(frame.StreamID, http2.ErrCodeStreamClosed); err != nil {
							return err
						}
					}
				}
				// the payload is answered after it arrived, not before
				if s.opened && s.ended && !s.answered && announced(s.transferId) {
					s.answered = true
					if err := reply(frame.StreamID, xpc.Message{Flags: xpc.AlwaysSetFlag | xpc.FileOpenReplyFlag, Id: s.transferId}); err != nil {
						return err
					}
					if f.resetFinished {
						if err := fr.WriteRSTStream(frame.StreamID, http2.ErrCodeStreamClosed); err != nil {
							return err
						}
					}
				}
				break
			}
			// XPC messages on the request (1) and reply (3) streams
			for len(s.buf) > 0 {
				r := bytes.NewReader(s.buf)
				msg, err := xpc.DecodeMessage(r)
				if err != nil {
					break // wait for the rest of the message
				}
				s.buf = s.buf[len(s.buf)-r.Len():]
				if msg.Flags&xpc.HeartbeatRequestFlag == 0 {
					// connection handshake, answered in kind
					if err := reply(frame.StreamID, xpc.Message{Flags: msg.Flags, Body: msg.Body}); err != nil {
						return err
					}
					continue
				}
				f.mu.Lock()
				f.requests = append(f.requests, msg.Body)
				f.mu.Unlock()
				argv, _ := msg.Body["argv"].(map[string]interface{})
				var resp map[string]interface{}
				switch msg.Body["routine"] {
				case "read-personalization-id":
					resp = map[string]interface{}{"argv": capturedChip}
				case "get-nonce":
					resp = map[string]interface{}{"argv": map[string]interface{}{"nonce": capturedNonce}}
				case "copy-installed":
					var list []interface{}
					f.mu.Lock()
					for _, c := range f.installed {
						list = append(list, map[string]interface{}{"remote-cryptex-identifier": c.Identifier, "remote-cryptex-version": c.Version})
					}
					f.mu.Unlock()
					resp = map[string]interface{}{"argv": map[string]interface{}{"remote-cryptex-array": list}}
				case "uninstall":
					f.mu.Lock()
					f.installed = nil
					f.mu.Unlock()
					resp = map[string]interface{}{"argv": map[string]interface{}{}}
				case "install":
					for _, key := range uploadKeys {
						if _, ok := argv[key].(xpc.FileTransfer); !ok {
							return protocolError{fmt.Errorf("install: %s is no file transfer", key)}
						}
					}
					if f.refuse != 0 {
						resp = map[string]interface{}{"error": f.refuse}
						break
					}
					// nothing is sent until the payloads arrived
					install, installId = argv, msg.Id
					continue
				default:
					resp = map[string]interface{}{"error": uint64(syscall.ENOTSUP)}
				}
				if err := reply(3, xpc.Message{Flags: xpc.AlwaysSetFlag | xpc.DataFlag | xpc.HeartbeatReplyFlag, Body: resp, Id: msg.Id}); err != nil {
					return err
				}
			}
		}

		if install != nil && f.uploadsComplete(install, streams, transfers) {
			f.mu.Lock()
			f.uploads = map[string][]byte{}
			for _, key := range uploadKeys {
				ft := install[key].(xpc.FileTransfer)
				data := streams[transfers[ft.MsgId]].buf
				if uint64(len(data)) != ft.TransferSize {
					f.t.Errorf("%s: %d bytes, announced %d", key, len(data), ft.TransferSize)
				}
				f.uploads[key] = data
			}
			f.mu.Unlock()
			if f.silent {
				return c.Close()
			}
			// like cryptexd, report the identifier and version of the cryptex_info
			var info cryptexInfo
			if _, err := plist.Unmarshal(f.uploads["info"], &info); err != nil {
				return err
			}
			f.mu.Lock()
			f.installed = append(f.installed, RemoteCryptex{Identifier: info.CFBundleIdentifier, Version: info.CFBundleVersion})
			f.mu.Unlock()
			resp := map[string]interface{}{"argv": map[string]interface{}{"remote-cryptex": map[string]interface{}{
				"remote-cryptex-identifier": info.CFBundleIdentifier,
				"remote-cryptex-version":    info.CFBundleVersion,
			}}}
			if err := reply(3, xpc.Message{Flags: xpc.AlwaysSetFlag | xpc.DataFlag | xpc.HeartbeatReplyFlag, Body: resp, Id: installId}); err != nil {
				return err
			}
			install = nil
		}
	}
}

func (f *fakeCryptexd) uploadsComplete(install map[string]interface{}, streams map[uint32]*fakeStream, transfers map[uint64]uint32) bool {
	for _, key := range uploadKeys {
		ft := install[key].(xpc.FileTransfer)
		sid, ok := transfers[ft.MsgId]
		if !ok {
			return false
		}
		if s := streams[sid]; s == nil || !s.ended {
			return false
		}
	}
	return true
}

func (f *fakeCryptexd) mounter() *CryptexDeveloperDiskImageMounter {
	return &CryptexDeveloperDiskImageMounter{
		connect: f.connect,
		sign: func(map[string]interface{}) ([]byte, error) {
			return bytes.Repeat([]byte("ticket"), 500), nil
		},
	}
}

// within runs fn and reports its error, or that it did not end within d.
func within(d time.Duration, fn func() error) (err error, ended bool) {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	select {
	case err := <-ch:
		return err, true
	case <-time.After(d):
		return nil, false
	}
}

// TestMountImageSendsPayloadsAtOnce installs the image on a cryptexd that,
// as on iOS 27.0.1, answers each payload only after it arrived.
func TestMountImageSendsPayloadsAtOnce(t *testing.T) {
	img := writeTestImage(t)
	fake := &fakeCryptexd{t: t, window: 1000, resetFinished: true}
	m := fake.mounter()
	err, ended := within(10*time.Second, func() error { return m.MountImage(img.dir) })
	if !ended {
		t.Fatal("MountImage did not end within 10 s")
	}
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	for key, want := range map[string][]byte{"image": img.files[cryptexDmgKey], "trustcache": img.files[cryptexTrustCacheKey],
		"volumehash": img.files[cryptexVolumeKey], "info": img.files[cryptexInfoPlistKey], "im4m": bytes.Repeat([]byte("ticket"), 500)} {
		if !bytes.Equal(fake.uploads[key], want) {
			t.Errorf("%s: %d bytes uploaded, want %d", key, len(fake.uploads[key]), len(want))
		}
	}
	var routines []interface{}
	for _, r := range fake.requests {
		routines = append(routines, r["routine"])
	}
	fake.mu.Unlock()
	if got := fmt.Sprint(routines); got != "[read-personalization-id get-nonce install]" {
		t.Errorf("routines %s", got)
	}

	mounted, err := m.IsImageMounted(img.dir)
	if err != nil || !mounted {
		t.Fatalf("IsImageMounted = %v, %v", mounted, err)
	}
	if err := m.UnmountImage(); err != nil {
		t.Fatal(err)
	}
	if images, err := m.ListImages(); err != nil || len(images) != 0 {
		t.Fatalf("after UnmountImage: %q, %v", images, err)
	}
}

// TestUpstreamInstallWaitsForever shows the bug: go-ios's installCryptex,
// as of the version this package was copied from, waits on each stream for
// a FileOpenReply before it sends the payload, which cryptexd on iOS 27.0.1
// sends only after the payload.
func TestUpstreamInstallWaitsForever(t *testing.T) {
	img := writeTestImage(t)
	fake := &fakeCryptexd{t: t, window: 1000}
	manifest, err := loadBuildManifest(path.Join(img.dir, "BuildManifest.plist"))
	if err != nil {
		t.Fatal(err)
	}
	chip, err := parseCryptexChip(capturedChip)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := manifest.findCryptexIdentity(chip)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := fake.connect()
	if err != nil {
		t.Fatal(err)
	}
	err, ended := within(2*time.Second, func() error {
		_, err := upstreamInstallCryptex(conn, img.dir, identity, []byte("ticket"))
		return err
	})
	if ended {
		t.Fatalf("go-ios's install ended (%v); it should wait for a FileOpenReply that never comes", err)
	}
	conn.Close() // what Pinky's timeout and lock watch do, through the tunnel
}

// TestMountImageEndsWithTheConnection: a cryptexd that never replies to an
// install keeps MountImage waiting only until the connection goes, which is
// how Pinky's timeout and lock watch end a mount (they close the tunnel).
func TestMountImageEndsWithTheConnection(t *testing.T) {
	img := writeTestImage(t)
	fake := &fakeCryptexd{t: t, window: 1000, silent: true}
	err, ended := within(10*time.Second, func() error { return fake.mounter().MountImage(img.dir) })
	if !ended {
		t.Fatal("MountImage still waits after the connection closed")
	}
	if err == nil || !strings.Contains(err.Error(), "install reply") {
		t.Fatalf("MountImage = %v, want an error reading the install reply", err)
	}
}

// TestMountImageRefused says why a cryptexd that refuses an install did,
// rather than only that it reset the payload's stream.
func TestMountImageRefused(t *testing.T) {
	img := writeTestImage(t)
	fake := &fakeCryptexd{t: t, window: 1000, refuse: uint64(syscall.EPERM)}
	err, ended := within(10*time.Second, func() error { return fake.mounter().MountImage(img.dir) })
	if !ended {
		t.Fatal("MountImage did not end within 10 s")
	}
	if err == nil || !strings.Contains(err.Error(), syscall.EPERM.Error()) {
		t.Fatalf("MountImage = %v, want the device's reason (%v)", err, syscall.EPERM)
	}
}
