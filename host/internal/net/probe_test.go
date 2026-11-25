package net

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net/netip"
	"testing"
	"time"
)

// Shared with ProtocolTest.kt: device ID a9e4ec727e6f64e1, key bytes 0..31,
// nonce bytes 0xA0..0xAF.
const (
	probeVectorRequest = "444c01090000000000000000000000001061396534656337323765366636346531a0a1a2a3a4a5a6a7a8a9aaabacadaeaf" +
		"90416313342141a0b20df5cb8401b9c5b61122ce86e6ab0fda5b331f5b56d857"
	probeVectorReply = "444c0109000000000000000000000000a0a1a2a3a4a5a6a7a8a9aaabacadaeaf" +
		"ed6df4fede51c170bba9536948b78475d37eba7df6aea7615460c0db4ff7e9ce"
)

func probeVectorInputs() (string, []byte, []byte) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	nonce := make([]byte, ProbeNonceSize)
	for i := range nonce {
		nonce[i] = byte(0xA0 + i)
	}
	return "a9e4ec727e6f64e1", key, nonce
}

func TestProbeVectors(t *testing.T) {
	id, key, nonce := probeVectorInputs()
	req, err := ProbeRequest(id, key, nonce)
	if err != nil || hex.EncodeToString(req) != probeVectorRequest {
		t.Fatalf("request %x, %v", req, err)
	}
	rep, err := ProbeReply(req, func(got string) ([]byte, bool) { return key, got == id })
	if err != nil || hex.EncodeToString(rep) != probeVectorReply {
		t.Fatalf("reply %x, %v", rep, err)
	}
	if !CheckProbeReply(rep, key, nonce) {
		t.Error("the client does not accept the host's reply")
	}
}

func TestProbesThatDoNotAuthenticateGetNoReply(t *testing.T) {
	id, key, nonce := probeVectorInputs()
	req, _ := ProbeRequest(id, key, nonce)
	known := func(got string) ([]byte, bool) { return key, got == id }
	tampered := bytes.Clone(req)
	tampered[len(tampered)-1] ^= 1
	inSession := bytes.Clone(req)
	inSession[7] = 1
	other := bytes.Repeat([]byte{9}, 32)
	cases := []struct {
		name   string
		b      []byte
		lookup func(string) ([]byte, bool)
	}{
		{"a tampered HMAC", tampered, known},
		{"an unknown device", req, func(string) ([]byte, bool) { return nil, false }},
		{"another device's key", req, func(string) ([]byte, bool) { return other, true }},
		{"a probe inside a session", inSession, known},
		{"a truncated probe", req[:len(req)-1], known},
		{"a bare header", req[:HeaderSize], known},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ProbeReply(c.b, c.lookup); !errors.Is(err, ErrBadProbe) {
				t.Errorf("err = %v, want ErrBadProbe", err)
			}
		})
	}
	rep, _ := ProbeReply(req, known)
	if CheckProbeReply(rep, other, nonce) || CheckProbeReply(rep, key, bytes.Repeat([]byte{1}, ProbeNonceSize)) {
		t.Error("a reply made with another key or for another nonce was accepted")
	}
}

func TestProbeLimiter(t *testing.T) {
	var l probeLimiter
	a := netip.MustParseAddr("10.0.0.5")
	now := time.Unix(0, 0)
	for i := range probeBurst {
		if !l.allow(a, now) {
			t.Fatalf("probe %d of the burst refused", i)
		}
	}
	if l.allow(a, now) {
		t.Error("a probe beyond the burst was answered")
	}
	if !l.allow(netip.MustParseAddr("10.0.0.6"), now) {
		t.Error("another address shares the first one's limit")
	}
	if !l.allow(a, now.Add(time.Second/probeRate)) {
		t.Error("the bucket did not refill")
	}
}
