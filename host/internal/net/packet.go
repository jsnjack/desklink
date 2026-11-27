package net

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
)

// Wire constants of the UDP media plane. Protocol.kt mirrors them.
const (
	// Magic is "DL" in the first two header bytes.
	Magic uint16 = 0x444C
	// ProtocolVersion is the header version byte.
	ProtocolVersion = 0x01
	// HeaderSize is the cleartext header, also the AEAD additional data.
	HeaderSize = 16
	// MaxDatagramSize keeps datagrams unfragmented inside a 1420-byte
	// WireGuard tunnel with room for IPv6 and UDP headers.
	MaxDatagramSize = 1280
	// TagSize is the Poly1305 tag length.
	TagSize = 16
	// MaxPlaintextSize is the largest encrypted payload.
	MaxPlaintextSize = MaxDatagramSize - HeaderSize - TagSize
	// VideoPrefixSize is the encrypted prefix of every video fragment.
	VideoPrefixSize = 12
	// MaxFragmentData is the frame data carried by one fragment.
	// It leaves FECPrefixSize spare, so a parity packet over full video
	// payloads still fits one datagram.
	MaxFragmentData = MaxPlaintextSize - VideoPrefixSize - FECPrefixSize
	// FECPrefixSize is a parity packet's group, stride, fragment count and
	// XOR of payload lengths.
	FECPrefixSize = 8
	// FECGroupSize is how many fragments one parity packet covers at most.
	FECGroupSize = 8
	// MaxFragments is the fragment limit of the 16-bit fragment index.
	MaxFragments = 0xFFFF
	// InputEventSize is the length of one encoded input event.
	InputEventSize = 12
	// TelemetrySize is the length of a telemetry payload.
	TelemetrySize = 5
	// KeepaliveSize is the length of a keepalive payload.
	KeepaliveSize = 8
)

// PacketType is header byte 3.
type PacketType uint8

// Packet types.
const (
	PacketVideoFrag    PacketType = 0x01
	PacketInputEvent   PacketType = 0x02
	PacketTelemetryAck PacketType = 0x03
	PacketKeepalive    PacketType = 0x04
	// PacketAudio carries one Opus frame; FrameID counts audio frames.
	PacketAudio PacketType = 0x05
	// PacketFEC carries the XOR of a group of one frame's video payloads,
	// so a client rebuilds one lost fragment of the group.
	PacketFEC PacketType = 0x06
	// PacketMicrophone carries one Opus frame of the client's microphone,
	// laid out like PacketAudio; FrameID counts microphone frames.
	PacketMicrophone PacketType = 0x07
	// PacketTiles carries which exact tiles a video frame changed, sent
	// before and after the frame's fragments (tiles.go,
	// AppendTilePacket); FrameID is the frame's.
	PacketTiles PacketType = 0x08
	// PacketProbe checks the UDP path without a session (probe.go).
	PacketProbe PacketType = 0x09
)

// AudioPrefixSize is the capture timestamp before each Opus frame.
const AudioPrefixSize = 8

// Header is the 16-byte big-endian cleartext header.
type Header struct {
	Type      PacketType
	SessionID uint32
	Seq       uint32
	FrameID   uint16
	FragIdx   uint8
	FragCnt   uint8
}

// Errors returned by the codec.
var (
	ErrShortPacket = errors.New("packet too short")
	ErrBadMagic    = errors.New("bad magic or version")
	ErrTooLarge    = errors.New("payload exceeds datagram limit")
)

func (h Header) put(b []byte) {
	binary.BigEndian.PutUint16(b[0:], Magic)
	b[2] = ProtocolVersion
	b[3] = byte(h.Type)
	binary.BigEndian.PutUint32(b[4:], h.SessionID)
	binary.BigEndian.PutUint32(b[8:], h.Seq)
	binary.BigEndian.PutUint16(b[12:], h.FrameID)
	b[14] = h.FragIdx
	b[15] = h.FragCnt
}

// ParseHeader decodes and checks the cleartext header.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, ErrShortPacket
	}
	if binary.BigEndian.Uint16(b[0:]) != Magic || b[2] != ProtocolVersion {
		return Header{}, ErrBadMagic
	}
	return Header{
		Type:      PacketType(b[3]),
		SessionID: binary.BigEndian.Uint32(b[4:]),
		Seq:       binary.BigEndian.Uint32(b[8:]),
		FrameID:   binary.BigEndian.Uint16(b[12:]),
		FragIdx:   b[14],
		FragCnt:   b[15],
	}, nil
}

// nonce places header bytes 4..11 (session ID and sequence number) in the
// last eight bytes of the 12-byte ChaCha20-Poly1305 nonce.
func nonce(h Header) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint32(n[4:], h.SessionID)
	binary.BigEndian.PutUint32(n[8:], h.Seq)
	return n
}

// Seal encodes the header, encrypts the payload with the header as additional
// data and appends the datagram to dst.
func Seal(aead cipher.AEAD, h Header, payload, dst []byte) ([]byte, error) {
	if len(payload) > MaxPlaintextSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(payload))
	}
	start := len(dst)
	dst = append(dst, make([]byte, HeaderSize)...)
	h.put(dst[start:])
	return aead.Seal(dst, nonce(h), payload, dst[start:start+HeaderSize]), nil
}

// Open checks and decrypts a datagram.
func Open(aead cipher.AEAD, datagram []byte) (Header, []byte, error) {
	h, err := ParseHeader(datagram)
	if err != nil {
		return Header{}, nil, err
	}
	if len(datagram) > MaxDatagramSize || len(datagram) < HeaderSize+TagSize {
		return Header{}, nil, fmt.Errorf("datagram length %d: %w", len(datagram), ErrShortPacket)
	}
	pt, err := aead.Open(nil, nonce(h), datagram[HeaderSize:], datagram[:HeaderSize])
	if err != nil {
		return Header{}, nil, fmt.Errorf("decrypt: %w", err)
	}
	return h, pt, nil
}

// VideoFragment is one slice of an encoded frame. The header carries the low
// bytes of the fragment index and count; the encrypted prefix carries the high
// bytes, so a 2560x1600 keyframe can exceed 255 fragments.
//
// Prefix layout: flags (1), index high (1), count high (1), reserved (1),
// capture time in Unix microseconds (8).
type VideoFragment struct {
	FrameID       uint16
	Index         uint16
	Count         uint16
	Keyframe      bool
	CaptureMicros uint64
	Data          []byte
}

const videoFlagKeyframe = 0x01

// FragmentFrame splits an encoded frame into fragments that each fit one datagram.
func FragmentFrame(frameID uint16, frame []byte, keyframe bool, captureMicros uint64) ([]VideoFragment, error) {
	if len(frame) == 0 {
		return nil, errors.New("empty frame")
	}
	count := (len(frame) + MaxFragmentData - 1) / MaxFragmentData
	if count > MaxFragments {
		return nil, fmt.Errorf("%w: frame of %d bytes needs %d fragments", ErrTooLarge, len(frame), count)
	}
	out := make([]VideoFragment, 0, count)
	for i := 0; i < count; i++ {
		end := min((i+1)*MaxFragmentData, len(frame))
		out = append(out, VideoFragment{
			FrameID:       frameID,
			Index:         uint16(i),
			Count:         uint16(count),
			Keyframe:      keyframe,
			CaptureMicros: captureMicros,
			Data:          frame[i*MaxFragmentData : end],
		})
	}
	return out, nil
}

// Header returns the datagram header of this fragment.
func (f VideoFragment) Header(sessionID, seq uint32) Header {
	return Header{
		Type:      PacketVideoFrag,
		SessionID: sessionID,
		Seq:       seq,
		FrameID:   f.FrameID,
		FragIdx:   uint8(f.Index),
		FragCnt:   uint8(f.Count),
	}
}

// AppendPayload appends the prefix and data to dst.
func (f VideoFragment) AppendPayload(dst []byte) []byte {
	var flags byte
	if f.Keyframe {
		flags |= videoFlagKeyframe
	}
	dst = append(dst, flags, byte(f.Index>>8), byte(f.Count>>8), 0)
	dst = binary.BigEndian.AppendUint64(dst, f.CaptureMicros)
	return append(dst, f.Data...)
}

// ParityPacket is the XOR of the video payloads (prefix and data) of the
// fragments Group, Group+Stride, Group+2*Stride, ... of frame FrameID, which
// has Count fragments, and the XOR of their payload lengths. Groups are
// strided, not consecutive, so a burst of up to Stride consecutive losses
// hits each group at most once and every lost fragment can be rebuilt.
type ParityPacket struct {
	FrameID uint16
	Group   uint16
	Stride  uint16
	Count   uint16
	LenXor  uint16
	Data    []byte
}

// FECStride is the number of parity packets for a frame of count fragments:
// one per FECGroupSize fragments, rounded up.
func FECStride(count int) int { return (count + FECGroupSize - 1) / FECGroupSize }

// ParityFor computes the parity packets of a fragmented frame.
func ParityFor(frags []VideoFragment) []ParityPacket {
	if len(frags) == 0 {
		return nil
	}
	stride := FECStride(len(frags))
	out := make([]ParityPacket, stride)
	for g := range out {
		out[g] = ParityPacket{FrameID: frags[0].FrameID, Group: uint16(g), Stride: uint16(stride), Count: uint16(len(frags))}
	}
	var payload []byte
	for i, f := range frags {
		payload = f.AppendPayload(payload[:0])
		p := &out[i%stride]
		p.Data = xorInto(p.Data, payload)
		p.LenXor ^= uint16(len(payload))
	}
	return out
}

// Members are the fragment indices the packet covers.
func (p ParityPacket) Members() []int {
	var out []int
	for i := int(p.Group); p.Stride > 0 && i < int(p.Count); i += int(p.Stride) {
		out = append(out, i)
	}
	return out
}

// Recover returns the payload of the one member missing from present, the
// payloads of all the other members; nil when they do not add up.
func (p ParityPacket) Recover(present [][]byte) []byte {
	buf := append([]byte(nil), p.Data...)
	length := p.LenXor
	for _, q := range present {
		buf = xorInto(buf, q)
		length ^= uint16(len(q))
	}
	if int(length) > len(buf) {
		return nil
	}
	return buf[:length]
}

// Header returns the datagram header of this parity packet.
func (p ParityPacket) Header(sessionID, seq uint32) Header {
	return Header{Type: PacketFEC, SessionID: sessionID, Seq: seq, FrameID: p.FrameID}
}

// AppendPayload appends the prefix and the parity bytes to dst.
func (p ParityPacket) AppendPayload(dst []byte) []byte {
	for _, v := range []uint16{p.Group, p.Stride, p.Count, p.LenXor} {
		dst = binary.BigEndian.AppendUint16(dst, v)
	}
	return append(dst, p.Data...)
}

// ParseParity decodes a decrypted parity payload.
func ParseParity(h Header, payload []byte) (ParityPacket, error) {
	if len(payload) < FECPrefixSize {
		return ParityPacket{}, ErrShortPacket
	}
	p := ParityPacket{
		FrameID: h.FrameID,
		Group:   binary.BigEndian.Uint16(payload[0:]),
		Stride:  binary.BigEndian.Uint16(payload[2:]),
		Count:   binary.BigEndian.Uint16(payload[4:]),
		LenXor:  binary.BigEndian.Uint16(payload[6:]),
		Data:    payload[FECPrefixSize:],
	}
	if p.Stride == 0 || p.Group >= p.Stride || p.Count == 0 {
		return ParityPacket{}, fmt.Errorf("bad parity group %d/%d of %d", p.Group, p.Stride, p.Count)
	}
	return p, nil
}

// xorInto xors src into dst, extending dst with zeros as needed.
func xorInto(dst, src []byte) []byte {
	for len(dst) < len(src) {
		dst = append(dst, 0)
	}
	for i, b := range src {
		dst[i] ^= b
	}
	return dst
}

// AudioPacket is one Opus frame and the time it was captured, in
// microseconds of the sender's wall clock: the host's for the desktop's
// sound, like video frames, the client's for its microphone.
type AudioPacket struct {
	Counter       uint16
	CaptureMicros uint64
	Data          []byte
}

// Header returns the datagram header of this audio packet.
func (a AudioPacket) Header(sessionID, seq uint32) Header {
	return Header{Type: PacketAudio, SessionID: sessionID, Seq: seq, FrameID: a.Counter}
}

// MicrophoneHeader returns the header of this packet sent from the client's
// microphone; ParseAudioPacket decodes its payload too.
func (a AudioPacket) MicrophoneHeader(sessionID, seq uint32) Header {
	return Header{Type: PacketMicrophone, SessionID: sessionID, Seq: seq, FrameID: a.Counter}
}

// AppendPayload appends the timestamp and the Opus frame to dst.
func (a AudioPacket) AppendPayload(dst []byte) []byte {
	dst = binary.BigEndian.AppendUint64(dst, a.CaptureMicros)
	return append(dst, a.Data...)
}

// ParseAudioPacket decodes a decrypted audio payload.
func ParseAudioPacket(h Header, payload []byte) (AudioPacket, error) {
	if len(payload) <= AudioPrefixSize {
		return AudioPacket{}, ErrShortPacket
	}
	return AudioPacket{
		Counter:       h.FrameID,
		CaptureMicros: binary.BigEndian.Uint64(payload),
		Data:          payload[AudioPrefixSize:],
	}, nil
}

// ParseVideoFragment decodes a decrypted video payload.
func ParseVideoFragment(h Header, payload []byte) (VideoFragment, error) {
	if len(payload) < VideoPrefixSize {
		return VideoFragment{}, ErrShortPacket
	}
	f := VideoFragment{
		FrameID:       h.FrameID,
		Index:         uint16(payload[1])<<8 | uint16(h.FragIdx),
		Count:         uint16(payload[2])<<8 | uint16(h.FragCnt),
		Keyframe:      payload[0]&videoFlagKeyframe != 0,
		CaptureMicros: binary.BigEndian.Uint64(payload[4:]),
		Data:          payload[VideoPrefixSize:],
	}
	if f.Count == 0 || f.Index >= f.Count {
		return VideoFragment{}, fmt.Errorf("fragment %d of %d is out of range", f.Index, f.Count)
	}
	return f, nil
}

// InputKind is the first byte of an input event.
type InputKind uint8

// Input event kinds.
const (
	InputKey     InputKind = 1
	InputRelMove InputKind = 2
	// InputAbsMove is the pen at X, Y in 0..65535. Flags: 0 hover, 1
	// touching, 2 left proximity. Code is the pressure, 1..65535 while
	// touching, or 0 when the client does not report it (full pressure).
	InputAbsMove     InputKind = 3
	InputMouseButton InputKind = 4
	InputScroll      InputKind = 5
	// InputAbsPointer places the mouse pointer at X, Y in 0..65535, for
	// windowed clients whose local cursor maps onto the desktop.
	InputAbsPointer InputKind = 6
	// InputHeld records list what the client holds down: Flags 1 key (Code
	// KEY_*), 2 mouse button (Code 1..5), 3 pen (Code bit 0 touching, bit 1
	// in range), or one record with Flags 0 for nothing. A packet of them is a
	// complete snapshot; the host lifts anything else it holds, which repairs
	// lost key-up packets.
	InputHeld InputKind = 7
	// InputPenTilt gives the tilt of the pen record that follows it: X towards
	// +x and Y towards +y, in hundredths of a degree (-9000..9000). Hosts
	// that predate it ignore it.
	InputPenTilt InputKind = 8
	// InputScrollPx scrolls like a touchpad: X and Y are how far the
	// client's fingers moved the content, in 1/ScrollPxUnit pixels of the
	// client's screen (positive X moves it right, positive Y down). Flags
	// ScrollPxFinished says the fingers lifted, so apps may let the scroll
	// run on. Only to hosts whose session has scroll_px.
	InputScrollPx InputKind = 9
)

// ScrollPxUnit is how many units of an InputScrollPx record make a pixel.
const ScrollPxUnit = 256

// ScrollPxFinished is the Flags bit of the InputScrollPx record that ends a
// scroll.
const ScrollPxFinished = 1

// InputEvent is the 12-byte client input record. An INPUT_EVENT payload holds
// one or more records back to back.
type InputEvent struct {
	Kind  InputKind
	Flags uint8
	Code  uint16
	X     int32
	Y     int32
}

// AppendTo appends the encoded event to dst.
func (e InputEvent) AppendTo(dst []byte) []byte {
	dst = append(dst, byte(e.Kind), e.Flags)
	dst = binary.BigEndian.AppendUint16(dst, e.Code)
	dst = binary.BigEndian.AppendUint32(dst, uint32(e.X))
	return binary.BigEndian.AppendUint32(dst, uint32(e.Y))
}

// ParseInputEvents decodes every record in an INPUT_EVENT payload.
func ParseInputEvents(payload []byte) ([]InputEvent, error) {
	if len(payload) == 0 || len(payload)%InputEventSize != 0 {
		return nil, fmt.Errorf("input payload of %d bytes is not a multiple of %d", len(payload), InputEventSize)
	}
	out := make([]InputEvent, 0, len(payload)/InputEventSize)
	for b := payload; len(b) > 0; b = b[InputEventSize:] {
		out = append(out, InputEvent{
			Kind:  InputKind(b[0]),
			Flags: b[1],
			Code:  binary.BigEndian.Uint16(b[2:]),
			X:     int32(binary.BigEndian.Uint32(b[4:])),
			Y:     int32(binary.BigEndian.Uint32(b[8:])),
		})
	}
	return out, nil
}

// Telemetry is the client's report, sent every 250 ms.
type Telemetry struct {
	LastContiguousFrameID uint16
	LossPctX100           uint16
	NeedIDR               bool
}

// AppendTo appends the encoded telemetry to dst.
func (t Telemetry) AppendTo(dst []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, t.LastContiguousFrameID)
	dst = binary.BigEndian.AppendUint16(dst, t.LossPctX100)
	var need byte
	if t.NeedIDR {
		need = 1
	}
	return append(dst, need)
}

// ParseTelemetry decodes a TELEMETRY_ACK payload.
func ParseTelemetry(payload []byte) (Telemetry, error) {
	if len(payload) < TelemetrySize {
		return Telemetry{}, ErrShortPacket
	}
	return Telemetry{
		LastContiguousFrameID: binary.BigEndian.Uint16(payload[0:]),
		LossPctX100:           binary.BigEndian.Uint16(payload[2:]),
		NeedIDR:               payload[4] != 0,
	}, nil
}

// KeepaliveEcho in FragIdx marks a keepalive that answers one from the other
// side and carries its timestamp back, so the sender can measure the RTT.
const KeepaliveEcho = 1

// ParseKeepalive decodes a KEEPALIVE payload and reports whether it is an echo.
func ParseKeepalive(h Header, payload []byte) (timestampMicros uint64, echo bool, err error) {
	if len(payload) < KeepaliveSize {
		return 0, false, ErrShortPacket
	}
	return binary.BigEndian.Uint64(payload), h.FragIdx == KeepaliveEcho, nil
}
