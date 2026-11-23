package client

import (
	"context"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	stdnet "net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

const (
	telemetryInterval = 250 * time.Millisecond
	statsInterval     = time.Second
	socketBuffer      = 4 << 20
)

// Stats are the live link numbers for the toolbar.
type Stats struct {
	RTT     time.Duration
	FPS     int
	Mbps    float64
	LossPct float64
	// Delay is the average time from the host capturing a frame to this
	// client showing it, over the last second with frames; 0 until known.
	Delay time.Duration
}

// transport is the UDP side of a session: it reassembles video, reports
// telemetry every 250 ms, exchanges keepalives and sends input.
type transport struct {
	conn  *stdnet.UDPConn
	sid   uint32
	send  cipher.AEAD
	recv  cipher.AEAD
	seq   atomic.Uint32
	asm   *FrameAssembler
	loss  LossMeter
	sink  func(EncodedFrame)
	audio func(dlnet.AudioPacket)
	sendM sync.Mutex
	held  heldInput // guarded by sendM

	bytes, frames atomic.Uint64
	rtt           atomic.Int64
	clock         hostClock
	shown         delayMeter
	statsMu       sync.Mutex
	stats         Stats
}

func newTransport(ip string, port int, sid uint32, keys crypto.SessionKeys, sink func(EncodedFrame), audio func(dlnet.AudioPacket)) (*transport, error) {
	send, err := crypto.NewAEAD(keys.ClientToHost)
	if err != nil {
		return nil, err
	}
	recv, err := crypto.NewAEAD(keys.HostToClient)
	if err != nil {
		return nil, err
	}
	addr, err := stdnet.ResolveUDPAddr("udp4", stdnet.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("resolve media address: %w", err)
	}
	conn, err := stdnet.DialUDP("udp4", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("open UDP socket: %w", err)
	}
	if err := conn.SetReadBuffer(socketBuffer); err != nil {
		slog.Debug("set UDP read buffer", "err", err)
	}
	return &transport{conn: conn, sid: sid, send: send, recv: recv, asm: NewFrameAssembler(), sink: sink, audio: audio}, nil
}

func (t *transport) run(ctx context.Context) {
	go t.receive(ctx)
	t.sendKeepalive(uint64(time.Now().UnixMicro()), false)
	t.sendTelemetry()
	tel := time.NewTicker(telemetryInterval)
	keep := time.NewTicker(dlnet.KeepaliveInterval)
	stats := time.NewTicker(statsInterval)
	defer tel.Stop()
	defer keep.Stop()
	defer stats.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tel.C:
			t.sendTelemetry()
			t.sendHeld()
		case now := <-keep.C:
			t.sendKeepalive(uint64(now.UnixMicro()), false)
		case <-stats.C:
			b, f := t.bytes.Swap(0), t.frames.Swap(0)
			t.statsMu.Lock()
			t.stats.RTT = time.Duration(t.rtt.Load())
			t.stats.FPS = int(f)
			t.stats.Mbps = float64(b) * 8 / 1e6
			if d, ok := t.shown.take(); ok {
				t.stats.Delay = d
			}
			t.statsMu.Unlock()
		}
	}
}

func (t *transport) close() {
	if err := t.conn.Close(); err != nil {
		slog.Debug("close UDP socket", "err", err)
	}
}

// frameShown records that the frame the host captured at captureMicros
// (its clock) is being shown now.
func (t *transport) frameShown(captureMicros uint64) {
	if d, ok := t.clock.since(time.UnixMicro(int64(captureMicros)), time.Now()); ok {
		t.shown.add(d)
	}
}

func (t *transport) currentStats() Stats {
	t.statsMu.Lock()
	defer t.statsMu.Unlock()
	return t.stats
}

func (t *transport) write(h dlnet.Header, payload []byte) {
	t.sendM.Lock()
	defer t.sendM.Unlock()
	t.writeLocked(h, payload)
}

func (t *transport) writeLocked(h dlnet.Header, payload []byte) {
	h.SessionID = t.sid
	h.Seq = t.seq.Add(1)
	dg, err := dlnet.Seal(t.send, h, payload, nil)
	if err != nil {
		slog.Debug("seal datagram", "err", err)
		return
	}
	if _, err := t.conn.Write(dg); err != nil {
		slog.Debug("send datagram", "err", err)
	}
}

func (t *transport) sendInput(events []dlnet.InputEvent) {
	t.sendM.Lock()
	defer t.sendM.Unlock()
	for _, e := range events {
		t.held.observe(e)
	}
	t.writeInputLocked(events)
}

func (t *transport) writeInputLocked(events []dlnet.InputEvent) {
	for len(events) > 0 {
		n := min(len(events), dlnet.MaxPlaintextSize/dlnet.InputEventSize)
		var payload []byte
		for _, e := range events[:n] {
			payload = e.AppendTo(payload)
		}
		t.writeLocked(dlnet.Header{Type: dlnet.PacketInputEvent}, payload)
		events = events[n:]
	}
}

// sendHeld sends the held-input snapshot under the send lock, so it never
// overtakes input queued before it.
func (t *transport) sendHeld() {
	t.sendM.Lock()
	defer t.sendM.Unlock()
	t.writeInputLocked(t.held.snapshot())
}

func (t *transport) sendTelemetry() {
	loss := t.loss.Sample()
	t.statsMu.Lock()
	t.stats.LossPct = float64(loss) / 100
	t.statsMu.Unlock()
	tel := dlnet.Telemetry{LastContiguousFrameID: t.asm.LastContiguous(), LossPctX100: loss, NeedIDR: t.asm.NeedKeyframe()}
	t.write(dlnet.Header{Type: dlnet.PacketTelemetryAck}, tel.AppendTo(nil))
}

func (t *transport) sendKeepalive(ts uint64, echo bool) {
	h := dlnet.Header{Type: dlnet.PacketKeepalive}
	if echo {
		h.FragIdx = dlnet.KeepaliveEcho
	}
	t.write(h, binary.BigEndian.AppendUint64(nil, ts))
}

// requestKeyframe drops frames until a keyframe and asks for one at once.
func (t *transport) requestKeyframe() {
	t.asm.ForceKeyframe()
	t.sendTelemetry()
}

func (t *transport) receive(ctx context.Context) {
	buf := make([]byte, 2048)
	for {
		n, err := t.conn.Read(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, stdnet.ErrClosed) {
				return
			}
			slog.Debug("receive datagram", "err", err)
			continue
		}
		h, pt, err := dlnet.Open(t.recv, buf[:n])
		if err != nil {
			slog.Log(ctx, dlnet.LevelTrace, "drop undecryptable datagram", "err", err)
			continue
		}
		if h.SessionID != t.sid {
			slog.Log(ctx, dlnet.LevelTrace, "drop datagram for another session", "session", h.SessionID)
			continue
		}
		t.bytes.Add(uint64(n))
		t.loss.Observe(h.Seq)
		switch h.Type {
		case dlnet.PacketVideoFrag:
			f, err := dlnet.ParseVideoFragment(h, pt)
			if err != nil {
				slog.Log(ctx, dlnet.LevelTrace, "drop bad video fragment", "err", err)
				continue
			}
			waiting := t.asm.NeedKeyframe()
			for _, frame := range t.asm.Add(f) {
				t.frames.Add(1)
				t.sink(frame)
			}
			if !waiting && t.asm.NeedKeyframe() {
				t.sendTelemetry()
			}
		case dlnet.PacketFEC:
			q, err := dlnet.ParseParity(h, pt)
			if err != nil {
				slog.Log(ctx, dlnet.LevelTrace, "drop bad parity packet", "err", err)
				continue
			}
			waiting := t.asm.NeedKeyframe()
			for _, frame := range t.asm.AddParity(q) {
				t.frames.Add(1)
				t.sink(frame)
			}
			if !waiting && t.asm.NeedKeyframe() {
				t.sendTelemetry()
			}
		case dlnet.PacketAudio:
			a, err := dlnet.ParseAudioPacket(h, pt)
			if err != nil {
				slog.Log(ctx, dlnet.LevelTrace, "drop bad audio packet", "err", err)
				continue
			}
			if t.audio != nil {
				t.audio(a)
			}
		case dlnet.PacketKeepalive:
			ts, echo, err := dlnet.ParseKeepalive(h, pt)
			if err != nil {
				slog.Log(ctx, dlnet.LevelTrace, "drop bad keepalive", "err", err)
				continue
			}
			if echo {
				if d := time.Since(time.UnixMicro(int64(ts))); d >= 0 {
					t.rtt.Store(int64(d))
					t.clock.roundTrip(d)
				}
				continue
			}
			t.clock.keepalive(time.UnixMicro(int64(ts)), time.Now())
			t.sendKeepalive(ts, true)
		}
	}
}
