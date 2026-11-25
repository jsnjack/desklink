package net

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	stdnet "net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"desklink/internal/config"
)

const (
	// uploadIdleTimeout ends an upload whose body stops arriving: the body
	// of POST /files may take as long as it needs, but not stall.
	uploadIdleTimeout = 30 * time.Second
	// uploadTempPattern names a file while it is being received, hidden in
	// the download directory so file managers do not show it half written.
	uploadTempPattern = ".desklink-upload-*.part"
	// maxFileNameBytes leaves room in a file name for a collision suffix
	// within Linux's 255-byte limit.
	maxFileNameBytes = 200
	// maxExtensionBytes is the longest ending kept as an extension when a
	// long name is shortened.
	maxExtensionBytes = 16
	// fallbackFileName names a file whose name is empty once cleaned.
	fallbackFileName = "file"
	// maxNameCollisions bounds the "name (n).ext" names tried.
	maxNameCollisions = 1000
	// downloadDirMode is the mode of a download directory that is created.
	downloadDirMode = 0o755
)

// UploadReply is the reply to POST /files: the name the file was saved
// under and its size, or why it was refused.
type UploadReply struct {
	Name  string `json:"name,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Error string `json:"error,omitempty"`
}

// Uploads receives the files paired devices send with POST /files and
// saves them, one at a time per device, in a directory.
type Uploads struct {
	// Dir returns the directory files are saved in. It is asked for each
	// upload, so a changed download directory applies at once.
	Dir func() (string, error)
	// Limit is the largest file accepted, in bytes; 0 refuses every file.
	Limit int64
	// Saved, when set, is called with the full path of each saved file.
	Saved func(peer config.Peer, path string)

	mu   sync.Mutex
	busy map[string]bool
	// idle replaces uploadIdleTimeout in tests.
	idle time.Duration
}

// take marks an upload from the peer as running, unless one already is.
func (u *Uploads) take(peerID string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy[peerID] {
		return false
	}
	if u.busy == nil {
		u.busy = map[string]bool{}
	}
	u.busy[peerID] = true
	return true
}

func (u *Uploads) release(peerID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.busy, peerID)
}

// handleUpload serves POST /files?name=...: the body is the file. It is
// written to disk as it arrives, under a hidden temporary name that becomes
// the cleaned file name, with a number added when that is taken, once the
// whole body is there; a body that breaks off or exceeds the limit leaves
// nothing behind. Every answer closes the connection, so a refused upload's
// body is never read.
func (s *ControlServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Connection", "close")
	peer, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	u := s.Uploads
	switch {
	case u.Limit == 0:
		writeJSON(w, http.StatusForbidden, UploadReply{Error: "this computer does not accept files"})
		return
	case r.ContentLength > u.Limit:
		writeJSON(w, http.StatusRequestEntityTooLarge, UploadReply{Error: tooLarge(u.Limit)})
		return
	case !u.take(peer.ID):
		writeJSON(w, http.StatusConflict, UploadReply{Error: "another file from this device is still being received"})
		return
	}
	defer u.release(peer.ID)
	rc := http.NewResponseController(w)
	// The server's timeouts would cut off a large file: the body gets as
	// long as it keeps coming, and the reply its own time afterwards.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		slog.Debug("clear the write deadline for an upload", "err", err)
	}
	idle := u.idle
	if idle == 0 {
		idle = uploadIdleTimeout
	}
	body := &idleReader{r: http.MaxBytesReader(w, r.Body, u.Limit), rc: rc, idle: idle}
	path, size, err := u.save(r.URL.Query().Get("name"), body)
	if err := rc.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
		slog.Debug("set the write deadline for an upload's reply", "err", err)
	}
	if err != nil {
		status, msg := uploadFailure(err, body.err, u.Limit)
		slog.Warn("file upload failed", "peer", peer.Name, "from", r.RemoteAddr, "status", status, "err", err)
		writeJSON(w, status, UploadReply{Error: msg})
		return
	}
	slog.Info("received a file", "peer", peer.Name, "path", path, "bytes", size)
	writeJSON(w, http.StatusOK, UploadReply{Name: filepath.Base(path), Size: size})
	if u.Saved != nil {
		u.Saved(peer, path)
	}
}

// uploadFailure is the status and message for a failed upload; readErr is
// the error reading the body ended with, if that is what failed.
func uploadFailure(err, readErr error, limit int64) (int, string) {
	var tooBig *http.MaxBytesError
	var netErr stdnet.Error
	switch {
	case errors.As(readErr, &tooBig):
		return http.StatusRequestEntityTooLarge, tooLarge(limit)
	case errors.As(readErr, &netErr) && netErr.Timeout():
		return http.StatusRequestTimeout, "the file stopped arriving"
	case readErr != nil:
		return http.StatusBadRequest, "the file broke off before its end"
	case errors.Is(err, syscall.ENOSPC):
		return http.StatusInsufficientStorage, "the disk is full"
	default:
		return http.StatusInternalServerError, "could not save the file"
	}
}

func tooLarge(limit int64) string {
	return fmt.Sprintf("the file is larger than this computer accepts (%d MiB)", limit>>20)
}

// idleReader reads an upload's body, giving each read idle more time, and
// records the error reading ended with.
type idleReader struct {
	r    io.Reader
	rc   *http.ResponseController
	idle time.Duration
	err  error
}

func (b *idleReader) Read(p []byte) (int, error) {
	if err := b.rc.SetReadDeadline(time.Now().Add(b.idle)); err != nil {
		b.err = fmt.Errorf("extend the upload's read deadline: %w", err)
		return 0, b.err
	}
	n, err := b.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.err = err
	}
	return n, err
}

// save writes body to a new file in the upload directory named after name,
// cleaned, and returns its path and size. Nothing is left behind on failure.
func (u *Uploads) save(name string, body io.Reader) (path string, size int64, err error) {
	dir, err := u.Dir()
	if err != nil {
		return "", 0, fmt.Errorf("find the download directory: %w", err)
	}
	if err := os.MkdirAll(dir, downloadDirMode); err != nil {
		return "", 0, fmt.Errorf("create the download directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, uploadTempPattern)
	if err != nil {
		return "", 0, fmt.Errorf("create a file for the upload: %w", err)
	}
	defer func() {
		if err != nil {
			if rerr := os.Remove(tmp.Name()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				slog.Warn("remove a partial upload", "path", tmp.Name(), "err", rerr)
			}
		}
	}()
	size, err = io.Copy(tmp, body)
	if cerr := tmp.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("receive the file: %w", err)
	}
	path, err = moveUnique(tmp.Name(), dir, SanitizeFileName(name))
	if err != nil {
		return "", 0, err
	}
	return path, size, nil
}

// moveUnique renames the file at tmp to name in dir, or to "base (n).ext"
// when that is taken; it never replaces an existing file. The name is
// claimed with an exclusive create first, which works on every filesystem,
// and the rename then replaces that empty claim.
func moveUnique(tmp, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	if ext == name || len(ext) > maxExtensionBytes {
		ext = ""
	}
	base := strings.TrimSuffix(name, ext)
	for i := range maxNameCollisions {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		path := filepath.Join(dir, candidate)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("claim the name %s: %w", candidate, err)
		}
		if err := f.Close(); err != nil {
			slog.Debug("close a claimed file name", "err", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			if rerr := os.Remove(path); rerr != nil {
				slog.Debug("give back a claimed file name", "err", rerr)
			}
			return "", fmt.Errorf("move the upload to %s: %w", candidate, err)
		}
		return path, nil
	}
	return "", fmt.Errorf("%d files named like %s exist already", maxNameCollisions, name)
}

// SanitizeFileName turns a name a client sent into a safe file name in one
// directory: path separators become underscores; control and invisible
// formatting characters (such as the right-to-left override that can
// disguise an extension) are dropped; other spaces become plain ones;
// leading dots and spaces go, so the file is neither hidden nor "." or "..";
// and a long name is shortened to maxFileNameBytes, keeping its extension.
// A name with nothing left becomes "file".
func SanitizeFileName(name string) string {
	name = strings.ToValidUTF8(name, "_")
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return '_'
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsSpace(r):
			return ' '
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimLeft(name, ". "))
	if len(name) > maxFileNameBytes {
		ext := filepath.Ext(name)
		if len(ext) > maxExtensionBytes {
			ext = ""
		}
		base := name[:maxFileNameBytes-len(ext)]
		for !utf8.ValidString(base) {
			base = base[:len(base)-1]
		}
		name = strings.TrimSpace(base) + ext
	}
	if name == "" {
		return fallbackFileName
	}
	return name
}
