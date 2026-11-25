package net

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"desklink/internal/config"
	"desklink/internal/crypto"
)

type memPeers struct {
	mu    sync.Mutex
	peers map[string]config.Peer
}

func (m *memPeers) Get(id string) (config.Peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.peers[id]
	return p, ok
}

func (m *memPeers) Add(p config.Peer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peers[p.ID] = p
	return nil
}

func (m *memPeers) forget(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.peers, id)
	return nil
}

type echoSessions struct {
	peers chan string
}

func (e *echoSessions) ServeSession(ctx context.Context, conn *ControlConn) {
	e.peers <- conn.Peer.ID
	msg, err := conn.Receive(ctx)
	if err != nil {
		return
	}
	if err := conn.Send(ctx, MsgSession, map[string]string{"echo": msg.Type}); err != nil {
		return
	}
	if _, err := conn.Receive(ctx); err != nil {
		return
	}
}

type controlFixture struct {
	addr        string
	fingerprint string
	pairer      *crypto.Pairer
	peers       *memPeers
	sessions    *echoSessions
	srv         *ControlServer
}

func startControl(t *testing.T, allow func(netip.Addr) bool) controlFixture {
	t.Helper()
	return startControlWith(t, allow, nil)
}

// startControlWith starts a control server that tune, when not nil, adjusts
// before it serves.
func startControlWith(t *testing.T, allow func(netip.Addr) bool, tune func(*ControlServer)) controlFixture {
	t.Helper()
	dir := t.TempDir()
	tlsCfg, fp, err := LoadOrCreateCert(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	f := controlFixture{fingerprint: fp, pairer: crypto.NewPairer(fp, time.Now), peers: &memPeers{peers: map[string]config.Peer{}}, sessions: &echoSessions{peers: make(chan string, 1)}}
	srv := &ControlServer{
		Addr: "127.0.0.1:0", TLS: tlsCfg, Peers: f.peers, Pairer: f.pairer, Sessions: f.sessions, Allow: allow,
		Health: func() Health { return Health{Name: "desklinkd", TimeMs: time.Now().UnixMilli()} },
		Forget: f.peers.forget,
	}
	if tune != nil {
		tune(srv)
	}
	f.srv = srv
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bound := make(chan string, 1)
	go func() {
		if err := srv.Serve(ctx, func(a stdnet.Addr) { bound <- a.String() }); err != nil {
			t.Log(err)
		}
	}()
	f.addr = wait(t, bound)
	return f
}

// pinnedClient trusts only the certificate with the given fingerprint, like
// the Android client after scanning the pairing QR code.
func pinnedClient(fp string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: pinnedTLS(fp)}}
}

func pinnedTLS(fp string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || crypto.Fingerprint(raw[0]) != fp {
				return errors.New("certificate fingerprint mismatch")
			}
			return nil
		},
	}
}

func postPair(t *testing.T, c *http.Client, addr string, req PairRequest) (int, PairResponse) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Post("https://"+addr+"/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Log(err)
		}
	}()
	var out PairResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func pairOverHTTP(t *testing.T, f controlFixture, pin string) (int, PairResponse, []byte) {
	t.Helper()
	c := pinnedClient(f.fingerprint)
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	status, start := postPair(t, c, f.addr, PairRequest{Phase: PairPhaseStart, ClientPub: priv.PublicKey().Bytes()})
	if status != http.StatusOK {
		t.Fatalf("start: HTTP %d %s", status, start.Error)
	}
	hostKey, err := ecdh.P256().NewPublicKey(start.HostPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := priv.ECDH(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypto.DerivePairingKeys(shared, pin, priv.PublicKey().Bytes(), start.HostPub, f.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	status, fin := postPair(t, c, f.addr, PairRequest{Phase: PairPhaseFinish, PairingID: start.PairingID, Proof: keys.ClientProof(), Name: "Tab S9"})
	if status == http.StatusOK && !bytes.Equal(fin.Proof, keys.HostProof()) {
		t.Fatal("host proof does not verify")
	}
	return status, fin, keys.PeerKey
}

func TestControlPlane(t *testing.T) {
	f := startControl(t, func(netip.Addr) bool { return true })
	pin, _, err := f.pairer.Begin()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("health needs no token", func(t *testing.T) {
		resp, err := pinnedClient(f.fingerprint).Get("https://" + f.addr + "/health")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Log(err)
			}
		}()
		var h Health
		if err := json.NewDecoder(resp.Body).Decode(&h); err != nil || h.Name != "desklinkd" || h.TimeMs == 0 {
			t.Fatalf("health %+v, %v", h, err)
		}
	})
	t.Run("wrong PIN is refused", func(t *testing.T) {
		if status, _, _ := pairOverHTTP(t, f, "999999x"); status != http.StatusUnauthorized {
			t.Errorf("HTTP %d, want 401", status)
		}
	})

	var peerID string
	var peerKey []byte
	t.Run("pairing stores a peer with the derived key", func(t *testing.T) {
		status, fin, key := pairOverHTTP(t, f, pin)
		if status != http.StatusOK {
			t.Fatalf("HTTP %d %s", status, fin.Error)
		}
		stored, ok := f.peers.Get(fin.PeerID)
		if !ok || !bytes.Equal(stored.Key, key) || stored.Name != "Tab S9" {
			t.Fatalf("stored peer %+v", stored)
		}
		peerID, peerKey = fin.PeerID, key
	})
	t.Run("the PIN works only once", func(t *testing.T) {
		c := pinnedClient(f.fingerprint)
		priv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if status, _ := postPair(t, c, f.addr, PairRequest{Phase: PairPhaseStart, ClientPub: priv.PublicKey().Bytes()}); status != http.StatusForbidden {
			t.Errorf("HTTP %d, want 403", status)
		}
	})
	dial := func(token string) (*websocket.Conn, *http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return websocket.Dial(ctx, "wss://"+f.addr+"/ws", &websocket.DialOptions{
			HTTPClient: pinnedClient(f.fingerprint),
			HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		})
	}
	t.Run("websocket accepts the paired peer's token", func(t *testing.T) {
		token, err := crypto.AuthToken(peerID, peerKey, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		ws, _, err := dial(token)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := ws.CloseNow(); err != nil {
				t.Log(err)
			}
		}()
		if got := wait(t, f.sessions.peers); got != peerID {
			t.Errorf("session for %q, want %q", got, peerID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","payload":{"codec":"av1"}}`)); err != nil {
			t.Fatal(err)
		}
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m Message
		if err := json.Unmarshal(data, &m); err != nil || m.Type != MsgSession || string(m.Payload) != `{"echo":"hello"}` {
			t.Fatalf("reply %s, %v", data, err)
		}
	})
	t.Run("websocket rejects a forged token", func(t *testing.T) {
		token, err := crypto.AuthToken(peerID, make([]byte, 32), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, resp, err := dial(token)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("err %v, response %v", err, resp)
		}
	})
	forget := func(token string) int {
		req, err := http.NewRequest(http.MethodDelete, "https://"+f.addr+"/pair", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := pinnedClient(f.fingerprint).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Log(err)
		}
		return resp.StatusCode
	}
	t.Run("only the peer itself can ask to be forgotten", func(t *testing.T) {
		forged, err := crypto.AuthToken(peerID, make([]byte, 32), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if status := forget(forged); status != http.StatusUnauthorized {
			t.Errorf("forged token: HTTP %d, want 401", status)
		}
		token, err := crypto.AuthToken(peerID, peerKey, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if status := forget(token); status != http.StatusOK {
			t.Fatalf("HTTP %d, want 200", status)
		}
		if _, ok := f.peers.Get(peerID); ok {
			t.Error("the peer is still paired")
		}
		if status := forget(token); status != http.StatusUnauthorized {
			t.Errorf("a forgotten peer's token: HTTP %d, want 401", status)
		}
	})
	t.Run("a client pinned to another certificate cannot connect", func(t *testing.T) {
		if _, err := pinnedClient("00").Get("https://" + f.addr + "/health"); err == nil {
			t.Error("connected despite a fingerprint mismatch")
		}
	})
}

func TestControlPlaneDropsDisallowedAddresses(t *testing.T) {
	f := startControl(t, func(netip.Addr) bool { return false })
	if _, err := pinnedClient(f.fingerprint).Get("https://" + f.addr + "/health"); err == nil {
		t.Error("a disallowed address got a response")
	}
}

func TestAllowList(t *testing.T) {
	cfg := config.Default()
	prefixes, err := cfg.Subnets()
	if err != nil {
		t.Fatal(err)
	}
	allow := AllowList(prefixes)
	cases := []struct {
		addr string
		want bool
	}{
		{"192.168.178.20", true},
		{"10.1.2.3", true},
		{"172.16.0.1", true},
		{"172.32.0.1", false},
		{"127.0.0.1", true},
		{"8.8.8.8", false},
		{"100.64.0.1", false},
		{"::ffff:192.168.1.5", true},
	}
	for _, c := range cases {
		t.Run(c.addr, func(t *testing.T) {
			if got := allow(netip.MustParseAddr(c.addr)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestLoadOrCreateCertIsStable(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c"), filepath.Join(dir, "k")
	_, fp1, err := LoadOrCreateCert(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	cfg, fp2, err := LoadOrCreateCert(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Error("fingerprint changed between loads")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("min TLS version %#x", cfg.MinVersion)
	}
	if len(fp1) != 64 {
		t.Errorf("fingerprint %q is not 64 hex digits", fp1)
	}
}

// stallPair sends the headers of a pairing request and the first byte of
// its body, then nothing more, like a client that stopped responding.
func stallPair(t *testing.T, f controlFixture) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", f.addr, pinnedTLS(f.fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, stdnet.ErrClosed) {
			t.Log(err)
		}
	})
	if _, err := io.WriteString(conn, "POST /pair HTTP/1.1\r\nHost: desklink\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	return conn
}

// answer reads the server's reply on conn, waiting at most within.
func answer(conn *tls.Conn, within time.Duration) (*http.Response, error) {
	if err := conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(conn), nil)
}

func TestAStalledPairingRequestIsCutOff(t *testing.T) {
	const readTimeout = 500 * time.Millisecond
	f := startControlWith(t, func(netip.Addr) bool { return true }, func(s *ControlServer) { s.readTimeout = readTimeout })
	if _, _, err := f.pairer.Begin(); err != nil {
		t.Fatal(err)
	}
	conn := stallPair(t, f)
	resp, err := answer(conn, readTimeout+4*time.Second)
	var ne stdnet.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("the server still waits for the body after its read timeout")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("HTTP %d, want 400 for the cut-off body", resp.StatusCode)
	}
}

func TestAClosedPairingWindowAnswersWithoutTheBody(t *testing.T) {
	f := startControl(t, func(netip.Addr) bool { return true })
	// No `desklinkd pair`: the server must refuse before waiting for the
	// body, well within its 10 s read timeout.
	conn := stallPair(t, f)
	resp, err := answer(conn, 3*time.Second)
	if err != nil {
		t.Fatalf("no answer while the body is outstanding: %v", err)
	}
	var body PairResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || body.Error != crypto.ErrNoPairing.Error() {
		t.Errorf("HTTP %d %q, want 403 %q", resp.StatusCode, body.Error, crypto.ErrNoPairing)
	}
}

// eventually waits up to three seconds for cond, failing with what.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPairingRequestsAreLimitedButHealthIsNot(t *testing.T) {
	f := startControl(t, func(netip.Addr) bool { return true })
	if _, _, err := f.pairer.Begin(); err != nil {
		t.Fatal(err)
	}
	busy := func(n int) {
		t.Helper()
		eventually(t, fmt.Sprintf("pairing requests in progress never reached %d", n), func() bool { return len(f.srv.unauthenticated) == n })
	}
	status := func(req func(*http.Client) (*http.Response, error)) int {
		t.Helper()
		resp, err := req(pinnedClient(f.fingerprint))
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Log(err)
		}
		return resp.StatusCode
	}
	health := func(c *http.Client) (*http.Response, error) { return c.Get("https://" + f.addr + "/health") }
	pair := func(c *http.Client) (*http.Response, error) {
		return c.Post("https://"+f.addr+"/pair", "application/json", strings.NewReader(`{"phase":"nonsense"}`))
	}
	var stalled []*tls.Conn
	for range maxUnauthenticated {
		stalled = append(stalled, stallPair(t, f))
	}
	busy(maxUnauthenticated)
	if got := status(pair); got != http.StatusServiceUnavailable {
		t.Errorf("pairing with every slot held by a stalled request: HTTP %d, want 503", got)
	}
	if got := status(health); got != http.StatusOK {
		t.Errorf("health with every pairing slot held: HTTP %d, want 200", got)
	}
	for _, c := range stalled {
		if err := c.Close(); err != nil {
			t.Log(err)
		}
	}
	busy(0)
	if got := status(pair); got != http.StatusBadRequest {
		t.Errorf("pairing after the stalled requests went away: HTTP %d, want 400 for the unknown phase", got)
	}
}

func TestConnectionsPerAddressAreCapped(t *testing.T) {
	const perIP = 3
	f := startControlWith(t, func(netip.Addr) bool { return true }, func(s *ControlServer) { s.connsPerIP = perIP })
	local := netip.MustParseAddr("127.0.0.1")
	open := func() stdnet.Conn {
		t.Helper()
		c, err := stdnet.Dial("tcp4", f.addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := c.Close(); err != nil && !errors.Is(err, stdnet.ErrClosed) {
				t.Log(err)
			}
		})
		return c
	}
	var held []stdnet.Conn
	for range perIP {
		held = append(held, open())
	}
	eventually(t, "the server never counted the open connections", func() bool { return f.srv.listener.openConns(local) == perIP })
	t.Run("one more is closed before TLS", func(t *testing.T) {
		extra := open()
		if err := extra.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := extra.Read(make([]byte, 1)); err == nil {
			t.Error("the connection over the limit got data")
		} else if ne, ok := err.(stdnet.Error); ok && ne.Timeout() {
			t.Error("the connection over the limit was left open")
		}
	})
	t.Run("a closed connection frees its slot", func(t *testing.T) {
		if err := held[0].Close(); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the closed connection still counts", func() bool { return f.srv.listener.openConns(local) == perIP-1 })
		resp, err := pinnedClient(f.fingerprint).Get("https://" + f.addr + "/health")
		if err != nil {
			t.Fatalf("a request within the limit failed: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Log(err)
		}
	})
}

func TestAClientThatStopsReadingIsCutOff(t *testing.T) {
	const writeTimeout = 300 * time.Millisecond
	// A response far larger than the socket buffers, so writing it blocks
	// while the client does not read.
	big := strings.Repeat("x", 16<<20)
	f := startControlWith(t, func(netip.Addr) bool { return true }, func(s *ControlServer) {
		s.writeTimeout = writeTimeout
		s.Health = func() Health { return Health{Name: "desklinkd", Hostname: big} }
	})
	conn, err := tls.Dial("tcp4", f.addr, pinnedTLS(f.fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, stdnet.ErrClosed) {
			t.Log(err)
		}
	})
	if _, err := io.WriteString(conn, "GET /health HTTP/1.1\r\nHost: desklink\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	local := netip.MustParseAddr("127.0.0.1")
	eventually(t, "the server never counted the connection", func() bool { return f.srv.listener.openConns(local) == 1 })
	// The client never reads; the server must give up writing and close.
	// crypto/tls then still tries its close-notify alert for up to 5 s.
	deadline := time.Now().Add(writeTimeout + 10*time.Second)
	for f.srv.listener.openConns(local) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the server still holds the connection of a client that stopped reading")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAWebSocketOutlivesTheRequestTimeouts(t *testing.T) {
	const timeout = 300 * time.Millisecond
	f := startControlWith(t, func(netip.Addr) bool { return true }, func(s *ControlServer) {
		s.readTimeout, s.writeTimeout = timeout, timeout
	})
	peer := config.Peer{ID: "tab", Name: "Tab", Key: bytes.Repeat([]byte{7}, 32)}
	if err := f.peers.Add(peer); err != nil {
		t.Fatal(err)
	}
	token, err := crypto.AuthToken(peer.ID, peer.Key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "wss://"+f.addr+"/ws", &websocket.DialOptions{
		HTTPClient: pinnedClient(f.fingerprint),
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ws.CloseNow(); err != nil {
			t.Log(err)
		}
	}()
	wait(t, f.sessions.peers)
	// Idle for longer than the server's read and write timeouts; the server
	// then reads the hello and writes its reply.
	time.Sleep(3 * timeout)
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","payload":{}}`)); err != nil {
		t.Fatal(err)
	}
	if _, data, err := ws.Read(ctx); err != nil {
		t.Fatalf("the session ended while idle: %v", err)
	} else if !bytes.Contains(data, []byte(`"echo":"hello"`)) {
		t.Errorf("reply %s", data)
	}
}

// The tablet's ProtocolTest reads and writes the same JSON.
func TestScreenGeometryOnTheWire(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{
			"a hello names the monitor to mirror",
			Hello{Codec: "av1", Width: 1600, Height: 2560, FPS: 120, QP: 18, Screen: ScreenMirror, Monitor: "DP-5"},
			`{"codec":"av1","width":1600,"height":2560,"fps":120,"qp":18,"screen":"mirror","monitor":"DP-5"}`,
		},
		{
			"a hello without a chosen monitor leaves it out",
			Hello{Codec: "av1", Width: 2560, Height: 1600, FPS: 120, QP: 18},
			`{"codec":"av1","width":2560,"height":1600,"fps":120,"qp":18}`,
		},
		{"mirror names a connector", MirrorChange{Connector: "DP-5"}, `{"connector":"DP-5"}`},
		{"resize gives the size in pixels", Resize{Width: 1600, Height: 2560}, `{"width":1600,"height":2560}`},
		{
			"a mirroring session lists the monitors and the one it mirrors",
			SessionInfo{Screen: ScreenMirror, Mirroring: "eDP-1", Monitors: []MonitorInfo{
				{Connector: "DP-5", Width: 2560, Height: 1440, Refresh: 59.951, Primary: true},
				{Connector: "eDP-1", Width: 2560, Height: 1600, Refresh: 120},
			}},
			`{"session_id":0,"udp_port":0,"host_nonce":null,"codec":"","width":0,"height":0,"fps":0,"qp":0,"display":"",` +
				`"auto":false,"level":0,"levels":null,"screen":"mirror","monitors":[` +
				`{"connector":"DP-5","width":2560,"height":1440,"refresh":59.951,"primary":true},` +
				`{"connector":"eDP-1","width":2560,"height":1600,"refresh":120}],"mirroring":"eDP-1"}`,
		},
		{
			"an own screen with the desk off leaves both out",
			SessionInfo{Screen: ScreenOwn, Scale: 2},
			`{"session_id":0,"udp_port":0,"host_nonce":null,"codec":"","width":0,"height":0,"fps":0,"qp":0,"display":"",` +
				`"auto":false,"level":0,"levels":null,"screen":"own","scale":2}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.v)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("JSON\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// TestTextToTypeHasTheShapeClientsSend: ProtocolTest.kt writes the same JSON.
func TestTextToTypeHasTheShapeClientsSend(t *testing.T) {
	cases := []struct {
		name, json, text string
	}{
		{"accents and emoji stay UTF-8", `{"text":"é😀"}`, "é😀"},
		{"backspaces and a newline are JSON escapes", `{"text":"\b\bé😀\n"}`, "\b\bé😀\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got TypeText
			if err := json.Unmarshal([]byte(c.json), &got); err != nil || got.Text != c.text {
				t.Errorf("decoded %q (%v), want %q", got.Text, err, c.text)
			}
		})
	}
	t.Run("the laptop client encodes text as the tablet does", func(t *testing.T) {
		raw, err := json.Marshal(TypeText{Text: "é😀"})
		if err != nil || string(raw) != `{"text":"é😀"}` {
			t.Errorf("encoded %s (%v)", raw, err)
		}
	})
	t.Run("only a host that types text says so", func(t *testing.T) {
		for _, keysyms := range []bool{true, false} {
			raw, err := json.Marshal(SessionInfo{Keysyms: keysyms})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(raw), `"keysyms"`); got != keysyms {
				t.Errorf("SessionInfo{Keysyms: %v} encodes as %s", keysyms, raw)
			}
		}
	})
}

// sharpHello is the hello the Android app sends for its Sharp preset; the
// same string is asserted in ProtocolTest.kt.
const sharpHello = `{"codec":"av1","width":2560,"height":1600,"fps":120,"qp":12,"auto":false,"fixed":true,"audio":false,"screen":"auto","scale":0.0,"keep_awake":0,"lock_after":false,"clipboard_images":8388608}`

func TestHelloFixed(t *testing.T) {
	t.Run("the tablet's sharp hello asks for fixed quality", func(t *testing.T) {
		var h Hello
		if err := json.Unmarshal([]byte(sharpHello), &h); err != nil {
			t.Fatal(err)
		}
		if !h.Fixed || h.Auto || h.QP != 12 || h.Width != 2560 || h.FPS != 120 {
			t.Errorf("decoded %+v", h)
		}
	})
	t.Run("the laptop sends fixed only when it is set", func(t *testing.T) {
		cases := []struct {
			hello Hello
			want  bool
		}{{Hello{Codec: "av1", QP: 12, Fixed: true}, true}, {Hello{Codec: "av1", QP: 12, Auto: true}, false}}
		for _, c := range cases {
			data, err := json.Marshal(c.hello)
			if err != nil {
				t.Fatal(err)
			}
			if got := bytes.Contains(data, []byte(`"fixed":true`)); got != c.want || bytes.Contains(data, []byte(`"fixed":false`)) {
				t.Errorf("%s", data)
			}
		}
	})
}
