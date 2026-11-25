// Package net implements the DeskLink control plane (TLS 1.3 HTTPS and
// WebSocket on :9443), the encrypted UDP media plane (:9444) and the local
// Unix-socket API that `desklinkd pair` uses.
package net

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	stdnet "net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"desklink/internal/config"
	"desklink/internal/crypto"
	"desklink/internal/logging"
)

// LevelTrace is the slog level enabled by --trace.
const LevelTrace = logging.LevelTrace

const (
	// MaxControlMessage fits a 256 KB clipboard even when JSON escapes every
	// character (<, > and & become six bytes each).
	MaxControlMessage = 2 << 20
	idleTimeout       = time.Minute
	maxPairBody       = 4 << 10
	certLifetime      = 10 * 365 * 24 * time.Hour
	// requestTimeout bounds reading a request, its body included, so a
	// client that stops sending cannot hold a connection. It does not apply
	// to a WebSocket once upgraded.
	requestTimeout = 10 * time.Second
	// responseTimeout bounds handling a request and writing its response, so
	// a client that stops reading cannot hold a connection either. It does
	// not apply to a WebSocket once upgraded.
	responseTimeout = 10 * time.Second
	// maxUnauthenticated bounds the pairing requests that may be in progress
	// at once; more are refused with 503. /health is not counted: it reads
	// no body and answers at once.
	maxUnauthenticated = 8
	// maxConnsPerIP bounds the connections one address may hold open at
	// once; more are closed before TLS. A paired client needs one WebSocket,
	// a second one while it reconnects, and a few short requests, some kept
	// open for reuse.
	maxConnsPerIP = 16
)

// Control message types. Protocol.kt mirrors them.
const (
	MsgHello            = "hello"
	MsgSession          = "session"
	MsgApps             = "apps"
	MsgLaunch           = "launch"
	MsgClipboard        = "clipboard"
	MsgClipboardRequest = "clipboard_request"
	MsgQuality          = "quality"
	// MsgScale changes the client's own screen's scale during a session.
	MsgScale = "scale"
	// MsgKeepAwake sets how long the computer stays awake after the session.
	MsgKeepAwake = "keep_awake"
	// MsgLockAfter sets whether the desk locks when the session ends.
	MsgLockAfter = "lock_after"
	// MsgPrimary makes the client's own screen GNOME's primary monitor, or
	// gives the primary back to the desk.
	MsgPrimary = "primary"
	// MsgDesk turns the desk's monitors off during an own-screen session, so
	// every window moves to the client's screen, or on again.
	MsgDesk   = "desk"
	MsgReqIDR = "req_idr"
	MsgError  = "error"
)

// Message is the JSON envelope of every WebSocket message.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Hello is the client's first message. With Auto, Width, Height and FPS are
// the top of the quality ladder and Start, when set, is the level the client
// last streamed at on this network, by size and rate.
type Hello struct {
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	FPS    int    `json:"fps"`
	QP     int    `json:"qp"`
	Auto   bool   `json:"auto,omitempty"`
	Start  *Level `json:"start,omitempty"`
	// Audio asks for the desktop's sound as Opus packets.
	Audio bool `json:"audio,omitempty"`
	// Scale is the client's own screen's scale, 2 when absent.
	Scale float64 `json:"scale,omitempty"`
	// Screen chooses what the client sees: ScreenOwn, ScreenMirror, or
	// ScreenAuto (also when absent): its own screen when the desk is locked,
	// otherwise a mirrored monitor.
	Screen string `json:"screen,omitempty"`
	// KeepAwake is how long the computer stays awake after the session
	// ends: KeepAwakeOff (also when absent), minutes, or KeepAwakeUntilReturn.
	KeepAwake int `json:"keep_awake,omitempty"`
	// LockAfter locks the desk when the session ends, also when it was
	// unlocked as the client came.
	LockAfter bool `json:"lock_after,omitempty"`
}

// KeepAwake values besides a number of minutes.
const (
	KeepAwakeOff         = 0
	KeepAwakeUntilReturn = -1
	// KeepAwakeMaxMinutes is the longest timed keep-awake, a day.
	KeepAwakeMaxMinutes = 24 * 60
)

// Values of Hello.Screen and SessionInfo.Screen.
const (
	ScreenAuto   = "auto"
	ScreenOwn    = "own"
	ScreenMirror = "mirror"
)

// Level is one rung of the auto-quality ladder.
type Level struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	FPS    int `json:"fps"`
}

// SessionInfo tells the client how to reach and decrypt the media plane.
type SessionInfo struct {
	SessionID uint32 `json:"session_id"`
	UDPPort   int    `json:"udp_port"`
	HostNonce []byte `json:"host_nonce"`
	Codec     string `json:"codec"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	FPS       int    `json:"fps"`
	QP        int    `json:"qp"`
	// Display describes how the desktop was matched, for the HUD.
	Display string `json:"display"`
	// Auto, Level and Levels describe the quality ladder; Level indexes Levels.
	Auto   bool    `json:"auto"`
	Level  int     `json:"level"`
	Levels []Level `json:"levels"`
	// Audio is set when the session sends the desktop's sound.
	Audio bool `json:"audio,omitempty"`
	// Screen is ScreenOwn for the client's own virtual screen, ScreenMirror
	// for a mirrored monitor.
	Screen string `json:"screen,omitempty"`
	// Scale is the own screen's scale as applied (Mutter rounds to the
	// scales it supports); zero when mirroring.
	Scale float64 `json:"scale,omitempty"`
	// KeepAwake is the session's keep-awake setting, as in Hello.
	KeepAwake int `json:"keep_awake,omitempty"`
	// LockAfter is the session's lock-after setting, as in Hello.
	LockAfter bool `json:"lock_after,omitempty"`
	// Primary says whether the client's own screen is GNOME's primary
	// monitor, the one with the top bar and the overview. It is present, true
	// or false, whenever the session can move the primary (MsgPrimary): with
	// its own screen in the layout beside another monitor that is on. Clients
	// offer the choice only then, since hosts before it had MsgPrimary never
	// send it.
	Primary *bool `json:"primary,omitempty"`
	// DeskOff says whether the desk's monitors are off. It is present, true
	// or false, whenever the client can switch them (MsgDesk): with the
	// session's own screen in the layout, unless the session found the desk
	// locked, which keeps them off until it ends.
	DeskOff *bool `json:"desk_off,omitempty"`
}

// DeskChange asks for the desk's monitors to be off, or on again.
type DeskChange struct {
	Off bool `json:"off"`
}

// PrimaryChange asks for the client's own screen to be the primary monitor,
// or for the desk to have the primary again.
type PrimaryChange struct {
	Primary bool `json:"primary"`
}

// LockAfterChange sets whether the desk locks when the session ends.
type LockAfterChange struct {
	Lock bool `json:"lock"`
}

// KeepAwakeChange changes how long the computer stays awake after the
// session.
type KeepAwakeChange struct {
	Minutes int `json:"minutes"`
}

// ScaleChange asks for another scale of the client's own screen.
type ScaleChange struct {
	Scale float64 `json:"scale"`
}

// Launch asks the host to start an application from the app list.
type Launch struct {
	ID   string `json:"id,omitempty"`
	Exec string `json:"exec"`
}

// Clipboard carries UTF-8 clipboard text in either direction.
type Clipboard struct {
	Text string `json:"text"`
}

// Quality changes the stream size or quantizer during a session.
type Quality struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	QP     int `json:"qp"`
}

// ErrorInfo is sent before the host closes a session it cannot serve.
type ErrorInfo struct {
	Message string `json:"message"`
	// Reason names an error that clients act on, such as ReasonReplaced;
	// empty for the others.
	Reason string `json:"reason,omitempty"`
}

// ReasonReplaced ends a session that another device took over. Its client
// must not reconnect by itself: that would take the desk back, and two
// devices that both reconnect would take it from each other in turn.
const ReasonReplaced = "replaced"

// Health is the unauthenticated /health response. The client uses Time to
// stamp bearer tokens in host time, and MAC for Wake-on-LAN.
type Health struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// UDPPort is the media port, for UDP probes before a session (probe.go);
	// hosts before it leave it out.
	UDPPort int `json:"udp_port,omitempty"`
	// Protocol is ControlProtocolVersion; hosts before it existed leave it out,
	// which clients read as 1.
	Protocol int    `json:"protocol,omitempty"`
	Hostname string `json:"hostname"`
	TimeMs   int64  `json:"time_ms"`
	MAC      string `json:"mac,omitempty"`
	// Addresses are every address clients may reach the host on (each
	// LAN and VPN interface), so a client that paired on one network finds
	// the host on another.
	Addresses []string `json:"addresses,omitempty"`
}

// ControlProtocolVersion numbers the set of control messages and fields this
// build speaks (the UDP header's ProtocolVersion is separate).
// Clients compare it with their own to tell the user which side to update:
// a host or client that is older lacks some features, which only hide.
// 1 is every release up to 0.28.0.
const ControlProtocolVersion = 2

// Pairing request phases.
const (
	PairPhaseStart  = "start"
	PairPhaseFinish = "finish"
)

// PairRequest is the body of POST /pair.
type PairRequest struct {
	Phase     string `json:"phase"`
	ClientPub []byte `json:"client_pub,omitempty"`
	PairingID string `json:"pairing_id,omitempty"`
	Proof     []byte `json:"proof,omitempty"`
	Name      string `json:"name,omitempty"`
}

// PairResponse is the reply to POST /pair.
type PairResponse struct {
	PairingID string `json:"pairing_id,omitempty"`
	HostPub   []byte `json:"host_pub,omitempty"`
	PeerID    string `json:"peer_id,omitempty"`
	Proof     []byte `json:"proof,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PeerRegistry stores paired peers.
type PeerRegistry interface {
	Get(id string) (config.Peer, bool)
	Add(p config.Peer) error
}

// SessionHandler serves one authenticated WebSocket connection until it
// returns; the control server then closes the connection.
type SessionHandler interface {
	ServeSession(ctx context.Context, conn *ControlConn)
}

// ControlServer is the HTTPS and WebSocket front end.
type ControlServer struct {
	Addr     string
	TLS      *tls.Config
	Peers    PeerRegistry
	Pairer   *crypto.Pairer
	Sessions SessionHandler
	Allow    func(netip.Addr) bool
	Health   func() Health
	// Forget removes a paired peer at its own request (DELETE /pair), ending
	// its session if it has one.
	Forget func(peerID string) error

	server *http.Server
	// readTimeout, writeTimeout and connsPerIP replace requestTimeout,
	// responseTimeout and maxConnsPerIP in tests.
	readTimeout  time.Duration
	writeTimeout time.Duration
	connsPerIP   int
	// unauthenticated holds a slot for each pairing request in progress.
	unauthenticated chan struct{}
	// listener counts the open connections per address.
	listener *filteredListener
}

// Serve listens on Addr (IPv4) and serves until ctx ends. Connections from
// addresses that Allow rejects, and those beyond maxConnsPerIP open from one
// address, are closed right after accept, before TLS. onListen receives the
// bound address.
func (s *ControlServer) Serve(ctx context.Context, onListen func(stdnet.Addr)) error {
	if s.readTimeout == 0 {
		s.readTimeout = requestTimeout
	}
	if s.writeTimeout == 0 {
		s.writeTimeout = responseTimeout
	}
	if s.connsPerIP == 0 {
		s.connsPerIP = maxConnsPerIP
	}
	s.unauthenticated = make(chan struct{}, maxUnauthenticated)
	ln, err := stdnet.Listen("tcp4", s.Addr)
	if err != nil {
		return fmt.Errorf("listen tcp %s: %w", s.Addr, err)
	}
	s.listener = &filteredListener{Listener: ln, allow: s.Allow, perIP: s.connsPerIP, open: map[netip.Addr]int{}}
	onListen(ln.Addr())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /pair", s.limited(s.handlePair))
	mux.HandleFunc("DELETE /pair", s.handleForget)
	mux.HandleFunc("GET /ws", s.handleWS)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: requestTimeout,
		ReadTimeout:       s.readTimeout,
		WriteTimeout:      s.writeTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext:       func(stdnet.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdown); err != nil {
			slog.Debug("shut down control server", "err", err)
		}
	}()
	err = s.server.Serve(tls.NewListener(s.listener, s.TLS))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve control plane: %w", err)
}

// limited runs h unless maxUnauthenticated pairing requests are in progress
// already, and otherwise answers 503 at once.
func (s *ControlServer) limited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.unauthenticated <- struct{}{}:
			defer func() { <-s.unauthenticated }()
			h(w, r)
		default:
			slog.Debug("refuse request: too many without a token", "path", r.URL.Path, "from", r.RemoteAddr)
			refuseUnread(w, http.StatusServiceUnavailable, "too many requests at once; try again")
		}
	}
}

// refuseUnread answers with an error and closes the connection afterwards,
// so the server does not first wait for a request body nobody will read.
func refuseUnread(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Connection", "close")
	writeJSON(w, status, PairResponse{Error: msg})
}

func (s *ControlServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Health())
}

// pairingClosed is why pairing requests fail now, or nil while a PIN is
// valid; the errors are the ones Pairer.Start and Finish return.
func pairingClosed(state crypto.PairingState) error {
	switch state {
	case crypto.PairingWaiting:
		return nil
	case crypto.PairingExpired:
		return crypto.ErrPairingExpired
	case crypto.PairingLocked:
		return crypto.ErrTooManyAttempts
	default:
		return crypto.ErrNoPairing
	}
}

func (s *ControlServer) handlePair(w http.ResponseWriter, r *http.Request) {
	state, _ := s.Pairer.State()
	if err := pairingClosed(state); err != nil {
		refuseUnread(w, pairStatus(err), err.Error())
		return
	}
	var req PairRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPairBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, PairResponse{Error: "malformed request"})
		return
	}
	switch req.Phase {
	case PairPhaseStart:
		id, hostPub, err := s.Pairer.Start(req.ClientPub)
		if err != nil {
			slog.Warn("pairing start rejected", "from", r.RemoteAddr, "err", err)
			writeJSON(w, pairStatus(err), PairResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, PairResponse{PairingID: id, HostPub: hostPub})
	case PairPhaseFinish:
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = "Android device"
		}
		peerKey, hostProof, err := s.Pairer.Finish(req.PairingID, req.Proof, name)
		if err != nil {
			slog.Warn("pairing finish rejected", "from", r.RemoteAddr, "err", err)
			writeJSON(w, pairStatus(err), PairResponse{Error: err.Error()})
			return
		}
		peerID, err := crypto.NewPeerID()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, PairResponse{Error: "internal error"})
			return
		}
		if err := s.Peers.Add(config.Peer{ID: peerID, Name: name, Key: peerKey, Paired: time.Now()}); err != nil {
			slog.Error("store paired peer", "err", err)
			writeJSON(w, http.StatusInternalServerError, PairResponse{Error: "could not store peer"})
			return
		}
		slog.Warn("paired new device", "peer", peerID, "name", name, "from", r.RemoteAddr)
		writeJSON(w, http.StatusOK, PairResponse{PeerID: peerID, Proof: hostProof})
	default:
		writeJSON(w, http.StatusBadRequest, PairResponse{Error: "unknown phase"})
	}
}

func pairStatus(err error) int {
	switch {
	case errors.Is(err, crypto.ErrBadProof):
		return http.StatusUnauthorized
	case errors.Is(err, crypto.ErrTooManyAttempts):
		return http.StatusTooManyRequests
	case errors.Is(err, crypto.ErrNoPairing), errors.Is(err, crypto.ErrPairingExpired), errors.Is(err, crypto.ErrUnknownPairing):
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

// authenticate checks the request's bearer token and returns its peer. On
// failure it has answered 401.
func (s *ControlServer) authenticate(w http.ResponseWriter, r *http.Request) (config.Peer, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return config.Peer{}, false
	}
	peerID, err := crypto.VerifyAuthToken(token, func(id string) ([]byte, bool) {
		p, ok := s.Peers.Get(id)
		return p.Key, ok
	}, time.Now())
	if err != nil {
		slog.Warn("reject control request", "path", r.URL.Path, "from", r.RemoteAddr, "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return config.Peer{}, false
	}
	peer, ok := s.Peers.Get(peerID)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
	return peer, ok
}

// handleForget removes the requesting peer's pairing: a client that deletes
// a host asks it to forget the client too.
func (s *ControlServer) handleForget(w http.ResponseWriter, r *http.Request) {
	peer, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if s.Forget == nil {
		http.Error(w, "forgetting is not supported", http.StatusNotImplemented)
		return
	}
	if err := s.Forget(peer.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("peer asked to be forgotten", "peer", peer.Name, "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]string{"forgotten": peer.ID})
}

func (s *ControlServer) handleWS(w http.ResponseWriter, r *http.Request) {
	peer, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "bad remote address", http.StatusBadRequest)
		return
	}
	// The server's read and write timeouts must not end the session; clear
	// them here instead of relying on net/http to clear them when hijacking.
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		slog.Debug("clear the read deadline for the websocket", "err", err)
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		slog.Debug("clear the write deadline for the websocket", "err", err)
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Warn("websocket upgrade", "from", r.RemoteAddr, "err", err)
		return
	}
	ws.SetReadLimit(MaxControlMessage)
	conn := &ControlConn{ws: ws, Peer: peer, RemoteIP: ap.Addr().Unmap()}
	s.Sessions.ServeSession(r.Context(), conn)
	if err := ws.CloseNow(); err != nil {
		slog.Log(r.Context(), LevelTrace, "close websocket", "err", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write JSON response", "err", err)
	}
}

// ControlConn is an authenticated WebSocket connection.
type ControlConn struct {
	ws       *websocket.Conn
	writeMu  sync.Mutex
	Peer     config.Peer
	RemoteIP netip.Addr
}

// Send writes one message.
func (c *ControlConn) Send(ctx context.Context, msgType string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", msgType, err)
	}
	data, err := json.Marshal(Message{Type: msgType, Payload: raw})
	if err != nil {
		return fmt.Errorf("encode %s message: %w", msgType, err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("send %s: %w", msgType, err)
	}
	return nil
}

// Receive reads the next message.
func (c *ControlConn) Receive(ctx context.Context) (Message, error) {
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return Message{}, fmt.Errorf("read control message: %w", err)
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return Message{}, fmt.Errorf("decode control message: %w", err)
	}
	return m, nil
}

// IsNormalClose reports whether err is the peer closing the WebSocket
// normally, as opposed to a broken connection.
func IsNormalClose(err error) bool {
	s := websocket.CloseStatus(err)
	return s == websocket.StatusNormalClosure || s == websocket.StatusGoingAway
}

// Close closes the connection with a normal status and a reason.
func (c *ControlConn) Close(reason string) error {
	if err := c.ws.Close(websocket.StatusNormalClosure, reason); err != nil {
		return fmt.Errorf("close websocket: %w", err)
	}
	return nil
}

// filteredListener accepts connections only from allowed addresses, and at
// most perIP open at once from each.
type filteredListener struct {
	stdnet.Listener
	allow func(netip.Addr) bool
	perIP int

	mu   sync.Mutex
	open map[netip.Addr]int
}

func (l *filteredListener) Accept() (stdnet.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
		switch ip := ap.Addr().Unmap(); {
		case err != nil || !l.allow(ip):
			slog.Debug("drop TCP connection from disallowed address", "from", c.RemoteAddr())
		case !l.take(ip):
			slog.Debug("drop TCP connection: too many open from this address", "from", c.RemoteAddr(), "limit", l.perIP)
		default:
			return &countedConn{Conn: c, done: func() { l.release(ip) }}, nil
		}
		if err := c.Close(); err != nil {
			slog.Log(context.Background(), LevelTrace, "close dropped connection", "err", err)
		}
	}
}

// take counts a new connection from ip, unless ip has perIP open already.
func (l *filteredListener) take(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[ip] >= l.perIP {
		return false
	}
	l.open[ip]++
	return true
}

func (l *filteredListener) release(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[ip]--; l.open[ip] <= 0 {
		delete(l.open, ip)
	}
}

// openConns is how many connections ip has open.
func (l *filteredListener) openConns(ip netip.Addr) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open[ip]
}

// countedConn gives its slot back when it is closed, whether by the HTTP
// server or, for a WebSocket, by the session's end.
type countedConn struct {
	stdnet.Conn
	once sync.Once
	done func()
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.done)
	return err
}

// AllowList returns a filter that accepts addresses inside the prefixes.
func AllowList(prefixes []netip.Prefix) func(netip.Addr) bool {
	return func(a netip.Addr) bool {
		a = a.Unmap()
		for _, p := range prefixes {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
}

// LoadOrCreateCert loads the TLS certificate from certPath and keyPath, or
// creates a self-signed ECDSA P-256 certificate there on first start. It
// returns a TLS 1.3-only server config and the certificate fingerprint.
func LoadOrCreateCert(certPath, keyPath string) (*tls.Config, string, error) {
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := createCert(certPath, keyPath); err != nil {
			return nil, "", err
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, "", fmt.Errorf("load TLS certificate: %w", err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
	return cfg, crypto.Fingerprint(cert.Certificate[0]), nil
}

func createCert(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return fmt.Errorf("generate certificate serial: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		host = "desklinkd"
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "desklinkd " + host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode TLS key: %w", err)
	}
	if err := config.WriteFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return fmt.Errorf("write TLS key: %w", err)
	}
	if err := config.WriteFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return fmt.Errorf("write TLS certificate: %w", err)
	}
	return nil
}
