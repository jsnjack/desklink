package net

import (
	"bytes"
	"context"
	"crypto/cipher"
	"encoding/binary"
	stdnet "net"
	"net/netip"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"desklink/internal/crypto"
)

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

type recordingHandler struct {
	input     chan []InputEvent
	telemetry chan Telemetry
	rtt       chan time.Duration
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{input: make(chan []InputEvent, 8), telemetry: make(chan Telemetry, 8), rtt: make(chan time.Duration, 8)}
}

func (h *recordingHandler) HandleInput(_ uint32, e []InputEvent) { h.input <- e }
func (h *recordingHandler) HandleTelemetry(t Telemetry)          { h.telemetry <- t }
func (h *recordingHandler) HandleRTT(d time.Duration)            { h.rtt <- d }

func wait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
	var zero T
	return zero
}

func TestMediaSessionLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := ListenMedia("127.0.0.1:0", func(netip.Addr) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := srv.Close(); err != nil {
			t.Log(err)
		}
	}()
	go func() {
		if err := srv.Serve(ctx); err != nil {
			t.Log(err)
		}
	}()
	keys := testKeys(t)
	h := newRecordingHandler()
	sess, err := srv.Open(77, keys, netip.MustParseAddr("127.0.0.1"), h)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if _, err := srv.Open(77, keys, netip.MustParseAddr("127.0.0.1"), h); err == nil {
		t.Error("opened a duplicate session ID")
	}

	conn, err := stdnet.DialUDP("udp4", nil, srv.Addr().(*stdnet.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Log(err)
		}
	}()
	c2h, err := crypto.NewAEAD(keys.ClientToHost)
	if err != nil {
		t.Fatal(err)
	}
	h2c, err := crypto.NewAEAD(keys.HostToClient)
	if err != nil {
		t.Fatal(err)
	}
	send := func(h Header, payload []byte) []byte {
		t.Helper()
		dg, err := Seal(c2h, h, payload, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(dg); err != nil {
			t.Fatal(err)
		}
		return dg
	}
	recv := func(aead cipher.AEAD) (Header, []byte) {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		if n > MaxDatagramSize {
			t.Fatalf("datagram of %d bytes", n)
		}
		hdr, pt, err := Open(aead, buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		return hdr, pt
	}

	t.Run("frames before the client's first packet are dropped", func(t *testing.T) {
		if err := sess.SendFrame([]byte("early"), true, time.Now()); err != nil {
			t.Fatal(err)
		}
		if sess.Connected() {
			t.Error("session connected without a client packet")
		}
	})
	t.Run("keepalive is echoed with its timestamp", func(t *testing.T) {
		send(Header{Type: PacketKeepalive, SessionID: 77, Seq: 1}, binary.BigEndian.AppendUint64(nil, 123456))
		hdr, pt := recv(h2c)
		ts, echo, err := ParseKeepalive(hdr, pt)
		if err != nil || !echo || ts != 123456 {
			t.Fatalf("echo %v ts %d err %v", echo, ts, err)
		}
	})
	// readFrame reads one frame's datagrams: its fragments' payloads in
	// order and its parity packets.
	readFrame := func(t *testing.T) ([][]byte, []ParityPacket) {
		t.Helper()
		var payloads [][]byte
		var parity []ParityPacket
		want := -1
		for want < 0 || len(parity) < want {
			hdr, pt := recv(h2c)
			switch hdr.Type {
			case PacketVideoFrag:
				payloads = append(payloads, append([]byte(nil), pt...))
				f, err := ParseVideoFragment(hdr, pt)
				if err != nil {
					t.Fatal(err)
				}
				want = FECStride(int(f.Count))
			case PacketFEC:
				p, err := ParseParity(hdr, pt)
				if err != nil {
					t.Fatal(err)
				}
				parity = append(parity, p)
			}
		}
		return payloads, parity
	}
	t.Run("a frame arrives in fragments that reassemble", func(t *testing.T) {
		frame := bytes.Repeat([]byte("0123456789"), 500)
		if err := sess.SendFrame(frame, true, time.Now()); err != nil {
			t.Fatal(err)
		}
		payloads, parity := readFrame(t)
		var got []byte
		for _, pt := range payloads {
			got = append(got, pt[VideoPrefixSize:]...)
		}
		if !bytes.Equal(got, frame) {
			t.Error("reassembled frame differs")
		}
		// 5 fragments and one parity packet over all of them.
		if st := sess.Stats(); st.FramesSent != 1 || st.PacketsSent != 6 || len(parity) != 1 {
			t.Errorf("stats %+v, %d parity packets", st, len(parity))
		}
		// Any one lost fragment comes back from the others and the parity.
		lost := 2
		var present [][]byte
		for i, pt := range payloads {
			if i != lost {
				present = append(present, pt)
			}
		}
		if rebuilt := parity[0].Recover(present); !bytes.Equal(rebuilt, payloads[lost]) {
			t.Error("parity did not rebuild the lost fragment")
		}
	})
	t.Run("a large frame is paced and still reassembles", func(t *testing.T) {
		frame := bytes.Repeat([]byte("abcdefghij"), 5000) // 50 KB: 41 fragments, 6 parity
		start := time.Now()
		if err := sess.SendFrame(frame, true, time.Now()); err != nil {
			t.Fatal(err)
		}
		// Two pauses between three groups of 16.
		if d := time.Since(start); d < 2*pacingGap {
			t.Errorf("sent in %v, want at least %v of pacing", d, 2*pacingGap)
		}
		payloads, parity := readFrame(t)
		var got []byte
		for _, pt := range payloads {
			got = append(got, pt[VideoPrefixSize:]...)
		}
		if !bytes.Equal(got, frame) {
			t.Error("paced frame differs after reassembly")
		}
		if len(parity) != FECStride(len(payloads)) {
			t.Errorf("%d parity packets for %d fragments", len(parity), len(payloads))
		}
	})
	t.Run("input reaches the handler once", func(t *testing.T) {
		dg := send(Header{Type: PacketInputEvent, SessionID: 77, Seq: 2}, InputEvent{Kind: InputKey, Flags: 1, Code: 30}.AppendTo(nil))
		events := wait(t, h.input)
		if len(events) != 1 || events[0].Code != 30 {
			t.Fatalf("events %+v", events)
		}
		if _, err := conn.Write(dg); err != nil {
			t.Fatal(err)
		}
		send(Header{Type: PacketTelemetryAck, SessionID: 77, Seq: 3}, Telemetry{NeedIDR: true}.AppendTo(nil))
		if tel := wait(t, h.telemetry); !tel.NeedIDR {
			t.Error("telemetry lost NeedIDR")
		}
		select {
		case e := <-h.input:
			t.Errorf("replayed input delivered: %+v", e)
		default:
		}
	})
	t.Run("packets for another session are ignored", func(t *testing.T) {
		send(Header{Type: PacketInputEvent, SessionID: 78, Seq: 4}, InputEvent{Kind: InputKey, Code: 31}.AppendTo(nil))
		send(Header{Type: PacketTelemetryAck, SessionID: 77, Seq: 5}, Telemetry{}.AppendTo(nil))
		wait(t, h.telemetry)
		select {
		case e := <-h.input:
			t.Errorf("foreign session input delivered: %+v", e)
		default:
		}
	})
}

func TestArrivalFromPktinfo(t *testing.T) {
	info := unix.Inet4Pktinfo{Ifindex: 7, Spec_dst: [4]byte{192, 168, 1, 7}, Addr: [4]byte{192, 168, 1, 139}}
	raw := (*[unsafe.Sizeof(info)]byte)(unsafe.Pointer(&info))[:]
	oob := cmsg(unix.IPPROTO_IP, unix.IP_PKTINFO, raw)
	a := parseArrival(oob)
	if a.ifindex != 7 || a.local != netip.MustParseAddr("192.168.1.139") {
		t.Fatalf("parseArrival = %+v, want interface 7 and the datagram's destination 192.168.1.139", a)
	}
	if a.oob() == nil {
		t.Error("a known arrival must produce a control message")
	}
	if (arrival{}).oob() != nil {
		t.Error("an unknown arrival must send without a control message")
	}
}

func cmsg(level, typ int32, data []byte) []byte {
	b := make([]byte, unix.CmsgSpace(len(data)))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&b[0]))
	h.Level, h.Type = level, typ
	h.SetLen(unix.CmsgLen(len(data)))
	copy(b[unix.CmsgLen(0):], data)
	return b
}
