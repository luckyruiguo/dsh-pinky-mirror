// upstreamInstallCryptex is installCryptex as go-ios has it in the version
// this package was copied from (github.com/danielpaulus/go-ios
// v1.3.3-0.20261103131045-6910f3b585ad, ios/imagemounter/cryptex.go, MIT
// License, LICENSE in this folder), kept to show the bug it had:
// TestUpstreamInstallWaitsForever.

package cryptex

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/danielpaulus/go-ios/ios/xpc"
)

func upstreamInstallCryptex(conn *xpc.Connection, imagePath string, identity buildIdentity, im4m []byte) (RemoteCryptex, error) {
	fromManifest := func(key string) func() (io.ReadCloser, uint64, error) {
		return func() (io.ReadCloser, uint64, error) {
			entry, ok := identity.Manifest[key]
			if !ok {
				return nil, 0, fmt.Errorf("the Cryptex1 identity has no %s entry", key)
			}
			p := path.Join(imagePath, entry.Info.Path)
			size, err := getFileSize(p)
			if err != nil {
				return nil, 0, err
			}
			f, err := os.Open(p)
			return f, size, err
		}
	}
	uploads := []cryptexUpload{
		{"image", fromManifest(cryptexDmgKey)},
		{"volumehash", fromManifest(cryptexVolumeKey)},
		{"info", fromManifest(cryptexInfoPlistKey)},
		{"trustcache", fromManifest(cryptexTrustCacheKey)},
		{"im4m", func() (io.ReadCloser, uint64, error) {
			return io.NopCloser(bytes.NewReader(im4m)), uint64(len(im4m)), nil
		}},
	}

	subType, _ := toUint64(identity.Cryptex1SubType)
	nonceDomain, _ := toUint64(identity.Cryptex1NonceDomain)
	argv := map[string]interface{}{
		"auth":              uint64(0),
		"client-version":    cryptexClientVersion,
		"image-type-index":  cryptexImageTypeIndex,
		"persistence":       cryptexPersistence,
		"nonce-persistence": cryptexNoncePersistence,
		"cryptex1-properties": map[string]interface{}{
			"Cryptex1,NonceDomain":     nonceDomain,
			"Cryptex1,PreauthVersion":  identity.Cryptex1PreauthorizationVersion,
			"Cryptex1,SubType":         subType,
			"Cryptex1,UseProductClass": identity.Cryptex1UseProductClass,
			"Cryptex1,Version":         identity.Cryptex1Version,
			"MountedCryptex":           false,
		},
	}

	// Each file goes on a stream of its own, announced with the message id that
	// the FileTransfer object in the request references. The install request
	// itself uses id 1.
	sources := make([]io.ReadCloser, len(uploads))
	defer func() {
		for _, s := range sources {
			if s != nil {
				s.Close()
			}
		}
	}()
	streams := make([]*xpc.FileTransferStream, len(uploads))
	for i, u := range uploads {
		src, size, err := u.open()
		if err != nil {
			return RemoteCryptex{}, fmt.Errorf("installCryptex: %s: %w", u.arg, err)
		}
		sources[i] = src
		id := uint64(2 + i)
		streams[i], err = conn.OpenFileTransfer(id)
		if err != nil {
			return RemoteCryptex{}, fmt.Errorf("installCryptex: %s: %w", u.arg, err)
		}
		argv[u.arg] = xpc.FileTransfer{MsgId: id, TransferSize: size}
	}

	err := conn.Send(map[string]interface{}{"routine": "install", "argv": argv}, xpc.HeartbeatRequestFlag)
	if err != nil {
		return RemoteCryptex{}, fmt.Errorf("installCryptex: failed to send install request: %w", err)
	}
	for i, u := range uploads {
		if err := streams[i].WaitAccepted(); err != nil {
			return RemoteCryptex{}, fmt.Errorf("installCryptex: device did not accept %s: %w", u.arg, err)
		}
		if _, err := io.Copy(streams[i], sources[i]); err != nil {
			return RemoteCryptex{}, fmt.Errorf("installCryptex: failed to send %s: %w", u.arg, err)
		}
		if err := streams[i].Close(); err != nil {
			return RemoteCryptex{}, fmt.Errorf("installCryptex: failed to finish %s: %w", u.arg, err)
		}
	}

	reply, err := conn.ReceiveOnServerClientStream()
	if err != nil {
		return RemoteCryptex{}, fmt.Errorf("installCryptex: failed to read install reply: %w", err)
	}
	result, err := parseCryptexReply("install", reply)
	if err != nil {
		return RemoteCryptex{}, fmt.Errorf("installCryptex: %w", err)
	}
	installed, _ := result["remote-cryptex"].(map[string]interface{})
	return parseRemoteCryptex(installed), nil
}
