package daemon

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	stdnet "net"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"desklink/internal/crypto"
	"desklink/internal/desktop"
	dlnet "desklink/internal/net"
)

// sessionsFunc serves each session with a function.
type sessionsFunc func(ctx context.Context, conn *dlnet.ControlConn)

func (f sessionsFunc) ServeSession(ctx context.Context, conn *dlnet.ControlConn) { f(ctx, conn) }

// controlPair connects the tablet to a control server and returns the
// host's end of the connection, open until the test ends, and the
// tablet's.
func controlPair(t *testing.T) (*dlnet.ControlConn, *websocket.Conn) {
	t.Helper()
	d, _ := peerDaemon(t, replaceTablet)
	dir := t.TempDir()
	tlsCfg, _, err := dlnet.LoadOrCreateCert(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	conns := make(chan *dlnet.ControlConn, 1)
	srv := &dlnet.ControlServer{
		Addr: "127.0.0.1:0", TLS: tlsCfg, Peers: d.peers, Allow: func(netip.Addr) bool { return true },
		Sessions: sessionsFunc(func(ctx context.Context, conn *dlnet.ControlConn) {
			conns <- conn
			<-ctx.Done()
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bound := make(chan string, 1)
	go func() {
		if err := srv.Serve(ctx, func(a stdnet.Addr) { bound <- a.String() }); err != nil {
			t.Error(err)
		}
	}()
	var addr string
	select {
	case addr = <-bound:
	case <-time.After(5 * time.Second):
		t.Fatal("the control server did not start")
	}
	token, err := crypto.AuthToken(replaceTablet.ID, replaceTablet.Key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.Dial(t.Context(), "wss://"+addr+"/ws", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	ws.SetReadLimit(-1)
	select {
	case conn := <-conns:
		return conn, ws
	case <-time.After(5 * time.Second):
		t.Fatal("the connection did not reach the session handler")
	}
	return nil, nil
}

// TestAnImageStopsWhenTheSessionEndsAndTheTakeoverNoticeStillGoesOut: a
// session that ends while it sends a clipboard image sends no more parts,
// and the connection stays open for the notice that another device took the
// session over. coder/websocket closes the connection when the context of a
// write has ended, so the client would hear nothing and reconnect.
func TestAnImageStopsWhenTheSessionEndsAndTheTakeoverNoticeStillGoesOut(t *testing.T) {
	conn, ws := controlPair(t)
	f := newSessionFixture(t)
	f.s.conn = conn
	f.s.cancel(errors.New("another device took over"))
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, bytes.Repeat([]byte{1}, 2*dlnet.ClipboardImagePart)...)
	f.s.clipMu.Lock()
	f.s.sendClipboardLocked(f.s.ctx, desktop.Content{PNG: png})
	f.s.clipMu.Unlock()
	notice := dlnet.ErrorInfo{Message: "Laptop took over the session", Reason: dlnet.ReasonReplaced}
	if err := conn.Send(t.Context(), dlnet.MsgError, notice); err != nil {
		t.Fatalf("the takeover notice could not go out: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("the tablet heard nothing: %v", err)
	}
	var m dlnet.Message
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != dlnet.MsgError {
		t.Errorf("the tablet got %q first, want the takeover notice and no image part", m.Type)
	}
}
