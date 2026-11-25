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
	"strings"
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

func (r *recordedLogs) count(msg string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

// waitLog waits until msg is logged.
func (r *recordedLogs) waitLog(t *testing.T, msg string) {
	t.Helper()
	r.waitLogged(t, msg, 1)
}

// waitLogged waits until msg has been logged n times.
func (r *recordedLogs) waitLogged(t *testing.T, msg string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.count(msg) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%q was logged %d times, want %d", msg, r.count(msg), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var (
	replaceTablet = config.Peer{ID: "a9e4ec727e6f64e1", Name: "Tablet", Key: bytes.Repeat([]byte{7}, 32), Paired: time.Now()}
	replaceLaptop = config.Peer{ID: "b1f0c2d3e4a5b6c7", Name: "Laptop", Key: bytes.Repeat([]byte{8}, 32), Paired: time.Now()}
)

// controlFixture is a daemon whose session in control is still cleaning up
// until prev.done closes, behind a real control server.
type controlFixture struct {
	d    *Daemon
	prev *activeSession
	addr string
}

func newControlFixture(t *testing.T) controlFixture {
	t.Helper()
	d, _ := peerDaemon(t, replaceTablet, replaceLaptop)
	f := controlFixture{d: d, prev: &activeSession{peerID: replaceTablet.ID, cancel: func() {}, done: make(chan struct{})}}
	d.ctl.active = f.prev

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
	select {
	case f.addr = <-bound:
	case <-time.After(5 * time.Second):
		t.Fatal("the control server did not start")
	}
	return f
}

// dial connects peer and sends its hello: the connection then waits in
// ServeSession for the session it replaces.
func (f controlFixture) dial(t *testing.T, peer config.Peer) *websocket.Conn {
	t.Helper()
	token, err := crypto.AuthToken(peer.ID, peer.Key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.Dial(t.Context(), "wss://"+f.addr+"/ws", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	sendControl(t, ws, dlnet.MsgHello)
	return ws
}

// waitingConnection connects the tablet to a daemon whose session in control
// is still cleaning up until prev.done closes.
func waitingConnection(t *testing.T) (d *Daemon, prev *activeSession, ws *websocket.Conn) {
	t.Helper()
	f := newControlFixture(t)
	return f.d, f.prev, f.dial(t, replaceTablet)
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

// TestATakeoverByAnotherDeviceIsAnnounced: when the laptop takes over, the
// tablet's connection hears so before it closes, as a reason the client
// does not reconnect on; otherwise the tablet would reconnect and take the
// desk back. A reconnect of the same device hears nothing.
func TestATakeoverByAnotherDeviceIsAnnounced(t *testing.T) {
	logs := recordLogs(t)
	f := newControlFixture(t)
	tablet := f.dial(t, replaceTablet)
	waitFor(t, func() bool { c := f.d.ctl.current(); return c != nil && c != f.prev })
	laptop := f.dial(t, replaceLaptop)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, raw, err := tablet.Read(ctx)
	if err != nil {
		t.Fatalf("the tablet heard nothing before its connection ended: %v", err)
	}
	var msg dlnet.Message
	var info dlnet.ErrorInfo
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != dlnet.MsgError || json.Unmarshal(msg.Payload, &info) != nil {
		t.Fatalf("the tablet got %s, want an error", raw)
	}
	if info.Reason != dlnet.ReasonReplaced || !strings.Contains(info.Message, "Laptop") {
		t.Errorf("the tablet got %+v, want reason %q naming the laptop", info, dlnet.ReasonReplaced)
	}

	// The laptop leaves too; then the first session finishes.
	if err := laptop.CloseNow(); err != nil {
		t.Fatal(err)
	}
	logs.waitLog(t, "control connection failed")
	close(f.prev.done)
	logs.waitLog(t, "the client left before its session started")
	waitFor(t, func() bool { return f.d.ctl.current() == nil })
}

func TestAReconnectOfTheSameDeviceIsNotATakeover(t *testing.T) {
	logs := recordLogs(t)
	f := newControlFixture(t)
	first := f.dial(t, replaceTablet)
	waitFor(t, func() bool { c := f.d.ctl.current(); return c != nil && c != f.prev })
	firstSession := f.d.ctl.current()
	second := f.dial(t, replaceTablet)
	// The second connection replaces the first before it leaves.
	waitFor(t, func() bool { return f.d.ctl.current() != firstSession })
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if _, raw, err := first.Read(ctx); err == nil {
		t.Errorf("the replaced connection of the same tablet got %s, want nothing", raw)
	}
	if err := second.CloseNow(); err != nil {
		t.Fatal(err)
	}
	// The read's deadline closed the first connection too. Both readers must
	// have failed before the session they wait for ends: if only the first
	// had, the second connection would start a session.
	logs.waitLogged(t, "control connection failed", 2)
	close(f.prev.done)
	logs.waitLog(t, "the client left before its session started")
	waitFor(t, func() bool { return f.d.ctl.current() == nil })
}
