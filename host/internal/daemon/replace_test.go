package daemon

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	stdnet "net"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"desklink/internal/config"
	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

// recordedLogs keeps the messages logged while it is slog's default handler.
type recordedLogs struct {
	mu   sync.Mutex
	msgs []string
}

func recordLogs(t *testing.T) *recordedLogs {
	t.Helper()
	r := &recordedLogs{}
	old := slog.Default()
	slog.SetDefault(slog.New(r))
	t.Cleanup(func() { slog.SetDefault(old) })
	return r
}

func (r *recordedLogs) Enabled(context.Context, slog.Level) bool { return true }
func (r *recordedLogs) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *recordedLogs) WithGroup(string) slog.Handler            { return r }

func (r *recordedLogs) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, rec.Message)
	return nil
}

func (r *recordedLogs) has(msg string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.msgs, msg)
}

// waitLog waits until msg is logged.
func (r *recordedLogs) waitLog(t *testing.T, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !r.has(msg) {
		if time.Now().After(deadline) {
			t.Fatalf("%q was not logged", msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitingConnection connects a tablet to a daemon whose session in control
// is still cleaning up until prev.done closes, and sends its hello: the
// connection then waits in ServeSession for prev.
func waitingConnection(t *testing.T) (d *Daemon, prev *activeSession, ws *websocket.Conn) {
	t.Helper()
	peer := config.Peer{ID: "a9e4ec727e6f64e1", Name: "Tablet", Key: bytes.Repeat([]byte{7}, 32), Paired: time.Now()}
	d, _ = peerDaemon(t, peer)
	prev = &activeSession{peerID: peer.ID, cancel: func() {}, done: make(chan struct{})}
	d.ctl.active = prev

	dir := t.TempDir()
	tlsCfg, _, err := dlnet.LoadOrCreateCert(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &dlnet.ControlServer{
		Addr: "127.0.0.1:0", TLS: tlsCfg, Peers: d.peers, Sessions: d,
		Allow: func(netip.Addr) bool { return true },
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

	token, err := crypto.AuthToken(peer.ID, peer.Key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err = websocket.Dial(ctx, "wss://"+addr+"/ws", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	sendControl(t, ws, dlnet.MsgHello)
	return d, prev, ws
}

// sendControl sends a control message with an empty payload.
func sendControl(t *testing.T, ws *websocket.Conn, msgType string) {
	t.Helper()
	msg, err := json.Marshal(dlnet.Message{Type: msgType, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(context.Background(), websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
}

// startsNoSession checks that once prev is done the waiting connection
// gives up control without starting a session for its client, who has left.
func startsNoSession(t *testing.T, logs *recordedLogs, d *Daemon, prev *activeSession) {
	t.Helper()
	close(prev.done)
	logs.waitLog(t, "the client left before its session started")
	if logs.has("session started") {
		t.Error("a session started for a client that had left")
	}
	deadline := time.Now().Add(5 * time.Second)
	for d.ctl.current() != nil {
		if time.Now().After(deadline) {
			t.Fatal("the connection still holds control after its client left")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestReplacingConnectionAnswersPings is the failed reconnect of 2026-11-25:
// the session in control hung, and the tablet's new connection, waiting for
// it, answered no pings, so the tablet gave up ("sent ping but didn't receive
// pong"). The waiting connection must answer pings, and must not start a
// session once its client has left.
func TestReplacingConnectionAnswersPings(t *testing.T) {
	logs := recordLogs(t)
	d, prev, ws := waitingConnection(t)
	ctx := t.Context()
	// The client reads the pongs only inside Read.
	go func() { _, _, _ = ws.Read(ctx) }()
	for i := range 3 {
		pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
		err := ws.Ping(pingCtx)
		pingCancel()
		if err != nil {
			t.Fatalf("ping %d while the replaced session cleans up: %v", i, err)
		}
	}

	// The client gives up; then the replaced session finishes.
	if err := ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	logs.waitLog(t, "control connection failed")
	startsNoSession(t, logs, d, prev)
}

// TestAnOverloadedConnectionIsClosed: the reader stops reading once the
// control queue is full, so it would answer no more pings and miss the
// client leaving. A client that sends more control messages than its session
// takes is disconnected instead.
func TestAnOverloadedConnectionIsClosed(t *testing.T) {
	logs := recordLogs(t)
	d, prev, ws := waitingConnection(t)
	for range controlQueue + 1 {
		sendControl(t, ws, dlnet.MsgReqIDR)
	}
	logs.waitLog(t, "closing a control connection that sent more messages than its session took")
	startsNoSession(t, logs, d, prev)
}
