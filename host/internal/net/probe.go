package net

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
)

// A UDP probe tells a client whether the host's media port answers from an
// address, before any session: HTTPS can get through where a firewall drops
// UDP. It needs no session key, so it is authenticated with the pairing key:
// the request carries the device ID, a nonce and an HMAC over both, and the
// host answers only a request whose HMAC checks out, so a stranger learns
// nothing. The reply is smaller than the request, which leaves nothing to
// amplify. Protocol.kt mirrors the layout.
//
//	request:  header (type 0x09, session 0) | id length (1) | device ID | nonce (16) | HMAC (32)
//	reply:    header (type 0x09, session 0) | nonce (16) | HMAC (32)
//
// The request's HMAC is HMAC-SHA256(peer key, ProbeRequestLabel | id | nonce),
// the reply's HMAC-SHA256(peer key, ProbeReplyLabel | nonce).
const (
	ProbeNonceSize    = 16
	probeMACSize      = sha256.Size
	ProbeRequestLabel = "desklink-udp-probe"
	ProbeReplyLabel   = "desklink-udp-probe-reply"
	maxProbeDeviceID  = 64
	ProbeReplySize    = HeaderSize + ProbeNonceSize + probeMACSize
)

// ErrBadProbe is a probe that is malformed or does not authenticate.
var ErrBadProbe = errors.New("bad UDP probe")

func probeMAC(key []byte, label string, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label))
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// ProbeRequest builds the probe a client sends for device id with its
// pairing key and a fresh nonce.
func ProbeRequest(id string, key, nonce []byte) ([]byte, error) {
	if len(id) == 0 || len(id) > maxProbeDeviceID || len(nonce) != ProbeNonceSize {
		return nil, fmt.Errorf("%w: device ID or nonce size", ErrBadProbe)
	}
	b := make([]byte, HeaderSize, HeaderSize+1+len(id)+ProbeNonceSize+probeMACSize)
	Header{Type: PacketProbe}.put(b)
	b = append(b, byte(len(id)))
	b = append(b, id...)
	b = append(b, nonce...)
	return append(b, probeMAC(key, ProbeRequestLabel, []byte(id), nonce)...), nil
}

// ParseProbeRequest returns a request's device ID, nonce and HMAC, unchecked.
func ParseProbeRequest(b []byte) (id string, nonce, mac []byte, err error) {
	h, err := ParseHeader(b)
	if err != nil || h.Type != PacketProbe || h.SessionID != 0 || len(b) < HeaderSize+1 {
		return "", nil, nil, ErrBadProbe
	}
	n := int(b[HeaderSize])
	if n == 0 || n > maxProbeDeviceID || len(b) != HeaderSize+1+n+ProbeNonceSize+probeMACSize {
		return "", nil, nil, ErrBadProbe
	}
	p := b[HeaderSize+1:]
	return string(p[:n]), p[n : n+ProbeNonceSize], p[n+ProbeNonceSize:], nil
}

// ProbeReply checks a request against the key of the device it names
// (lookup) and returns the reply to send, or ErrBadProbe.
func ProbeReply(b []byte, lookup func(id string) ([]byte, bool)) ([]byte, error) {
	id, nonce, mac, err := ParseProbeRequest(b)
	if err != nil {
		return nil, err
	}
	key, ok := lookup(id)
	if !ok || !hmac.Equal(mac, probeMAC(key, ProbeRequestLabel, []byte(id), nonce)) {
		return nil, ErrBadProbe
	}
	r := make([]byte, HeaderSize, ProbeReplySize)
	Header{Type: PacketProbe}.put(r)
	r = append(r, nonce...)
	return append(r, probeMAC(key, ProbeReplyLabel, nonce)...), nil
}

// CheckProbeReply reports whether b is the host's reply to the request with
// this nonce, made with the device's key.
func CheckProbeReply(b, key, nonce []byte) bool {
	h, err := ParseHeader(b)
	if err != nil || h.Type != PacketProbe || len(b) != ProbeReplySize {
		return false
	}
	p := b[HeaderSize:]
	return hmac.Equal(p[:ProbeNonceSize], nonce) && hmac.Equal(p[ProbeNonceSize:], probeMAC(key, ProbeReplyLabel, nonce))
}
