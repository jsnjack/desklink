package net

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"desklink/internal/config"
	"desklink/internal/crypto"
)

// uploadFixture is a control server that saves uploads in dir.
type uploadFixture struct {
	controlFixture
	dir   string
	mu    sync.Mutex
	saved []string
	// started is when each saved file's upload started, as reported.
	started []time.Time
}

func startUploads(t *testing.T, limit int64, tune func(*ControlServer)) *uploadFixture {
	t.Helper()
	u := &uploadFixture{dir: t.TempDir()}
	u.controlFixture = startControlWith(t, func(netip.Addr) bool { return true }, func(s *ControlServer) {
		s.Uploads = &Uploads{
			Dir:   func() (string, error) { return u.dir, nil },
			Limit: limit,
			Saved: func(peer config.Peer, path string, started time.Time) {
				u.mu.Lock()
				defer u.mu.Unlock()
				u.saved = append(u.saved, peer.Name+":"+path)
				u.started = append(u.started, started)
			},
		}
		if tune != nil {
			tune(s)
		}
	})
	return u
}

// token pairs a peer named name and returns a bearer token for it.
func (u *uploadFixture) token(t *testing.T, name string) string {
	t.Helper()
	peer := config.Peer{ID: name, Name: name, Key: bytes.Repeat([]byte(name[:1]), 32)}
	if err := u.peers.Add(peer); err != nil {
		t.Fatal(err)
	}
	token, err := crypto.AuthToken(peer.ID, peer.Key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// send posts body as the file name with token, and returns the status and
// reply. size -1 sends the body chunked.
func (u *uploadFixture) send(t *testing.T, token, name string, body io.Reader, size int64) (int, UploadReply) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://"+u.addr+"/files?name="+url.QueryEscape(name), body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = size
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := pinnedClient(u.fingerprint).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Log(err)
		}
	}()
	var reply UploadReply
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("reply %s: %v", raw, err)
		}
	}
	return resp.StatusCode, reply
}

// files lists the directory's file names, hidden ones included.
func (u *uploadFixture) files(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(u.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestUploadNeedsAPairedDevicesToken(t *testing.T) {
	u := startUploads(t, 1<<20, nil)
	stranger, err := crypto.AuthToken("stranger", bytes.Repeat([]byte{9}, 32), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"no token": "", "an unknown device's token": stranger} {
		t.Run(name, func(t *testing.T) {
			if status, _ := u.send(t, token, "a.txt", strings.NewReader("data"), 4); status != http.StatusUnauthorized {
				t.Errorf("HTTP %d, want 401", status)
			}
			if got := u.files(t); len(got) != 0 {
				t.Errorf("files %q were written", got)
			}
		})
	}
}

func TestUploadSavesTheFileUnderItsCleanedName(t *testing.T) {
	u := startUploads(t, 1<<20, nil)
	token := u.token(t, "tablet")
	before := time.Now()
	status, reply := u.send(t, token, "../secret/.report.pdf", strings.NewReader("contents"), 8)
	after := time.Now()
	if status != http.StatusOK || reply.Name != "_secret_.report.pdf" || reply.Size != 8 {
		t.Fatalf("HTTP %d %+v, want 200 _secret_.report.pdf of 8 bytes", status, reply)
	}
	path := filepath.Join(u.dir, reply.Name)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "contents" {
		t.Fatalf("saved %q, %v", got, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("saved file mode %v, %v; want 0600", st.Mode(), err)
	}
	if files := u.files(t); len(files) != 1 {
		t.Errorf("directory holds %q, want only the file", files)
	}
	// The host reports the file after it replied.
	eventually(t, "the saved file was not reported as "+path, func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return len(u.saved) == 1 && u.saved[0] == "tablet:"+path
	})
	// The daemon groups a share's files by when each upload started.
	if started := u.started[0]; started.Before(before) || started.After(after) {
		t.Errorf("upload reported as started at %v, want between %v and %v", started, before, after)
	}
}

func TestUploadAddsANumberWhenTheNameIsTaken(t *testing.T) {
	u := startUploads(t, 1<<20, nil)
	token := u.token(t, "tablet")
	var names []string
	for i := range 3 {
		body := fmt.Sprintf("copy %d", i)
		status, reply := u.send(t, token, "notes.txt", strings.NewReader(body), int64(len(body)))
		if status != http.StatusOK {
			t.Fatalf("upload %d: HTTP %d %s", i, status, reply.Error)
		}
		names = append(names, reply.Name)
	}
	if want := []string{"notes.txt", "notes (1).txt", "notes (2).txt"}; strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("saved as %q, want %q", names, want)
	}
	if got, err := os.ReadFile(filepath.Join(u.dir, "notes.txt")); err != nil || string(got) != "copy 0" {
		t.Errorf("the first file now holds %q, %v; a later upload replaced it", got, err)
	}
}

func TestUploadOverTheLimitIsRefused(t *testing.T) {
	u := startUploads(t, 10, nil)
	token := u.token(t, "tablet")
	cases := []struct {
		name string
		size int64
	}{
		{"announced by its length", 11},
		{"found while it arrives", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, reply := u.send(t, token, "big.bin", strings.NewReader(strings.Repeat("x", 11)), c.size)
			if status != http.StatusRequestEntityTooLarge || reply.Error == "" {
				t.Errorf("HTTP %d %q, want 413 with a reason", status, reply.Error)
			}
			if got := u.files(t); len(got) != 0 {
				t.Errorf("files %q were left behind", got)
			}
		})
	}
}

func TestUploadsCanBeTurnedOff(t *testing.T) {
	u := startUploads(t, 0, nil)
	if status, _ := u.send(t, u.token(t, "tablet"), "a.txt", strings.NewReader("data"), 4); status != http.StatusForbidden {
		t.Errorf("HTTP %d, want 403", status)
	}
}

// rawUpload starts POST /files on a TLS connection of its own, announcing
// length bytes, and sends first.
func rawUpload(t *testing.T, u *uploadFixture, token string, length int, first string) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", u.addr, pinnedTLS(u.fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	head := fmt.Sprintf("POST /files?name=part.bin HTTP/1.1\r\nHost: desklink\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n\r\n", token, length)
	if _, err := io.WriteString(conn, head+first); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestAnUploadThatBreaksOffLeavesNothing(t *testing.T) {
	cases := []struct {
		name string
		end  func(*tls.Conn) error
	}{
		{"the client goes away", (*tls.Conn).Close},
		{"the client stops sending", func(*tls.Conn) error { return nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := startUploads(t, 1<<20, func(s *ControlServer) { s.Uploads.idle = 300 * time.Millisecond })
			conn := rawUpload(t, u, u.token(t, "tablet"), 1000, strings.Repeat("x", 100))
			eventually(t, "the partial file never appeared", func() bool { return len(u.files(t)) == 1 })
			if err := c.end(conn); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the partial file was left behind", func() bool { return len(u.files(t)) == 0 })
			u.mu.Lock()
			defer u.mu.Unlock()
			if len(u.saved) != 0 {
				t.Errorf("a broken-off upload counted as saved: %q", u.saved)
			}
		})
	}
}

// TestUploadStreamsToDisk: the file grows on disk while its body arrives,
// so a 2 GiB upload never sits in memory.
func TestUploadStreamsToDisk(t *testing.T) {
	u := startUploads(t, 1<<30, nil)
	token := u.token(t, "tablet")
	pr, pw := io.Pipe()
	chunk := bytes.Repeat([]byte("y"), 256<<10)
	done := make(chan UploadReply, 1)
	go func() {
		_, reply := u.send(t, token, "stream.bin", pr, -1)
		done <- reply
	}()
	if _, err := pw.Write(chunk); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first part of the body never reached the disk", func() bool {
		files := u.files(t)
		if len(files) != 1 || !strings.HasPrefix(files[0], ".desklink-upload-") {
			return false
		}
		st, err := os.Stat(filepath.Join(u.dir, files[0]))
		return err == nil && st.Size() >= int64(len(chunk))
	})
	if _, err := pw.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	reply := wait(t, done)
	if reply.Name != "stream.bin" || reply.Size != int64(2*len(chunk)) {
		t.Errorf("reply %+v, want stream.bin of %d bytes", reply, 2*len(chunk))
	}
}

func TestASecondUploadFromTheSameDeviceIsRefused(t *testing.T) {
	u := startUploads(t, 1<<20, nil)
	tablet := u.token(t, "tablet")
	pr, pw := io.Pipe()
	first := make(chan int, 1)
	go func() {
		status, _ := u.send(t, tablet, "first.bin", pr, -1)
		first <- status
	}()
	if _, err := pw.Write([]byte("started")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first upload never started", func() bool { return len(u.files(t)) == 1 })
	if status, _ := u.send(t, tablet, "second.bin", strings.NewReader("x"), 1); status != http.StatusConflict {
		t.Errorf("a second upload from the same device: HTTP %d, want 409", status)
	}
	if status, _ := u.send(t, u.token(t, "laptop"), "other.bin", strings.NewReader("x"), 1); status != http.StatusOK {
		t.Errorf("an upload from another device: HTTP %d, want 200", status)
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	if status := wait(t, first); status != http.StatusOK {
		t.Errorf("the first upload: HTTP %d, want 200", status)
	}
	if status, _ := u.send(t, tablet, "third.bin", strings.NewReader("x"), 1); status != http.StatusOK {
		t.Errorf("an upload after the first finished: HTTP %d, want 200", status)
	}
}

// slowReader returns its data a byte at a time, pausing before each.
type slowReader struct {
	data  []byte
	pause time.Duration
}

func (r *slowReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.pause)
	p[0], r.data = r.data[0], r.data[1:]
	return 1, nil
}

func TestAnUploadOutlivesTheRequestTimeouts(t *testing.T) {
	const timeout = 200 * time.Millisecond
	u := startUploads(t, 1<<20, func(s *ControlServer) { s.readTimeout, s.writeTimeout = timeout, timeout })
	body := &slowReader{data: []byte("slow file"), pause: timeout / 2}
	status, reply := u.send(t, u.token(t, "tablet"), "slow.txt", body, 9)
	if status != http.StatusOK || reply.Size != 9 {
		t.Errorf("HTTP %d %+v, want 200 after the body took longer than the request timeouts", status, reply)
	}
}

func TestSanitizeFileName(t *testing.T) {
	long := strings.Repeat("é", 150) + ".pdf"
	cases := []struct {
		name, in, want string
	}{
		{"a plain name is kept", "Quarterly report.pdf", "Quarterly report.pdf"},
		{"path separators become underscores", "../../etc/passwd", "_.._etc_passwd"},
		{"Windows separators too", `C:\Users\me\notes.txt`, "C:_Users_me_notes.txt"},
		{"leading dots go, so the file is not hidden", ".bashrc", "bashrc"},
		{"a name of dots becomes file", "..", "file"},
		{"an empty name becomes file", "", "file"},
		{"control characters are dropped", "a\x00b\nc\x7f.txt", "abc.txt"},
		{"a right-to-left override cannot disguise the extension", "photo\u202egpj.exe", "photogpj.exe"},
		{"other spaces become plain ones", "a\u00a0b\u2028c.txt", "a b c.txt"},
		{"surrounding spaces go", "  name.txt  ", "name.txt"},
		{"invalid UTF-8 is replaced", "bad\xffname", "bad_name"},
		{"a long name is shortened, keeping its extension", long, strings.Repeat("é", 98) + ".pdf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SanitizeFileName(c.in); got != c.want {
				t.Errorf("SanitizeFileName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
