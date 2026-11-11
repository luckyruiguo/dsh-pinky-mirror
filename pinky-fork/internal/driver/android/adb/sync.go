package adb

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// syncMaxChunk is the largest DATA payload the sync protocol allows.
const syncMaxChunk = 64 * 1024

// FileInfo is what the sync STAT request returns.
type FileInfo struct {
	Mode    os.FileMode // Unix mode bits as the device reports them
	Size    int64
	ModTime time.Time
}

// Exists reports whether the stat found a file (adb reports zeros for a
// missing path).
func (fi FileInfo) Exists() bool { return fi.Mode != 0 || fi.Size != 0 || !fi.ModTime.IsZero() }

type syncConn struct {
	conn net.Conn
	r    *bufio.Reader
}

func (c *Client) openSync(ctx context.Context, serial string) (*syncConn, func() bool, error) {
	conn, err := c.Dial(ctx, serial, "sync:")
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	return &syncConn{conn: conn, r: bufio.NewReaderSize(conn, syncMaxChunk+8)}, stop, nil
}

func (s *syncConn) close() {
	// QUIT is a courtesy; the server also handles a plain close.
	_ = s.send("QUIT", nil)
	s.conn.Close()
}

func (s *syncConn) send(id string, data []byte) error {
	hdr := make([]byte, 8, 8+len(data))
	copy(hdr, id)
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(data)))
	_, err := s.conn.Write(append(hdr, data...))
	return err
}

func (s *syncConn) readHeader() (string, uint32, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(s.r, hdr[:]); err != nil {
		return "", 0, err
	}
	return string(hdr[:4]), binary.LittleEndian.Uint32(hdr[4:]), nil
}

func (s *syncConn) readFail(n uint32) error {
	if n > 64*1024 {
		return fmt.Errorf("adb: sync: failure message too long (%d)", n)
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(s.r, msg); err != nil {
		return err
	}
	return &Error{Msg: string(msg)}
}

// Push writes the content of r to remote on the device with the given
// permission bits (for example 0644) and modification time.
func (c *Client) Push(ctx context.Context, serial string, r io.Reader, remote string, perm os.FileMode, mtime time.Time) error {
	s, stop, err := c.openSync(ctx, serial)
	if err != nil {
		return err
	}
	defer stop()
	defer s.close()
	// The mode is sent in decimal and includes the regular-file type bit.
	spec := fmt.Sprintf("%s,%d", remote, 0o100000|uint32(perm.Perm()))
	if err := s.send("SEND", []byte(spec)); err != nil {
		return ctxErr(ctx, fmt.Errorf("adb: push %s: %w", remote, err))
	}
	buf := make([]byte, syncMaxChunk)
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			if err := s.send("DATA", buf[:n]); err != nil {
				return ctxErr(ctx, fmt.Errorf("adb: push %s: %w", remote, err))
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("adb: push %s: read source: %w", remote, rerr)
		}
	}
	var done [8]byte
	copy(done[:], "DONE")
	binary.LittleEndian.PutUint32(done[4:], uint32(mtime.Unix()))
	if _, err := s.conn.Write(done[:]); err != nil {
		return ctxErr(ctx, fmt.Errorf("adb: push %s: %w", remote, err))
	}
	id, n, err := s.readHeader()
	if err != nil {
		return ctxErr(ctx, fmt.Errorf("adb: push %s: %w", remote, err))
	}
	switch id {
	case "OKAY":
		return nil
	case "FAIL":
		return fmt.Errorf("push %s: %w", remote, s.readFail(n))
	default:
		return fmt.Errorf("adb: push %s: unexpected reply %q", remote, id)
	}
}

// Pull copies remote from the device into w and returns the bytes copied.
func (c *Client) Pull(ctx context.Context, serial, remote string, w io.Writer) (int64, error) {
	s, stop, err := c.openSync(ctx, serial)
	if err != nil {
		return 0, err
	}
	defer stop()
	defer s.close()
	if err := s.send("RECV", []byte(remote)); err != nil {
		return 0, ctxErr(ctx, fmt.Errorf("adb: pull %s: %w", remote, err))
	}
	var total int64
	for {
		id, n, err := s.readHeader()
		if err != nil {
			return total, ctxErr(ctx, fmt.Errorf("adb: pull %s: %w", remote, err))
		}
		switch id {
		case "DATA":
			if n > syncMaxChunk {
				return total, fmt.Errorf("adb: pull %s: chunk too large (%d)", remote, n)
			}
			m, err := io.CopyN(w, s.r, int64(n))
			total += m
			if err != nil {
				return total, ctxErr(ctx, fmt.Errorf("adb: pull %s: %w", remote, err))
			}
		case "DONE":
			return total, nil
		case "FAIL":
			return total, fmt.Errorf("pull %s: %w", remote, s.readFail(n))
		default:
			return total, fmt.Errorf("adb: pull %s: unexpected reply %q", remote, id)
		}
	}
}

// Stat returns the mode, size and modification time of remote. A missing
// path returns a FileInfo whose Exists is false.
func (c *Client) Stat(ctx context.Context, serial, remote string) (FileInfo, error) {
	s, stop, err := c.openSync(ctx, serial)
	if err != nil {
		return FileInfo{}, err
	}
	defer stop()
	defer s.close()
	if err := s.send("STAT", []byte(remote)); err != nil {
		return FileInfo{}, ctxErr(ctx, fmt.Errorf("adb: stat %s: %w", remote, err))
	}
	var buf [16]byte
	if _, err := io.ReadFull(s.r, buf[:]); err != nil {
		return FileInfo{}, ctxErr(ctx, fmt.Errorf("adb: stat %s: %w", remote, err))
	}
	if string(buf[:4]) != "STAT" {
		return FileInfo{}, fmt.Errorf("adb: stat %s: unexpected reply %q", remote, buf[:4])
	}
	mode := binary.LittleEndian.Uint32(buf[4:])
	size := binary.LittleEndian.Uint32(buf[8:])
	mtime := binary.LittleEndian.Uint32(buf[12:])
	fi := FileInfo{Mode: unixMode(mode), Size: int64(size)}
	if mtime != 0 {
		fi.ModTime = time.Unix(int64(mtime), 0)
	}
	return fi, nil
}

// unixMode converts st_mode bits to an os.FileMode.
func unixMode(m uint32) os.FileMode {
	fm := os.FileMode(m & 0o777)
	switch m & 0o170000 {
	case 0o040000:
		fm |= os.ModeDir
	case 0o120000:
		fm |= os.ModeSymlink
	case 0o020000:
		fm |= os.ModeDevice | os.ModeCharDevice
	case 0o060000:
		fm |= os.ModeDevice
	case 0o010000:
		fm |= os.ModeNamedPipe
	case 0o140000:
		fm |= os.ModeSocket
	}
	return fm
}

// Entry is one directory entry from Sync.List.
type Entry struct {
	Name string
	FileInfo
}

// Sync is an open sync connection for several requests in a row, such as
// walking a directory tree. It is not safe for concurrent use.
type Sync struct {
	s    *syncConn
	stop func() bool
	v2   bool
}

// OpenSync opens a sync connection. With v2 it uses the LIS2 and LST2
// requests, which carry 64-bit sizes and times and an error for a missing
// path; the device must report the "ls_v2" and "stat_v2" features
// (Android 9 and later).
func (c *Client) OpenSync(ctx context.Context, serial string, v2 bool) (*Sync, error) {
	s, stop, err := c.openSync(ctx, serial)
	if err != nil {
		return nil, err
	}
	return &Sync{s: s, stop: stop, v2: v2}, nil
}

// Close ends the connection.
func (y *Sync) Close() error {
	y.stop()
	y.s.close()
	return nil
}

// Lstat returns what the device's lstat(2) says about path: a symbolic
// link is reported as one, not followed. A missing path returns a
// FileInfo whose Exists is false.
func (y *Sync) Lstat(path string) (FileInfo, error) {
	if !y.v2 {
		return y.statV1(path)
	}
	if err := y.s.send("LST2", []byte(path)); err != nil {
		return FileInfo{}, fmt.Errorf("adb: lstat %s: %w", path, err)
	}
	var buf [72]byte
	if _, err := io.ReadFull(y.s.r, buf[:]); err != nil {
		return FileInfo{}, fmt.Errorf("adb: lstat %s: %w", path, err)
	}
	if string(buf[:4]) != "LST2" {
		return FileInfo{}, fmt.Errorf("adb: lstat %s: unexpected reply %q", path, buf[:4])
	}
	if errno := binary.LittleEndian.Uint32(buf[4:]); errno != 0 {
		if errno == 2 { // ENOENT
			return FileInfo{}, nil
		}
		return FileInfo{}, fmt.Errorf("adb: lstat %s: errno %d", path, errno)
	}
	return statV2(buf[24:]), nil
}

// statV2 decodes mode, nlink, uid, gid, size, atime, mtime and ctime as
// the v2 stat and dent structures lay them out.
func statV2(b []byte) FileInfo {
	mode := binary.LittleEndian.Uint32(b[0:])
	size := binary.LittleEndian.Uint64(b[16:])
	mtime := int64(binary.LittleEndian.Uint64(b[32:]))
	fi := FileInfo{Mode: unixMode(mode), Size: int64(size)}
	if mtime != 0 {
		fi.ModTime = time.Unix(mtime, 0)
	}
	return fi
}

func (y *Sync) statV1(path string) (FileInfo, error) {
	if err := y.s.send("STAT", []byte(path)); err != nil {
		return FileInfo{}, fmt.Errorf("adb: stat %s: %w", path, err)
	}
	var buf [16]byte
	if _, err := io.ReadFull(y.s.r, buf[:]); err != nil {
		return FileInfo{}, fmt.Errorf("adb: stat %s: %w", path, err)
	}
	if string(buf[:4]) != "STAT" {
		return FileInfo{}, fmt.Errorf("adb: stat %s: unexpected reply %q", path, buf[:4])
	}
	fi := FileInfo{
		Mode: unixMode(binary.LittleEndian.Uint32(buf[4:])),
		Size: int64(binary.LittleEndian.Uint32(buf[8:])),
	}
	if mtime := binary.LittleEndian.Uint32(buf[12:]); mtime != 0 {
		fi.ModTime = time.Unix(int64(mtime), 0)
	}
	return fi, nil
}

// List returns the entries of a directory, without "." and "..". Modes
// come from lstat(2), so symbolic links are reported as links. A missing
// or unreadable directory lists as empty.
func (y *Sync) List(dir string) ([]Entry, error) {
	id := "LIST"
	hdrLen := 16 // mode, size, mtime, namelen after the id
	if y.v2 {
		id, hdrLen = "LIS2", 72 // error, dev, ino, mode ... ctime, namelen
	}
	if err := y.s.send(id, []byte(dir)); err != nil {
		return nil, fmt.Errorf("adb: list %s: %w", dir, err)
	}
	var out []Entry
	buf := make([]byte, 4+hdrLen)
	for {
		if _, err := io.ReadFull(y.s.r, buf); err != nil {
			return out, fmt.Errorf("adb: list %s: %w", dir, err)
		}
		switch string(buf[:4]) {
		case "DONE":
			return out, nil
		case "DENT", "DNT2":
		case "FAIL":
			return out, fmt.Errorf("adb: list %s: %w", dir, y.s.readFail(binary.LittleEndian.Uint32(buf[4:])))
		default:
			return out, fmt.Errorf("adb: list %s: unexpected reply %q", dir, buf[:4])
		}
		var fi FileInfo
		var nameLen uint32
		if y.v2 {
			fi = statV2(buf[24:])
			nameLen = binary.LittleEndian.Uint32(buf[72:])
		} else {
			fi = FileInfo{
				Mode: unixMode(binary.LittleEndian.Uint32(buf[4:])),
				Size: int64(binary.LittleEndian.Uint32(buf[8:])),
			}
			if mtime := binary.LittleEndian.Uint32(buf[12:]); mtime != 0 {
				fi.ModTime = time.Unix(int64(mtime), 0)
			}
			nameLen = binary.LittleEndian.Uint32(buf[16:])
		}
		if nameLen > 4096 {
			return out, fmt.Errorf("adb: list %s: name too long (%d)", dir, nameLen)
		}
		name := make([]byte, nameLen)
		if _, err := io.ReadFull(y.s.r, name); err != nil {
			return out, fmt.Errorf("adb: list %s: %w", dir, err)
		}
		if n := string(name); n != "." && n != ".." {
			out = append(out, Entry{Name: n, FileInfo: fi})
		}
	}
}
