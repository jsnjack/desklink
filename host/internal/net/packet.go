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
	MaxFragmentData = MaxPlaintextSize - VideoPrefixSize
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
)

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
	InputKey         InputKind = 1
	InputRelMove     InputKind = 2
	InputAbsMove     InputKind = 3
	InputMouseButton InputKind = 4
	InputScroll      InputKind = 5
)

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

// keepaliveEcho in FragIdx marks a keepalive that answers one from the other
// side and carries its timestamp back, so the sender can measure the RTT.
const keepaliveEcho = 1

// ParseKeepalive decodes a KEEPALIVE payload and reports whether it is an echo.
func ParseKeepalive(h Header, payload []byte) (timestampMicros uint64, echo bool, err error) {
	if len(payload) < KeepaliveSize {
		return 0, false, ErrShortPacket
	}
	return binary.BigEndian.Uint64(payload), h.FragIdx == keepaliveEcho, nil
}
