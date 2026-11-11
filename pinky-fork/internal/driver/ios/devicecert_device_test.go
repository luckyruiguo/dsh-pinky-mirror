//go:build device

package ios

import (
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"testing"

	goios "github.com/danielpaulus/go-ios/ios"
)

// TestDeviceCertificate reads, and only reads, which certificate the phone presents in
// lockdown's TLS, over the cable and over Wi-Fi when usbmuxd lists it there: on a lockdown
// session (nothing is asked of it) and on the CoreDeviceProxy service that Pinky's tunnel goes
// through, opened and closed without starting a tunnel. Each must carry the public key of the
// pair record's DeviceCertificate, which go-ios checks once the peer's certificate is pinned:
// otherwise pinning would refuse the phone itself.
func TestDeviceCertificate(t *testing.T) {
	udid := os.Getenv("PINKY_DEVICE")
	if udid == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	pr, err := goios.ReadPairRecord(udid)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(pr.DeviceCertificate)
	if block == nil {
		t.Fatalf("the pair record's DeviceCertificate is not PEM (%d bytes)", len(pr.DeviceCertificate))
	}
	paired, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pair record: DeviceCertificate %s", certSummary(paired))

	for _, wifi := range []bool{false, true} {
		e, err := findEntry(udid, wifi)
		if err != nil {
			t.Logf("wifi=%v: no entry: %v", wifi, err)
			continue
		}
		t.Run(fmt.Sprintf("wifi=%v", wifi), func(t *testing.T) {
			lockdown, err := goios.ConnectLockdownWithSession(e)
			if err != nil {
				t.Fatalf("lockdown session: %v", err)
			}
			checkPeer(t, "lockdown session", lockdown.Conn(), paired)
			lockdown.Close()

			proxy, err := goios.ConnectToService(e, coreDeviceProxy)
			if err != nil {
				t.Fatalf("%s: %v", coreDeviceProxy, err)
			}
			checkPeer(t, coreDeviceProxy, proxy.Conn(), paired)
			proxy.Close()
		})
	}
}

func checkPeer(t *testing.T, what string, c net.Conn, paired *x509.Certificate) {
	t.Helper()
	tc, ok := c.(*tls.Conn)
	if !ok {
		t.Fatalf("%s: not TLS (%T)", what, c)
	}
	st := tc.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		t.Fatalf("%s: no peer certificate", what)
	}
	leaf := st.PeerCertificates[0]
	sameKey := paired.PublicKey.(interface{ Equal(crypto.PublicKey) bool }).Equal(leaf.PublicKey)
	t.Logf("%s: TLS 0x%04x, %d certificates, leaf %s, same key as DeviceCertificate: %v, same bytes: %v",
		what, st.Version, len(st.PeerCertificates), certSummary(leaf), sameKey, string(leaf.Raw) == string(paired.Raw))
	if !sameKey {
		t.Errorf("%s: the phone's certificate does not carry the DeviceCertificate's key", what)
	}
}

// certSummary names a certificate by its key type and a short fingerprint of its key, so the
// log holds nothing that identifies the phone beyond this test.
func certSummary(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return fmt.Sprintf("%s key %x…", c.PublicKeyAlgorithm, sum[:4])
}
