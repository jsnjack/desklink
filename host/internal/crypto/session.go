// Package crypto implements DeskLink pairing (ECDH P-256 bound to a one-time
// PIN through HKDF), bearer tokens for the control plane and the per-session
// ChaCha20-Poly1305 keys for the UDP media plane.
//
// The Android client mirrors every derivation in SessionCrypto.kt; a change
// to a label or an encoding here must be made there too.
package crypto

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// KeySize is the length of every symmetric key.
	KeySize = 32
	// PINLifetime is how long a pairing PIN stays valid.
	PINLifetime = 5 * time.Minute
	// MaxPINAttempts is the number of wrong proofs that cancels a PIN.
	MaxPINAttempts = 5
	// TokenWindow is how far a bearer token's timestamp may be from the host clock.
	TokenWindow = 5 * time.Minute
	// pendingLifetime bounds the gap between the two pairing requests.
	pendingLifetime = time.Minute
	maxPending      = 8

	labelPair    = "desklink pair v1"
	labelPINSalt = "desklink pair pin v1:"
	labelAuth    = "desklink auth v1"
	labelMedia   = "desklink media v1"
	proofClient  = "client"
	proofHost    = "host"
)

// Pairing errors returned by Pairer.
var (
	ErrNoPairing       = errors.New("no pairing in progress: run `desklinkd pair` on the host")
	ErrPairingExpired  = errors.New("pairing PIN expired")
	ErrBadProof        = errors.New("wrong PIN")
	ErrTooManyAttempts = errors.New("too many wrong PINs: pairing cancelled")
	ErrUnknownPairing  = errors.New("unknown or expired pairing request")
	ErrBadToken        = errors.New("invalid bearer token")
)

// NewPIN returns a uniformly random six-digit PIN.
func NewPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("generate PIN: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// Fingerprint is the lowercase hex SHA-256 of a DER certificate.
func Fingerprint(certDER []byte) string {
	sum := sha256.Sum256(certDER)
	return hex.EncodeToString(sum[:])
}

// PairingKeys are derived from the ECDH secret and the PIN.
type PairingKeys struct {
	PeerKey []byte
	confirm []byte
}

// DerivePairingKeys binds the ECDH shared secret to the PIN, both public keys
// and the host certificate fingerprint. A client that talked to an impostor
// certificate, or typed a different PIN, derives different keys.
func DerivePairingKeys(shared []byte, pin string, clientPub, hostPub []byte, fingerprint string) (PairingKeys, error) {
	salt := sha256.Sum256([]byte(labelPINSalt + pin))
	info := strings.Join([]string{labelPair, hex.EncodeToString(clientPub), hex.EncodeToString(hostPub), fingerprint}, "|")
	okm, err := hkdf.Key(sha256.New, shared, salt[:], info, 2*KeySize)
	if err != nil {
		return PairingKeys{}, fmt.Errorf("derive pairing keys: %w", err)
	}
	return PairingKeys{PeerKey: okm[:KeySize], confirm: okm[KeySize:]}, nil
}

// ClientProof is the MAC the client sends to prove it knows the PIN.
func (k PairingKeys) ClientProof() []byte { return mac(k.confirm, proofClient) }

// HostProof is the MAC the host returns to prove it knows the PIN.
func (k PairingKeys) HostProof() []byte { return mac(k.confirm, proofHost) }

func mac(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// PairingState describes the pairing window for `desklinkd pair`.
type PairingState string

// Pairing states reported by Pairer.State.
const (
	PairingIdle    PairingState = "idle"
	PairingWaiting PairingState = "waiting"
	PairingDone    PairingState = "paired"
	PairingExpired PairingState = "expired"
	PairingLocked  PairingState = "locked"
)

type pendingPair struct {
	priv      *ecdh.PrivateKey
	clientPub []byte
	created   time.Time
}

// Pairer runs the host side of pairing. The client first sends its public
// key (Start) and only then proves the PIN (Finish), so the host reveals
// nothing PIN-dependent to a client that has not proven it; each wrong proof
// counts against MaxPINAttempts.
type Pairer struct {
	fingerprint string
	now         func() time.Time

	mu         sync.Mutex
	pin        string
	expires    time.Time
	attempts   int
	state      PairingState
	pairedName string
	pending    map[string]pendingPair
}

// NewPairer returns a Pairer for the host certificate with this fingerprint.
func NewPairer(fingerprint string, now func() time.Time) *Pairer {
	return &Pairer{fingerprint: fingerprint, now: now, state: PairingIdle, pending: map[string]pendingPair{}}
}

// Begin opens a pairing window with a fresh PIN.
func (p *Pairer) Begin() (pin string, expires time.Time, err error) {
	pin, err = NewPIN()
	if err != nil {
		return "", time.Time{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pin = pin
	p.expires = p.now().Add(PINLifetime)
	p.attempts = 0
	p.state = PairingWaiting
	p.pairedName = ""
	p.pending = map[string]pendingPair{}
	return pin, p.expires, nil
}

// State reports the pairing window and, once paired, the device name.
func (p *Pairer) State() (PairingState, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked()
	return p.state, p.pairedName
}

func (p *Pairer) expireLocked() {
	if p.state == PairingWaiting && !p.now().Before(p.expires) {
		p.state = PairingExpired
		p.pin = ""
		p.pending = map[string]pendingPair{}
	}
}

func (p *Pairer) activeLocked() error {
	p.expireLocked()
	switch p.state {
	case PairingWaiting:
		return nil
	case PairingExpired:
		return ErrPairingExpired
	case PairingLocked:
		return ErrTooManyAttempts
	default:
		return ErrNoPairing
	}
}

// Start accepts the client's ephemeral public key (uncompressed P-256) and
// returns a pairing ID and the host's ephemeral public key.
func (p *Pairer) Start(clientPub []byte) (pairingID string, hostPub []byte, err error) {
	if _, err := ecdh.P256().NewPublicKey(clientPub); err != nil {
		return "", nil, fmt.Errorf("client public key: %w", err)
	}
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("generate host key: %w", err)
	}
	id, err := randomHex(16)
	if err != nil {
		return "", nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.activeLocked(); err != nil {
		return "", nil, err
	}
	now := p.now()
	for k, v := range p.pending {
		if now.Sub(v.created) > pendingLifetime {
			delete(p.pending, k)
		}
	}
	if len(p.pending) >= maxPending {
		return "", nil, ErrTooManyAttempts
	}
	p.pending[id] = pendingPair{priv: priv, clientPub: append([]byte(nil), clientPub...), created: now}
	return id, priv.PublicKey().Bytes(), nil
}

// Finish checks the client's proof. On success it closes the pairing window
// and returns the new peer key and the host's proof.
func (p *Pairer) Finish(pairingID string, clientProof []byte, deviceName string) (peerKey, hostProof []byte, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.activeLocked(); err != nil {
		return nil, nil, err
	}
	pend, ok := p.pending[pairingID]
	if !ok || p.now().Sub(pend.created) > pendingLifetime {
		return nil, nil, ErrUnknownPairing
	}
	delete(p.pending, pairingID)
	clientKey, err := ecdh.P256().NewPublicKey(pend.clientPub)
	if err != nil {
		return nil, nil, fmt.Errorf("client public key: %w", err)
	}
	shared, err := pend.priv.ECDH(clientKey)
	if err != nil {
		return nil, nil, fmt.Errorf("ecdh: %w", err)
	}
	keys, err := DerivePairingKeys(shared, p.pin, pend.clientPub, pend.priv.PublicKey().Bytes(), p.fingerprint)
	if err != nil {
		return nil, nil, err
	}
	if !hmac.Equal(keys.ClientProof(), clientProof) {
		p.attempts++
		if p.attempts >= MaxPINAttempts {
			p.state = PairingLocked
			p.pin = ""
			p.pending = map[string]pendingPair{}
			return nil, nil, ErrTooManyAttempts
		}
		return nil, nil, ErrBadProof
	}
	p.state = PairingDone
	p.pairedName = deviceName
	p.pin = ""
	p.pending = map[string]pendingPair{}
	return keys.PeerKey, keys.HostProof(), nil
}

// NewPeerID returns a random peer identifier.
func NewPeerID() (string, error) { return randomHex(8) }

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func authKey(peerKey []byte) ([]byte, error) {
	k, err := hkdf.Key(sha256.New, peerKey, nil, labelAuth, KeySize)
	if err != nil {
		return nil, fmt.Errorf("derive auth key: %w", err)
	}
	return k, nil
}

// AuthToken returns "<peer_id>.<unix seconds>.<hex HMAC>" for the
// Authorization: Bearer header.
func AuthToken(peerID string, peerKey []byte, now time.Time) (string, error) {
	k, err := authKey(peerKey)
	if err != nil {
		return "", err
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	return peerID + "." + ts + "." + hex.EncodeToString(mac(k, peerID+"."+ts)), nil
}

// VerifyAuthToken checks a bearer token and returns its peer ID. The client
// uses the host time from /health, so the window only has to absorb latency.
func VerifyAuthToken(token string, lookup func(peerID string) ([]byte, bool), now time.Time) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrBadToken
	}
	peerID, tsText, sigHex := parts[0], parts[1], parts[2]
	ts, err := strconv.ParseInt(tsText, 10, 64)
	if err != nil {
		return "", ErrBadToken
	}
	if d := now.Sub(time.Unix(ts, 0)); d > TokenWindow || d < -TokenWindow {
		return "", fmt.Errorf("%w: timestamp is %s away from host time", ErrBadToken, d.Round(time.Second))
	}
	peerKey, ok := lookup(peerID)
	if !ok {
		return "", fmt.Errorf("%w: unknown peer %q", ErrBadToken, peerID)
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", ErrBadToken
	}
	k, err := authKey(peerKey)
	if err != nil {
		return "", err
	}
	if !hmac.Equal(mac(k, peerID+"."+tsText), sig) {
		return "", ErrBadToken
	}
	return peerID, nil
}

// SessionKeys are the two directional media keys of one session. Separate
// keys let both sides count sequence numbers from zero without nonce reuse.
type SessionKeys struct {
	HostToClient []byte
	ClientToHost []byte
}

// DeriveSessionKeys derives the media keys for a session from the peer key,
// the session ID and a random nonce the host picks for every session.
func DeriveSessionKeys(peerKey []byte, sessionID uint32, hostNonce []byte) (SessionKeys, error) {
	info := fmt.Sprintf("%s|%08x", labelMedia, sessionID)
	okm, err := hkdf.Key(sha256.New, peerKey, hostNonce, info, 2*KeySize)
	if err != nil {
		return SessionKeys{}, fmt.Errorf("derive session keys: %w", err)
	}
	return SessionKeys{HostToClient: okm[:KeySize], ClientToHost: okm[KeySize:]}, nil
}

// NewAEAD returns ChaCha20-Poly1305 for a 32-byte key.
func NewAEAD(key []byte) (cipher.AEAD, error) {
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("chacha20poly1305: %w", err)
	}
	return a, nil
}
