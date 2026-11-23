package client

import (
	"bytes"
	"context"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	stdnet "net"
	"reflect"
	"strings"
	"testing"
	"time"

	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

// The same cases as TestReplayWindow in internal/net, so the client drops
// repeated datagrams exactly as the host does.
func TestReplayWindow(t *testing.T) {
	cases := []struct {
		name string
		seqs []uint32
		want []bool
	}{
		{"in order", []uint32{1, 2, 3}, []bool{true, true, true}},
		{"duplicate", []uint32{1, 2, 2}, []bool{true, true, false}},
		{"reordered within the window", []uint32{1, 5, 3, 4, 2}, []bool{true, true, true, true, true}},
		{"reordered duplicate", []uint32{5, 3, 3}, []bool{true, true, false}},
		{"older than the window", []uint32{2000, 2000 - replayWindowSize}, []bool{true, false}},
		{"jump clears the window", []uint32{1, 1 + 3*replayWindowSize, 1 + 3*replayWindowSize - 1}, []bool{true, true, true}},
		{"zero is never valid", []uint32{0}, []bool{false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var w replayWindow
			for i, s := range c.seqs {
				if got := w.accept(s); got != c.want[i] {
					t.Errorf("seq %d: got %v, want %v", s, got, c.want[i])
				}
			}
		})
	}
}

func TestMediaWatch(t *testing.T) {
	timing := mediaTiming{firstWait: 3 * time.Second, quietAfter: 5 * time.Second, timeout: 10 * time.Second}
	type step struct {
		at   time.Duration // after the transport started
		seen bool          // a datagram arrives; otherwise the run loop checks
		want mediaEvent
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"the first datagram makes the path ready, later ones change nothing", []step{
			{1 * time.Second, true, mediaReady}, {2 * time.Second, true, mediaNone}, {6 * time.Second, false, mediaNone},
		}},
		{"no datagram is reported quiet once after firstWait and lost at timeout", []step{
			{2900 * time.Millisecond, false, mediaNone}, {3 * time.Second, false, mediaQuiet}, {5 * time.Second, false, mediaNone},
			{10 * time.Second, false, mediaLost},
		}},
		{"a first datagram after the quiet report makes the path ready", []step{
			{3 * time.Second, false, mediaQuiet}, {4 * time.Second, true, mediaReady}, {8 * time.Second, false, mediaNone},
			{9 * time.Second, false, mediaQuiet}, {14 * time.Second, false, mediaLost},
		}},
		{"datagrams resuming after a quiet spell make the path ready again", []step{
			{0, true, mediaReady}, {5 * time.Second, false, mediaQuiet}, {6 * time.Second, true, mediaReady},
			{10 * time.Second, false, mediaNone},
		}},
		{"silence after the path was up ends at timeout from the last datagram", []step{
			{1 * time.Second, true, mediaReady}, {6 * time.Second, false, mediaQuiet}, {10900 * time.Millisecond, false, mediaNone},
			{11 * time.Second, false, mediaLost},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Unix(1_700_000_000, 0)
			w := newMediaWatch(timing, start)
			for i, s := range c.steps {
				var got mediaEvent
				if s.seen {
					got = w.seen(start.Add(s.at))
				} else {
					got = w.check(start.Add(s.at))
				}
				if got != s.want {
					t.Errorf("step %d at %v: got %d, want %d", i, s.at, got, s.want)
				}
			}
		})
	}
}

// hostEnd is the host side of a transport under test: a local UDP socket
// that receives what the client sends, and the keys of both directions.
type hostEnd struct {
	conn *stdnet.UDPConn
	seal cipher.AEAD // host to client
	open cipher.AEAD // client to host
	seq  uint32
}

// datagram seals a host datagram with the next sequence number.
func (h *hostEnd) datagram(t *testing.T, hd dlnet.Header, payload []byte) []byte {
	t.Helper()
	h.seq++
	return h.datagramAt(t, h.seq, hd, payload)
}

func (h *hostEnd) datagramAt(t *testing.T, seq uint32, hd dlnet.Header, payload []byte) []byte {
	t.Helper()
	hd.SessionID, hd.Seq = 42, seq
	dg, err := dlnet.Seal(h.seal, hd, payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	return dg
}

func keepaliveDatagram(t *testing.T, h *hostEnd, ts time.Time, echo bool) []byte {
	t.Helper()
	hd := dlnet.Header{Type: dlnet.PacketKeepalive}
	if echo {
		hd.FragIdx = dlnet.KeepaliveEcho
	}
	return h.datagram(t, hd, binary.BigEndian.AppendUint64(nil, uint64(ts.UnixMicro())))
}

type mediaRecord struct {
	frames []EncodedFrame
	events []mediaEvent
}

// newTestTransport builds a transport for session 42 that sends to a local
// socket; the test feeds it datagrams through handle.
func newTestTransport(t *testing.T) (*transport, *mediaRecord, *hostEnd) {
	t.Helper()
	conn, err := stdnet.ListenUDP("udp4", &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Log(err)
		}
	})
	keys, err := crypto.DeriveSessionKeys(bytes.Repeat([]byte{1}, crypto.KeySize), 42, fakeNonce)
	if err != nil {
		t.Fatal(err)
	}
	rec := &mediaRecord{}
	tr, err := newTransport("127.0.0.1", conn.LocalAddr().(*stdnet.UDPAddr).Port, 42, keys, defaultMediaTiming, mediaHandlers{
		frame: func(f EncodedFrame) { rec.frames = append(rec.frames, f) },
		state: func(e mediaEvent) { rec.events = append(rec.events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.close)
	seal, err := crypto.NewAEAD(keys.HostToClient)
	if err != nil {
		t.Fatal(err)
	}
	open, err := crypto.NewAEAD(keys.ClientToHost)
	if err != nil {
		t.Fatal(err)
	}
	return tr, rec, &hostEnd{conn: conn, seal: seal, open: open}
}

func TestTransportDropsRepeatedDatagrams(t *testing.T) {
	ctx := context.Background()
	t.Run("a repeated video fragment makes one frame and counts its bytes once", func(t *testing.T) {
		tr, rec, h := newTestTransport(t)
		now := time.Now()
		f := dlnet.VideoFragment{FrameID: 1, Index: 0, Count: 1, Keyframe: true, Data: []byte("key")}
		dg := h.datagram(t, f.Header(0, 0), f.AppendPayload(nil))
		tr.handle(ctx, dg, now)
		tr.handle(ctx, dg, now)
		if len(rec.frames) != 1 || tr.bytes.Load() != uint64(len(dg)) {
			t.Errorf("%d frames, %d bytes; want 1 frame, %d bytes", len(rec.frames), tr.bytes.Load(), len(dg))
		}
	})
	t.Run("a repeated datagram does not hide a lost one", func(t *testing.T) {
		tr, _, h := newTestTransport(t)
		now := time.Now()
		echo := func(seq uint32) []byte {
			return h.datagramAt(t, seq, dlnet.Header{Type: dlnet.PacketKeepalive, FragIdx: dlnet.KeepaliveEcho},
				binary.BigEndian.AppendUint64(nil, uint64(now.UnixMicro())))
		}
		for _, seq := range []uint32{1, 2, 2, 4} { // 3 is lost
			tr.handle(ctx, echo(seq), now)
		}
		if got := tr.loss.Sample(); got != 2500 {
			t.Errorf("loss %d, want 2500 (one of four)", got)
		}
	})
	t.Run("a replayed keepalive echo does not change the round trip", func(t *testing.T) {
		tr, _, h := newTestTransport(t)
		sent := time.UnixMicro(time.Now().UnixMicro()) // the wire carries microseconds
		dg := keepaliveDatagram(t, h, sent, true)
		tr.handle(ctx, dg, sent.Add(10*time.Millisecond))
		tr.handle(ctx, dg, sent.Add(900*time.Millisecond))
		if got := time.Duration(tr.rtt.Load()); got != 10*time.Millisecond || len(tr.clock.rtts) != 1 {
			t.Errorf("round trip %v from %d samples, want 10ms from 1", got, len(tr.clock.rtts))
		}
	})
	t.Run("a replayed host keepalive counts once for the clock", func(t *testing.T) {
		tr, _, h := newTestTransport(t)
		sent := time.UnixMicro(time.Now().UnixMicro()) // the wire carries microseconds
		dg := keepaliveDatagram(t, h, sent, false)
		tr.handle(ctx, dg, sent.Add(5*time.Millisecond))
		tr.handle(ctx, dg, sent.Add(50*time.Millisecond))
		if len(tr.clock.gaps) != 1 || tr.clock.gaps[0] != 5*time.Millisecond {
			t.Errorf("clock samples %v, want [5ms]", tr.clock.gaps)
		}
	})
	t.Run("a datagram too far behind the newest is dropped", func(t *testing.T) {
		tr, _, h := newTestTransport(t)
		now := time.Now()
		payload := binary.BigEndian.AppendUint64(nil, uint64(now.UnixMicro()))
		hd := dlnet.Header{Type: dlnet.PacketKeepalive, FragIdx: dlnet.KeepaliveEcho}
		newest := h.datagramAt(t, 2000, hd, payload)
		tr.handle(ctx, newest, now)
		tr.handle(ctx, h.datagramAt(t, 2000-replayWindowSize, hd, payload), now)
		if got := tr.bytes.Load(); got != uint64(len(newest)) {
			t.Errorf("%d bytes counted, want only the newest datagram's %d", got, len(newest))
		}
	})
	t.Run("a replayed datagram neither readies the path nor keeps it alive", func(t *testing.T) {
		tr, rec, h := newTestTransport(t)
		start := time.Now()
		tr.watch = newMediaWatch(defaultMediaTiming, start)
		dg := keepaliveDatagram(t, h, start, true)
		tr.handle(ctx, dg, start)
		quietAt := start.Add(defaultMediaTiming.quietAfter)
		if e := tr.watch.check(quietAt); e != mediaQuiet {
			t.Fatalf("check after quietAfter: %d, want mediaQuiet", e)
		}
		tr.handle(ctx, dg, quietAt.Add(time.Second))
		if want := []mediaEvent{mediaReady}; !reflect.DeepEqual(rec.events, want) {
			t.Errorf("events %v, want %v", rec.events, want)
		}
		if e := tr.watch.check(start.Add(defaultMediaTiming.timeout)); e != mediaLost {
			t.Errorf("check at timeout: %d, want mediaLost", e)
		}
	})
	t.Run("a datagram that fails authentication is ignored", func(t *testing.T) {
		tr, rec, h := newTestTransport(t)
		dg := keepaliveDatagram(t, h, time.Now(), true)
		dg[len(dg)-1] ^= 1
		tr.handle(ctx, dg, time.Now())
		if len(rec.events) != 0 || tr.bytes.Load() != 0 {
			t.Errorf("events %v, %d bytes from a forged datagram", rec.events, tr.bytes.Load())
		}
	})
}

// A session whose host never sends a UDP datagram reports the path quiet,
// then closes with ErrMediaLost and releases the keys it held.
func TestSilentMediaPathEndsTheSession(t *testing.T) {
	silent, err := stdnet.ListenUDP("udp4", &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := silent.Close(); err != nil {
			t.Log(err)
		}
	}()
	h := startFakeHostWith(t, silent)
	ip, port := h.addr.Addr().String(), int(h.addr.Port())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pin, _, err := h.pairer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Pair(ctx, ip, port, pin, h.fp, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	p := Profile{Name: "Desk", IP: ip, Port: port, PeerID: res.PeerID, PeerKey: res.PeerKey, Fingerprint: res.Fingerprint}
	ev := newRecordedEvents()
	timing := mediaTiming{firstWait: 200 * time.Millisecond, quietAfter: 300 * time.Millisecond, timeout: 800 * time.Millisecond, check: 20 * time.Millisecond}
	s, err := connect(ctx, p, Request{Codec: "av1"}, func(EncodedFrame) {}, ev, timing)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wait(t, ev.info)
	s.SendInput(dlnet.InputEvent{Kind: dlnet.InputKey, Flags: 1, Code: 30})

	if msg := wait(t, ev.quiet); !strings.Contains(msg, "UDP port") {
		t.Errorf("quiet message %q does not name the UDP port", msg)
	}
	if err := wait(t, ev.closed); !errors.Is(err, ErrMediaLost) {
		t.Fatalf("closed with %v, want ErrMediaLost", err)
	}
	select {
	case <-ev.ready:
		t.Error("the path was reported ready without a host datagram")
	default:
	}

	keys, err := crypto.DeriveSessionKeys(res.PeerKey, 42, fakeNonce)
	if err != nil {
		t.Fatal(err)
	}
	open, err := crypto.NewAEAD(keys.ClientToHost)
	if err != nil {
		t.Fatal(err)
	}
	if err := silent.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	for {
		n, err := silent.Read(buf)
		if err != nil {
			t.Fatalf("no release of the held key reached the host: %v", err)
		}
		hd, pt, err := dlnet.Open(open, buf[:n])
		if err != nil || hd.Type != dlnet.PacketInputEvent {
			continue
		}
		events, err := dlnet.ParseInputEvents(pt)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Kind == dlnet.InputKey && e.Code == 30 && e.Flags == 0 {
				return
			}
		}
	}
}
