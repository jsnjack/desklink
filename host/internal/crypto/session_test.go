package crypto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func seqBytes(n int, start byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

// The same vectors are asserted in SessionCryptoTest.kt, so the Go host and
// the Android client derive identical keys.
func TestDerivationsMatchSharedVectors(t *testing.T) {
	clientPub := append([]byte{4}, seqBytes(64, 0x40)...)
	hostPub := append([]byte{4}, seqBytes(64, 0x80)...)
	fp := "ab" + hex.EncodeToString(seqBytes(31, 0))
	keys, err := DerivePairingKeys(seqBytes(32, 0x10), "123456", clientPub, hostPub, fp)
	if err != nil {
		t.Fatal(err)
	}
	peerKey := seqBytes(32, 0xA0)
	token, err := AuthToken("0011223344556677", peerKey, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	sk, err := DeriveSessionKeys(peerKey, 0x01020304, seqBytes(16, 0x30))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, got, want string
	}{
		{"peer key", hex.EncodeToString(keys.PeerKey), "b2e174e2435c70e3a5dcb935c9de25e1d49f29da6d961e1fb336a5fe8f132cd6"},
		{"client proof", hex.EncodeToString(keys.ClientProof()), "6fd9aa9945c6784d2433920c4899807fafd3756b0e6c95d49c483848a754cd63"},
		{"host proof", hex.EncodeToString(keys.HostProof()), "b636fb05e935edbfa59992c09e01afb02c1ef5f56c0ec9189512ebd9552a14d3"},
		{"auth token", token, "0011223344556677.1800000000.bc6503e1012a494178f17a35eef51de0bbcfa9c70ab2b7f603211be7917cabf8"},
		{"host to client key", hex.EncodeToString(sk.HostToClient), "526722d5d437ce84e5fb7ef6514a801596ec1cbf109dea3778ab18cfac7dd503"},
		{"client to host key", hex.EncodeToString(sk.ClientToHost), "4c57da5db0f9e19d6c83834886a53a30cff31ab99aac9b36264e07feb99647fc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("got %s, want %s", c.got, c.want)
			}
		})
	}
}

// client runs the Android side of pairing against a Pairer.
type client struct {
	priv *ecdh.PrivateKey
}

func newClient(t *testing.T) client {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return client{priv: priv}
}

func (c client) keys(t *testing.T, hostPub []byte, pin, fp string) PairingKeys {
	t.Helper()
	hk, err := ecdh.P256().NewPublicKey(hostPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := c.priv.ECDH(hk)
	if err != nil {
		t.Fatal(err)
	}
	k, err := DerivePairingKeys(shared, pin, c.priv.PublicKey().Bytes(), hostPub, fp)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestPairing(t *testing.T) {
	const fp = "f00d"
	t.Run("correct PIN derives the same peer key on both sides", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(1000, 0)}
		p := NewPairer(fp, clock.Now)
		pin, _, err := p.Begin()
		if err != nil {
			t.Fatal(err)
		}
		c := newClient(t)
		id, hostPub, err := p.Start(c.priv.PublicKey().Bytes())
		if err != nil {
			t.Fatal(err)
		}
		ck := c.keys(t, hostPub, pin, fp)
		peerKey, hostProof, err := p.Finish(id, ck.ClientProof(), "Tab S9")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(peerKey, ck.PeerKey) {
			t.Error("peer keys differ")
		}
		if !bytes.Equal(hostProof, ck.HostProof()) {
			t.Error("host proof does not verify on the client")
		}
		if st, dev := p.State(); st != PairingDone || dev != "Tab S9" {
			t.Errorf("state %s %q, want paired Tab S9", st, dev)
		}
		if _, _, err := p.Start(c.priv.PublicKey().Bytes()); !errors.Is(err, ErrNoPairing) {
			t.Errorf("PIN reuse: got %v, want ErrNoPairing", err)
		}
	})
	t.Run("wrong PIN is rejected", func(t *testing.T) {
		p := NewPairer(fp, time.Now)
		if _, _, err := p.Begin(); err != nil {
			t.Fatal(err)
		}
		c := newClient(t)
		id, hostPub, err := p.Start(c.priv.PublicKey().Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.Finish(id, c.keys(t, hostPub, "000000x", fp).ClientProof(), "x"); !errors.Is(err, ErrBadProof) {
			t.Errorf("got %v, want ErrBadProof", err)
		}
	})
	t.Run("a different certificate fingerprint fails like a wrong PIN", func(t *testing.T) {
		p := NewPairer(fp, time.Now)
		pin, _, err := p.Begin()
		if err != nil {
			t.Fatal(err)
		}
		c := newClient(t)
		id, hostPub, err := p.Start(c.priv.PublicKey().Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.Finish(id, c.keys(t, hostPub, pin, "beef").ClientProof(), "x"); !errors.Is(err, ErrBadProof) {
			t.Errorf("got %v, want ErrBadProof", err)
		}
	})
	t.Run("five wrong PINs cancel pairing", func(t *testing.T) {
		p := NewPairer(fp, time.Now)
		if _, _, err := p.Begin(); err != nil {
			t.Fatal(err)
		}
		var last error
		for range MaxPINAttempts {
			c := newClient(t)
			id, hostPub, err := p.Start(c.priv.PublicKey().Bytes())
			if err != nil {
				t.Fatal(err)
			}
			_, _, last = p.Finish(id, c.keys(t, hostPub, "bad", fp).ClientProof(), "x")
		}
		if !errors.Is(last, ErrTooManyAttempts) {
			t.Fatalf("got %v, want ErrTooManyAttempts", last)
		}
		c := newClient(t)
		if _, _, err := p.Start(c.priv.PublicKey().Bytes()); !errors.Is(err, ErrTooManyAttempts) {
			t.Errorf("after lockout: got %v, want ErrTooManyAttempts", err)
		}
	})
	t.Run("PIN expires after five minutes", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(1000, 0)}
		p := NewPairer(fp, clock.Now)
		if _, _, err := p.Begin(); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(PINLifetime)
		c := newClient(t)
		if _, _, err := p.Start(c.priv.PublicKey().Bytes()); !errors.Is(err, ErrPairingExpired) {
			t.Errorf("got %v, want ErrPairingExpired", err)
		}
	})
	t.Run("no pairing window", func(t *testing.T) {
		p := NewPairer(fp, time.Now)
		c := newClient(t)
		if _, _, err := p.Start(c.priv.PublicKey().Bytes()); !errors.Is(err, ErrNoPairing) {
			t.Errorf("got %v, want ErrNoPairing", err)
		}
	})
	t.Run("invalid client key", func(t *testing.T) {
		p := NewPairer(fp, time.Now)
		if _, _, err := p.Begin(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.Start([]byte{4, 1, 2, 3}); err == nil {
			t.Error("accepted a malformed public key")
		}
	})
}

func TestNewPIN(t *testing.T) {
	for range 50 {
		pin, err := NewPIN()
		if err != nil {
			t.Fatal(err)
		}
		if len(pin) != 6 || strings.Trim(pin, "0123456789") != "" {
			t.Fatalf("PIN %q is not six digits", pin)
		}
	}
}

func TestAuthToken(t *testing.T) {
	key := seqBytes(32, 1)
	now := time.Unix(1_800_000_000, 0)
	lookup := func(id string) ([]byte, bool) {
		if id == "peer" {
			return key, true
		}
		return nil, false
	}
	good, err := AuthToken("peer", key, now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := AuthToken("stranger", key, now)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		token string
		at    time.Time
		ok    bool
	}{
		{"valid", good, now, true},
		{"within the clock window", good, now.Add(TokenWindow - time.Second), true},
		{"too old", good, now.Add(TokenWindow + time.Second), false},
		{"from the future", good, now.Add(-TokenWindow - time.Second), false},
		{"tampered signature", good[:len(good)-1] + "0", now, false},
		{"unknown peer", other, now, false},
		{"malformed", "peer.notanumber.00", now, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := VerifyAuthToken(c.token, lookup, c.at)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
			if c.ok && id != "peer" {
				t.Errorf("peer %q", id)
			}
		})
	}
}

func TestSessionKeys(t *testing.T) {
	peerKey := seqBytes(32, 7)
	a, err := DeriveSessionKeys(peerKey, 1, seqBytes(16, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveSessionKeys(peerKey, 2, seqBytes(16, 0))
	if err != nil {
		t.Fatal(err)
	}
	c, err := DeriveSessionKeys(peerKey, 1, seqBytes(16, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("directions use different keys", func(t *testing.T) {
		if bytes.Equal(a.HostToClient, a.ClientToHost) {
			t.Error("directional keys are equal")
		}
	})
	t.Run("session ID changes the keys", func(t *testing.T) {
		if bytes.Equal(a.HostToClient, b.HostToClient) {
			t.Error("keys repeat across session IDs")
		}
	})
	t.Run("host nonce changes the keys", func(t *testing.T) {
		if bytes.Equal(a.HostToClient, c.HostToClient) {
			t.Error("keys repeat across nonces")
		}
	})
}

func TestAEADRoundTrip(t *testing.T) {
	aead, err := NewAEAD(seqBytes(KeySize, 3))
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	ct := aead.Seal(nil, nonce, []byte("frame"), []byte("hdr"))
	t.Run("decrypts", func(t *testing.T) {
		pt, err := aead.Open(nil, nonce, ct, []byte("hdr"))
		if err != nil || string(pt) != "frame" {
			t.Fatalf("got %q, %v", pt, err)
		}
	})
	t.Run("rejects changed additional data", func(t *testing.T) {
		if _, err := aead.Open(nil, nonce, ct, []byte("HDR")); err == nil {
			t.Error("accepted modified header")
		}
	})
}
