package android

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// knownHostsPath is where the adb server keeps the guids of the phones it
// paired with over Wi-Fi: adb_known_hosts.pb in ~/.android, as adb finds
// it (from HOME, not from ANDROID_USER_HOME).
func knownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".android", "adb_known_hosts.pb"), nil
}

// readKnownHosts returns the guids in the adb server's known hosts, none
// when the computer never paired a phone over Wi-Fi.
func readKnownHosts() ([]string, error) {
	path, err := knownHostsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseKnownHosts(b)
}

// parseKnownHosts reads adb's AdbKnownHosts protocol buffer
// (packages/modules/adb/proto/adb_known_hosts.proto):
//
//	message HostInfo { string guid = 1; }
//	message AdbKnownHosts { repeated HostInfo host_infos = 1; }
//
// It returns each guid once, in file order, and skips fields it does not
// know.
func parseKnownHosts(b []byte) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	err := eachField(b, func(num int, data []byte) error {
		if num != 1 {
			return nil
		}
		return eachField(data, func(num int, guid []byte) error {
			if num == 1 && len(guid) > 0 && !seen[string(guid)] {
				seen[string(guid)] = true
				out = append(out, string(guid))
			}
			return nil
		})
	})
	return out, err
}

// eachField calls fn with the number and contents of each length-delimited
// field of a protocol buffer message, skipping the other wire types.
func eachField(b []byte, fn func(num int, data []byte) error) error {
	for len(b) > 0 {
		key, n := uvarint(b)
		if n <= 0 {
			return errors.New("android: adb_known_hosts.pb: bad field key")
		}
		b = b[n:]
		num, wire := int(key>>3), key&7
		switch wire {
		case 0: // varint
			_, n := uvarint(b)
			if n <= 0 {
				return errors.New("android: adb_known_hosts.pb: bad varint")
			}
			b = b[n:]
		case 1: // 64-bit
			if len(b) < 8 {
				return errors.New("android: adb_known_hosts.pb: short field")
			}
			b = b[8:]
		case 5: // 32-bit
			if len(b) < 4 {
				return errors.New("android: adb_known_hosts.pb: short field")
			}
			b = b[4:]
		case 2: // length-delimited
			size, n := uvarint(b)
			if n <= 0 || size > uint64(len(b)-n) {
				return errors.New("android: adb_known_hosts.pb: bad length")
			}
			data := b[n : n+int(size)]
			b = b[n+int(size):]
			if err := fn(num, data); err != nil {
				return err
			}
		default:
			return fmt.Errorf("android: adb_known_hosts.pb: wire type %d", wire)
		}
	}
	return nil
}

// uvarint decodes a protocol buffer varint and returns it and its length,
// or a length of 0 or less when it is cut short or too long.
func uvarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}
