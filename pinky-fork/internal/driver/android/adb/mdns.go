package adb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Wireless debugging (Android 11 and later): the phone advertises its
// pairing and connect services with mDNS, the adb server discovers them,
// pairs with a code or a QR password, and connects over TLS.

// mDNS service types the adb server reports.
const (
	ServicePairing = "_adb-tls-pairing._tcp" // a phone showing a pairing code or scanning a QR code
	ServiceConnect = "_adb-tls-connect._tcp" // a paired phone with wireless debugging on
	ServiceLegacy  = "_adb._tcp"             // `adb tcpip`
)

// MDNSService is a service the adb server found on the network.
type MDNSService struct {
	Instance string // the pairing name, or "adb-<serial>-<suffix>" for a phone
	Type     string // ServicePairing, ServiceConnect or ServiceLegacy
	Addr     string // "192.168.1.42:37123"
}

// MDNSCheck reports whether the server can discover services with mDNS,
// and which implementation it uses. A server without discovery fails with
// an *Error.
func (c *Client) MDNSCheck(ctx context.Context) (string, error) {
	s, err := c.query(ctx, "host:mdns:check")
	if err != nil {
		return "", err
	}
	// A server started with ADB_MDNS=0 answers OKAY and
	// "ERROR: mdns discovery disabled".
	if strings.HasPrefix(s, "ERROR") {
		return "", &Error{Msg: strings.TrimSpace(strings.TrimPrefix(s, "ERROR:"))}
	}
	return s, nil
}

// MDNSServices lists the services the server has discovered.
func (c *Client) MDNSServices(ctx context.Context) ([]MDNSService, error) {
	s, err := c.query(ctx, "host:mdns:services")
	if err != nil {
		return nil, err
	}
	return ParseMDNSServices(s), nil
}

// ParseMDNSServices parses the server's list: one service a line, as
// "instance<TAB>type<TAB>address:port". Types may end with a dot.
func ParseMDNSServices(s string) []MDNSService {
	var out []MDNSService
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		svc := MDNSService{
			Instance: strings.TrimSpace(f[0]),
			Type:     strings.TrimSuffix(strings.TrimSpace(f[1]), "."),
			Addr:     strings.TrimSpace(f[2]),
		}
		if svc.Instance != "" && svc.Type != "" && svc.Addr != "" {
			out = append(out, svc)
		}
	}
	return out
}

var pairedRE = regexp.MustCompile(`^Successfully paired to (\S+)(?: \[guid=([^\]]+)\])?`)

// ErrPairing is wrapped by Pair when the phone refused the code or the
// connection dropped.
var ErrPairing = errors.New("adb: pairing failed")

// Pair pairs with the phone's pairing service at addr ("host:port"), with
// the six-digit code it shows or the password of a QR code. It returns the
// phone's guid, the instance name of its connect service
// ("adb-<serial>-<suffix>"), when the server reports one.
func (c *Client) Pair(ctx context.Context, addr, password string) (string, error) {
	if strings.ContainsAny(password, ":\n") || password == "" {
		return "", fmt.Errorf("adb: pair: bad password")
	}
	resp, err := c.pairQuery(ctx, "host:pair:"+password+":"+addr)
	if err != nil {
		var ae *Error
		if errors.As(err, &ae) {
			return "", fmt.Errorf("%w: %s", ErrPairing, strings.TrimPrefix(ae.Msg, "Failed: "))
		}
		return "", err
	}
	resp = strings.TrimSpace(resp)
	m := pairedRE.FindStringSubmatch(resp)
	if m == nil {
		return "", fmt.Errorf("%w: %s", ErrPairing, strings.TrimPrefix(resp, "Failed: "))
	}
	return m[2], nil
}

// pairQuery runs host:pair. The server answers OKAY when it takes the
// request, then OKAY and the result, or FAIL and the reason, once the
// pairing is over; a server that answers with one status and the result
// is read as well.
func (c *Client) pairQuery(ctx context.Context, service string) (string, error) {
	conn, stop, err := c.hostConn(ctx, service)
	if err != nil {
		return "", err
	}
	defer stop()
	defer conn.Close()
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return "", ctxErr(ctx, fmt.Errorf("adb: pair: %w", err))
	}
	switch string(hdr[:]) {
	case "OKAY":
		s, err := readString(conn)
		return s, ctxErr(ctx, err)
	case "FAIL":
		msg, err := readString(conn)
		if err != nil {
			return "", ctxErr(ctx, err)
		}
		return "", &Error{Msg: msg}
	}
	n, err := strconv.ParseUint(string(hdr[:]), 16, 32)
	if err != nil {
		return "", fmt.Errorf("adb: pair: unexpected reply %q", hdr[:])
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", ctxErr(ctx, err)
	}
	return string(buf), nil
}

// Connect connects the server to a phone's adb over the network, as
// `adb connect` does. A phone already connected is not an error.
func (c *Client) Connect(ctx context.Context, addr string) error {
	resp, err := c.query(ctx, "host:connect:"+addr)
	if err != nil {
		return err
	}
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "connected to") || strings.HasPrefix(resp, "already connected to") {
		return nil
	}
	return fmt.Errorf("adb: connect %s: %s", addr, resp)
}

// Disconnect drops the server's connection to a phone over the network,
// as `adb disconnect <serial>` does. The server reads serial as an
// address, so a connection made by address ("192.168.1.42:41000") can be
// dropped, and one its own discovery made, which it names after the
// phone's mDNS service ("adb-<serial>-<suffix>._adb-tls-connect._tcp"),
// cannot: platform-tools 37 looks for "<name>:5555" and answers no such
// device. A serial with no connection returns an error wrapping
// ErrDeviceNotFound. An empty serial is refused, since to the server it
// means every connection.
func (c *Client) Disconnect(ctx context.Context, serial string) error {
	if strings.TrimSpace(serial) == "" {
		return errors.New("adb: disconnect: no serial")
	}
	resp, err := c.query(ctx, "host:disconnect:"+serial)
	var ae *Error
	if errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Msg), "no such device") {
		return fmt.Errorf("adb: disconnect %s: %s: %w", serial, ae.Msg, ErrDeviceNotFound)
	}
	if err != nil {
		return err
	}
	if resp = strings.TrimSpace(resp); !strings.HasPrefix(resp, "disconnected") {
		return fmt.Errorf("adb: disconnect %s: %s", serial, resp)
	}
	return nil
}
