package net

import (
	"context"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	stdnet "net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"desklink/internal/crypto"
)

const (
	// KeepaliveInterval is how often each side sends a keepalive.
	KeepaliveInterval = 2 * time.Second
	socketBufferBytes = 4 << 20
	// pacingBurst datagrams of a frame go out back to back, then the sender
	// pauses pacingGap: about 20 KB per quarter millisecond, so a 255 KB
	// keyframe leaves in about 3 ms instead of one burst.
	pacingBurst      = 16
	pacingGap        = 250 * time.Microsecond
	replayWindowSize = 1024
)

// ErrSequenceExhausted ends a session whose 32-bit sequence counter would
// wrap and so repeat a nonce; the client reconnects with fresh keys.
var ErrSequenceExhausted = errors.New("media sequence numbers exhausted")

// MediaHandler receives the decrypted client traffic of one session.
type MediaHandler interface {
	HandleInput(seq uint32, events []InputEvent)
	HandleTelemetry(t Telemetry)
	HandleRTT(rtt time.Duration)
}

// MediaServer owns the UDP socket and routes datagrams to sessions by the
// session ID in the cleartext header.
type MediaServer struct {
	conn  *stdnet.UDPConn
	allow func(netip.Addr) bool

	mu       sync.RWMutex
	sessions map[uint32]*MediaSession
}

// ListenMedia opens the IPv4 UDP socket. Datagrams from addresses that allow
// rejects are dropped before any parsing.
func ListenMedia(addr string, allow func(netip.Addr) bool) (*MediaServer, error) {
	ua, err := stdnet.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve media address %s: %w", addr, err)
	}
	conn, err := stdnet.ListenUDP("udp4", ua)
	if err != nil {
		return nil, fmt.Errorf("listen udp %s: %w", addr, err)
	}
	if err := enablePktinfo(conn); err != nil {
		slog.Debug("enable IP_PKTINFO; replies use the routing source address", "err", err)
	}
	if err := conn.SetWriteBuffer(socketBufferBytes); err != nil {
		slog.Debug("set UDP write buffer", "err", err)
	}
	if err := conn.SetReadBuffer(socketBufferBytes); err != nil {
		slog.Debug("set UDP read buffer", "err", err)
	}
	return &MediaServer{conn: conn, allow: allow, sessions: map[uint32]*MediaSession{}}, nil
}

// Addr is the bound address.
func (s *MediaServer) Addr() stdnet.Addr { return s.conn.LocalAddr() }

// Port is the bound UDP port.
func (s *MediaServer) Port() int { return s.conn.LocalAddr().(*stdnet.UDPAddr).Port }

// Close closes the socket and ends Serve.
func (s *MediaServer) Close() error { return s.conn.Close() }

// Serve reads datagrams until the socket is closed.
func (s *MediaServer) Serve(ctx context.Context) error {
	buf := make([]byte, 2048)
	oob := make([]byte, 128)
	for {
		n, oobn, _, from, err := s.conn.ReadMsgUDPAddrPort(buf, oob)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, stdnet.ErrClosed) {
				return nil
			}
			slog.Warn("read UDP", "err", err)
			continue
		}
		addr := from.Addr().Unmap()
		if !s.allow(addr) {
			slog.Log(ctx, LevelTrace, "drop UDP from disallowed address", "from", from)
			continue
		}
		h, err := ParseHeader(buf[:n])
		if err != nil {
			slog.Log(ctx, LevelTrace, "drop malformed UDP", "from", from, "err", err)
			continue
		}
		s.mu.RLock()
		sess := s.sessions[h.SessionID]
		s.mu.RUnlock()
		if sess == nil {
			slog.Log(ctx, LevelTrace, "drop UDP for unknown session", "from", from, "session", h.SessionID)
			continue
		}
		sess.receive(netip.AddrPortFrom(addr, from.Port()), parseArrival(oob[:oobn]), buf[:n])
	}
}

// Open registers a session. Only datagrams that decrypt with the
// client-to-host key and come from peerIP are accepted; the first one also
// tells the session where to send video.
func (s *MediaServer) Open(id uint32, keys crypto.SessionKeys, peerIP netip.Addr, h MediaHandler) (*MediaSession, error) {
	send, err := crypto.NewAEAD(keys.HostToClient)
	if err != nil {
		return nil, err
	}
	recv, err := crypto.NewAEAD(keys.ClientToHost)
	if err != nil {
		return nil, err
	}
	m := &MediaSession{id: id, server: s, send: send, recv: recv, peerIP: peerIP.Unmap(), handler: h}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[id]; exists {
		return nil, fmt.Errorf("media session %d already open", id)
	}
	s.sessions[id] = m
	return m, nil
}

func (s *MediaServer) remove(id uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// MediaStats are counters for the session log.
type MediaStats struct {
	FramesSent  uint64
	BytesSent   uint64
	PacketsSent uint64
}

// MediaSession is the UDP side of one remote session.
type MediaSession struct {
	id      uint32
	server  *MediaServer
	send    cipher.AEAD
	recv    cipher.AEAD
	peerIP  netip.Addr
	handler MediaHandler

	// frameMu serialises SendFrame, so frames do not interleave.
	frameMu sync.Mutex

	mu     sync.Mutex
	remote netip.AddrPort
	// arrival is where the client's datagrams arrive; sends go out the same way.
	arrival arrival
	seq     uint32
	frameID uint16
	// audioCounter numbers audio packets so clients drop late ones.
	audioCounter uint16
	buf          []byte
	payload      []byte
	replay       replayWindow

	framesSent, bytesSent, packetsSent atomic.Uint64
}

// ID is the session ID carried in every header.
func (m *MediaSession) ID() uint32 { return m.id }

// Stats returns the send counters.
func (m *MediaSession) Stats() MediaStats {
	return MediaStats{FramesSent: m.framesSent.Load(), BytesSent: m.bytesSent.Load(), PacketsSent: m.packetsSent.Load()}
}

// Close unregisters the session.
func (m *MediaSession) Close() { m.server.remove(m.id) }

// Connected reports whether the client's UDP address is known yet.
func (m *MediaSession) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.remote.IsValid()
}

func (m *MediaSession) nextSeqLocked() (uint32, error) {
	if m.seq == math.MaxUint32 {
		return 0, ErrSequenceExhausted
	}
	m.seq++
	return m.seq, nil
}

// SendFrame fragments, encrypts and sends one encoded frame. Frames produced
// before the client's first datagram arrives are dropped; the client asks for
// a keyframe once it starts receiving.
//
// Large frames go out in paced bursts. Each burst is numbered and sent under
// mu, so sequence numbers leave in order (clients count a gap as loss), and
// the pauses between bursts are outside it, so received input, audio and
// keepalives never wait for a whole keyframe. frameMu keeps frames whole.
func (m *MediaSession) SendFrame(frame []byte, keyframe bool, captured time.Time) error {
	m.frameMu.Lock()
	defer m.frameMu.Unlock()
	packets, err := m.framePackets(frame, keyframe, captured)
	if err != nil || len(packets) == 0 {
		return err
	}
	for start := 0; start < len(packets); start += pacingBurst {
		// Pace large frames: a keyframe's ~200 datagrams sent back to back
		// can overflow a Wi-Fi access point's or VPN's queue, losing the
		// frame and costing another keyframe. Small frames go out at once.
		if start > 0 {
			time.Sleep(pacingGap)
		}
		if err := m.sendBurst(packets[start:min(start+pacingBurst, len(packets))]); err != nil {
			return err
		}
	}
	m.framesSent.Add(1)
	return nil
}

// sealable is a video fragment or a parity packet.
type sealable interface {
	Header(sessionID, seq uint32) Header
	AppendPayload(dst []byte) []byte
}

// framePackets fragments one frame and adds its parity after the data, so a
// loss burst inside the frame does not also take the packets that repair it.
// It returns nothing before the client's first datagram.
func (m *MediaSession) framePackets(frame []byte, keyframe bool, captured time.Time) ([]sealable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.remote.IsValid() {
		return nil, nil
	}
	frags, err := FragmentFrame(m.frameID, frame, keyframe, uint64(captured.UnixMicro()))
	if err != nil {
		return nil, err
	}
	m.frameID++
	parity := ParityFor(frags)
	packets := make([]sealable, 0, len(frags)+len(parity))
	for _, f := range frags {
		packets = append(packets, f)
	}
	for _, p := range parity {
		packets = append(packets, p)
	}
	return packets, nil
}

// sendBurst numbers, encrypts and sends packets back to back.
func (m *MediaSession) sendBurst(packets []sealable) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range packets {
		seq, err := m.nextSeqLocked()
		if err != nil {
			return err
		}
		m.payload = p.AppendPayload(m.payload[:0])
		m.buf, err = Seal(m.send, p.Header(m.id, seq), m.payload, m.buf[:0])
		if err != nil {
			return err
		}
		if _, err := m.writeLocked(m.buf); err != nil {
			return fmt.Errorf("send video: %w", err)
		}
		m.packetsSent.Add(1)
		m.bytesSent.Add(uint64(len(m.buf)))
	}
	return nil
}

// writeLocked sends one datagram to the client. The caller holds mu.
func (m *MediaSession) writeLocked(b []byte) (int, error) {
	return m.server.write(b, m.remote, m.arrival)
}

// write sends one datagram to a client from the address and interface its
// datagrams arrive on.
func (s *MediaServer) write(b []byte, to netip.AddrPort, at arrival) (int, error) {
	if oob := at.oob(); oob != nil {
		n, _, err := s.conn.WriteMsgUDPAddrPort(b, oob, to)
		return n, err
	}
	return s.conn.WriteToUDPAddrPort(b, to)
}

// SendAudio encrypts and sends one Opus frame. Like video, audio produced
// before the client's first datagram is dropped.
func (m *MediaSession) SendAudio(frame []byte, captured time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.remote.IsValid() {
		return nil
	}
	if AudioPrefixSize+len(frame) > MaxPlaintextSize {
		return fmt.Errorf("%w: Opus frame of %d bytes", ErrTooLarge, len(frame))
	}
	seq, err := m.nextSeqLocked()
	if err != nil {
		return err
	}
	a := AudioPacket{Counter: m.audioCounter, CaptureMicros: uint64(captured.UnixMicro()), Data: frame}
	m.audioCounter++
	m.payload = a.AppendPayload(m.payload[:0])
	m.buf, err = Seal(m.send, a.Header(m.id, seq), m.payload, m.buf[:0])
	if err != nil {
		return err
	}
	if _, err := m.writeLocked(m.buf); err != nil {
		return fmt.Errorf("send audio: %w", err)
	}
	m.packetsSent.Add(1)
	m.bytesSent.Add(uint64(len(m.buf)))
	return nil
}

func (m *MediaSession) sendKeepalive(timestampMicros uint64, echo bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.remote.IsValid() {
		return nil
	}
	seq, err := m.nextSeqLocked()
	if err != nil {
		return err
	}
	h := Header{Type: PacketKeepalive, SessionID: m.id, Seq: seq}
	if echo {
		h.FragIdx = KeepaliveEcho
	}
	m.buf, err = Seal(m.send, h, binary.BigEndian.AppendUint64(nil, timestampMicros), m.buf[:0])
	if err != nil {
		return err
	}
	if _, err := m.writeLocked(m.buf); err != nil {
		return fmt.Errorf("send keepalive: %w", err)
	}
	return nil
}

// RunKeepalive sends a keepalive every KeepaliveInterval until ctx ends.
func (m *MediaSession) RunKeepalive(ctx context.Context) error {
	t := time.NewTicker(KeepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			if err := m.sendKeepalive(uint64(now.UnixMicro()), false); err != nil {
				return err
			}
		}
	}
}

func (m *MediaSession) receive(from netip.AddrPort, at arrival, datagram []byte) {
	if from.Addr() != m.peerIP {
		slog.Log(context.Background(), LevelTrace, "drop UDP from address other than the control connection", "from", from, "want", m.peerIP)
		return
	}
	h, pt, err := Open(m.recv, datagram)
	if err != nil {
		slog.Log(context.Background(), LevelTrace, "drop undecryptable UDP", "from", from, "err", err)
		return
	}
	m.mu.Lock()
	fresh := m.replay.accept(h.Seq)
	if fresh {
		m.remote, m.arrival = from, at
	}
	m.mu.Unlock()
	if !fresh {
		slog.Log(context.Background(), LevelTrace, "drop replayed UDP", "seq", h.Seq)
		return
	}
	switch h.Type {
	case PacketInputEvent:
		events, err := ParseInputEvents(pt)
		if err != nil {
			slog.Debug("bad input packet", "err", err)
			return
		}
		m.handler.HandleInput(h.Seq, events)
	case PacketTelemetryAck:
		t, err := ParseTelemetry(pt)
		if err != nil {
			slog.Debug("bad telemetry packet", "err", err)
			return
		}
		m.handler.HandleTelemetry(t)
	case PacketKeepalive:
		ts, echo, err := ParseKeepalive(h, pt)
		if err != nil {
			slog.Debug("bad keepalive packet", "err", err)
			return
		}
		if echo {
			if sent := time.UnixMicro(int64(ts)); time.Since(sent) >= 0 {
				m.handler.HandleRTT(time.Since(sent))
			}
			return
		}
		if err := m.sendKeepalive(ts, true); err != nil {
			slog.Debug("echo keepalive", "err", err)
		}
	default:
		slog.Debug("unexpected packet type from client", "type", h.Type)
	}
}

// replayWindow accepts each sequence number once and rejects numbers more
// than replayWindowSize behind the newest one seen.
type replayWindow struct {
	top  uint32
	seen [replayWindowSize / 64]uint64
}

func (w *replayWindow) bit(seq uint32) (word int, mask uint64) {
	i := seq % replayWindowSize
	return int(i / 64), 1 << (i % 64)
}

func (w *replayWindow) accept(seq uint32) bool {
	if seq == 0 {
		return false
	}
	if seq > w.top {
		if seq-w.top >= replayWindowSize {
			w.seen = [replayWindowSize / 64]uint64{}
		} else {
			for s := w.top + 1; s < seq; s++ {
				word, mask := w.bit(s)
				w.seen[word] &^= mask
			}
		}
		w.top = seq
		word, mask := w.bit(seq)
		w.seen[word] |= mask
		return true
	}
	if w.top-seq >= replayWindowSize {
		return false
	}
	word, mask := w.bit(seq)
	if w.seen[word]&mask != 0 {
		return false
	}
	w.seen[word] |= mask
	return true
}
