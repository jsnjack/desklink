package net

import (
	"bytes"
	"encoding/hex"
	"testing"

	"desklink/internal/crypto"
)

func seqBytes(n int, start byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

func testKeys(t *testing.T) crypto.SessionKeys {
	t.Helper()
	k, err := crypto.DeriveSessionKeys(seqBytes(32, 0xA0), 0x01020304, seqBytes(16, 0x30))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// ProtocolTest.kt decodes these datagrams, so the Android client stays
// compatible with the host's encoding.
func TestDatagramsMatchSharedVectors(t *testing.T) {
	keys := testKeys(t)
	h2c, err := crypto.NewAEAD(keys.HostToClient)
	if err != nil {
		t.Fatal(err)
	}
	c2h, err := crypto.NewAEAD(keys.ClientToHost)
	if err != nil {
		t.Fatal(err)
	}
	f := VideoFragment{FrameID: 9, Index: 0x0102, Count: 0x0203, Keyframe: true, CaptureMicros: 0x0102030405060708, Data: []byte("hello")}
	video, err := Seal(h2c, f.Header(0x01020304, 7), f.AppendPayload(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	in := InputEvent{Kind: InputRelMove, X: -5, Y: 300}
	input, err := Seal(c2h, Header{Type: PacketInputEvent, SessionID: 0x01020304, Seq: 1}, in.AppendTo(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	a := AudioPacket{Counter: 0x0102, CaptureMicros: 0x0102030405060708, Data: []byte("opus")}
	audio, err := Seal(h2c, a.Header(0x01020304, 8), a.AppendPayload(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, got, want string }{
		{"audio packet", hex.EncodeToString(audio), "444c0105010203040000000801020000eee798241dac103901c344001b5d32eb21aafd25ebe2f5c8b43652eb"},
		{"video fragment", hex.EncodeToString(video), "444c01010102030400000007000902039d3f934b64d90a4f8f306b8387320d754c027b29287f9f8652654cb535fcaa7369"},
		{"input event", hex.EncodeToString(input), "444c0102010203040000000100000000a8d7d153404684a969382be34dddec2de4f7f39bd5d7fa30d6b4d12c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("got %s, want %s", c.got, c.want)
			}
		})
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{Type: PacketTelemetryAck, SessionID: 0xdeadbeef, Seq: 42, FrameID: 65535, FragIdx: 3, FragCnt: 9}
	b := make([]byte, HeaderSize)
	h.put(b)
	got, err := ParseHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != h {
		t.Errorf("got %+v, want %+v", got, h)
	}
	t.Run("rejects bad magic", func(t *testing.T) {
		b[0] = 0
		if _, err := ParseHeader(b); err == nil {
			t.Error("accepted bad magic")
		}
	})
	t.Run("rejects short packets", func(t *testing.T) {
		if _, err := ParseHeader(b[:10]); err == nil {
			t.Error("accepted 10 bytes")
		}
	})
}

func TestFragmentationRoundTrip(t *testing.T) {
	keys := testKeys(t)
	send, err := crypto.NewAEAD(keys.HostToClient)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		size  int
		count int
	}{
		{"one byte", 1, 1},
		{"exactly one fragment", MaxFragmentData, 1},
		{"one byte over a fragment", MaxFragmentData + 1, 2},
		{"more than 255 fragments", 400_000, (400_000 + MaxFragmentData - 1) / MaxFragmentData},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frame := make([]byte, c.size)
			for i := range frame {
				frame[i] = byte(i * 7)
			}
			frags, err := FragmentFrame(300, frame, true, 99)
			if err != nil {
				t.Fatal(err)
			}
			if len(frags) != c.count {
				t.Fatalf("%d fragments, want %d", len(frags), c.count)
			}
			var rebuilt []byte
			// Decrypt in reverse to show the order comes from the indices.
			parts := make([][]byte, len(frags))
			for i := len(frags) - 1; i >= 0; i-- {
				dg, err := Seal(send, frags[i].Header(1, uint32(i+1)), frags[i].AppendPayload(nil), nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(dg) > MaxDatagramSize {
					t.Fatalf("datagram of %d bytes exceeds %d", len(dg), MaxDatagramSize)
				}
				h, pt, err := Open(send, dg)
				if err != nil {
					t.Fatal(err)
				}
				f, err := ParseVideoFragment(h, pt)
				if err != nil {
					t.Fatal(err)
				}
				if f.Count != uint16(c.count) || !f.Keyframe || f.FrameID != 300 || f.CaptureMicros != 99 {
					t.Fatalf("fragment metadata %+v", f)
				}
				parts[f.Index] = f.Data
			}
			for _, p := range parts {
				rebuilt = append(rebuilt, p...)
			}
			if !bytes.Equal(rebuilt, frame) {
				t.Error("reassembled frame differs")
			}
		})
	}
	t.Run("rejects empty frames", func(t *testing.T) {
		if _, err := FragmentFrame(1, nil, false, 0); err == nil {
			t.Error("accepted an empty frame")
		}
	})
	t.Run("rejects frames above the fragment limit", func(t *testing.T) {
		if _, err := FragmentFrame(1, make([]byte, MaxFragments*MaxFragmentData+1), false, 0); err == nil {
			t.Error("accepted an oversized frame")
		}
	})
}

func TestOpenRejectsTampering(t *testing.T) {
	keys := testKeys(t)
	aead, err := crypto.NewAEAD(keys.ClientToHost)
	if err != nil {
		t.Fatal(err)
	}
	dg, err := Seal(aead, Header{Type: PacketKeepalive, SessionID: 5, Seq: 1}, make([]byte, KeepaliveSize), nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		mut  func([]byte)
	}{
		{"header session ID", func(b []byte) { b[7] ^= 1 }},
		{"header sequence", func(b []byte) { b[11] ^= 1 }},
		{"ciphertext", func(b []byte) { b[HeaderSize] ^= 1 }},
		{"tag", func(b []byte) { b[len(b)-1] ^= 1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := append([]byte(nil), dg...)
			c.mut(b)
			if _, _, err := Open(aead, b); err == nil {
				t.Error("accepted a modified datagram")
			}
		})
	}
	t.Run("payload above the limit", func(t *testing.T) {
		if _, err := Seal(aead, Header{}, make([]byte, MaxPlaintextSize+1), nil); err == nil {
			t.Error("sealed an oversized payload")
		}
	})
}

func TestInputEventsRoundTrip(t *testing.T) {
	events := []InputEvent{
		{Kind: InputKey, Flags: 1, Code: 29},
		{Kind: InputRelMove, X: -12, Y: 7},
		{Kind: InputAbsMove, Flags: 1, X: 65535, Y: 0},
		{Kind: InputScroll, X: 0, Y: -120},
	}
	var payload []byte
	for _, e := range events {
		payload = e.AppendTo(payload)
	}
	got, err := ParseInputEvents(payload)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			if got[i] != events[i] {
				t.Errorf("got %+v, want %+v", got[i], events[i])
			}
		})
	}
	t.Run("rejects partial records", func(t *testing.T) {
		if _, err := ParseInputEvents(payload[:13]); err == nil {
			t.Error("accepted 13 bytes")
		}
	})
}

func TestAudioPacketRoundTrip(t *testing.T) {
	a := AudioPacket{Counter: 65535, CaptureMicros: 123456789, Data: []byte{0xfc, 0xff, 0xfe}}
	got, err := ParseAudioPacket(a.Header(1, 2), a.AppendPayload(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Counter != a.Counter || got.CaptureMicros != a.CaptureMicros || string(got.Data) != string(a.Data) {
		t.Errorf("got %+v, want %+v", got, a)
	}
	if _, err := ParseAudioPacket(a.Header(1, 2), make([]byte, AudioPrefixSize)); err == nil {
		t.Error("an audio payload without an Opus frame was accepted")
	}
}

func TestTelemetryRoundTrip(t *testing.T) {
	want := Telemetry{LastContiguousFrameID: 512, LossPctX100: 250, NeedIDR: true}
	got, err := ParseTelemetry(want.AppendTo(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
