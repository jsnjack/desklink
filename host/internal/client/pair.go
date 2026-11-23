package client

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	stdnet "net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

const (
	httpTimeout = 10 * time.Second
	// maxReplyBytes bounds a /health or /pair reply, which is a few hundred
	// bytes; a longer one fails to decode instead of filling memory.
	maxReplyBytes = 64 << 10
	// maxNameRunes bounds a host name taken from the host's /health.
	maxNameRunes = 64
)

// ErrFingerprintMismatch means the host presented a different certificate
// than the one the profile was paired with.
var ErrFingerprintMismatch = errors.New("certificate fingerprint does not match the paired host")

// ErrNoFingerprint means there is no certificate to pin. The laptop client
// never trusts a certificate it was not told about: the fingerprint comes
// from the pairing code that desklinkd pair prints.
var ErrNoFingerprint = errors.New("no certificate fingerprint to check the host against; pair with the code desklinkd pair prints")

// certWatch records the certificate fingerprint a connection presented.
type certWatch struct {
	mu   sync.Mutex
	seen string
}

func (w *certWatch) observed() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen
}

// pinnedTLS trusts only the certificate with the given fingerprint, and
// none when the fingerprint is empty.
func pinnedTLS(fingerprint string) (*tls.Config, *certWatch) {
	w := &certWatch{}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The fingerprint check below replaces chain and hostname verification.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("host sent no certificate")
			}
			if fingerprint == "" {
				return ErrNoFingerprint
			}
			fp := crypto.Fingerprint(raw[0])
			if !strings.EqualFold(fp, fingerprint) {
				return fmt.Errorf("%w: got %s", ErrFingerprintMismatch, fp)
			}
			w.mu.Lock()
			w.seen = fp
			w.mu.Unlock()
			return nil
		},
	}, w
}

// pinnedHTTP returns a client for a few one-off requests; keep-alives are
// off so no idle TLS connection outlives it on either side.
func pinnedHTTP(fingerprint string) (*http.Client, *certWatch) {
	cfg, w := pinnedTLS(fingerprint)
	return &http.Client{Timeout: httpTimeout, Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}, w
}

func baseURL(ip string, port int) string {
	return "https://" + stdnet.JoinHostPort(ip, strconv.Itoa(port))
}

// Health calls GET /health, trusting only the certificate with the given
// fingerprint.
func Health(ctx context.Context, ip string, port int, fingerprint string) (dlnet.Health, error) {
	if fingerprint == "" {
		return dlnet.Health{}, ErrNoFingerprint
	}
	c, _ := pinnedHTTP(fingerprint)
	return health(ctx, c, baseURL(ip, port))
}

func health(ctx context.Context, c *http.Client, base string) (dlnet.Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return dlnet.Health{}, fmt.Errorf("build health request: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return dlnet.Health{}, fmt.Errorf("reach host: %w", err)
	}
	defer closeBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return dlnet.Health{}, fmt.Errorf("health: HTTP %d", resp.StatusCode)
	}
	var h dlnet.Health
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReplyBytes)).Decode(&h); err != nil {
		return dlnet.Health{}, fmt.Errorf("decode health: %w", err)
	}
	return h, nil
}

func closeBody(c io.Closer) {
	if err := c.Close(); err != nil {
		slog.Debug("close response body", "err", err)
	}
}

// PairingCode is the JSON `desklinkd pair` prints and puts in its QR code.
type PairingCode struct {
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	PIN         string `json:"pin"`
	Fingerprint string `json:"fingerprint"`
	// Addresses are the host's other addresses (other networks, VPNs).
	Addresses []string `json:"addresses,omitempty"`
}

// ParsePairingCode reads the pairing JSON. A code must carry the host's
// address, the six-digit PIN and the certificate fingerprint: the PIN alone
// protects nothing, because whoever answers the pairing request can try all
// million PINs offline against the keys it derived.
func ParsePairingCode(text string) (PairingCode, error) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "{") {
		return PairingCode{}, errors.New("not a DeskLink pairing code: paste the whole line of JSON that desklinkd pair prints, not only the PIN")
	}
	var c PairingCode
	if err := json.Unmarshal([]byte(text), &c); err != nil {
		return PairingCode{}, fmt.Errorf("not a DeskLink pairing code: %w", err)
	}
	if _, err := netip.ParseAddr(c.IP); err != nil || c.Port <= 0 || c.Port > 65535 {
		return PairingCode{}, errors.New("the pairing code lacks the host's address")
	}
	if len(c.PIN) != 6 || strings.Trim(c.PIN, "0123456789") != "" {
		return PairingCode{}, errors.New("the pairing code lacks the six-digit PIN")
	}
	if fp, err := hex.DecodeString(c.Fingerprint); err != nil || len(fp) != 32 {
		return PairingCode{}, errors.New("the pairing code lacks the certificate fingerprint; copy the whole line desklinkd pair prints")
	}
	c.Fingerprint = strings.ToLower(c.Fingerprint)
	return c, nil
}

// PairResult is what a successful pairing stores in the profile.
type PairResult struct {
	PeerID      string
	PeerKey     []byte
	Fingerprint string
}

// Pair runs the two-step ECDH and PIN pairing against POST /pair, trusting
// only the certificate with the given fingerprint, which is bound into the
// keys.
func Pair(ctx context.Context, ip string, port int, pin, fingerprint, deviceName string) (PairResult, error) {
	if fingerprint == "" {
		return PairResult{}, ErrNoFingerprint
	}
	c, watch := pinnedHTTP(fingerprint)
	base := baseURL(ip, port)
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return PairResult{}, fmt.Errorf("generate pairing key: %w", err)
	}
	clientPub := priv.PublicKey().Bytes()
	start, err := postPair(ctx, c, base, dlnet.PairRequest{Phase: dlnet.PairPhaseStart, ClientPub: clientPub})
	if err != nil {
		return PairResult{}, err
	}
	hostKey, err := ecdh.P256().NewPublicKey(start.HostPub)
	if err != nil {
		return PairResult{}, fmt.Errorf("host public key: %w", err)
	}
	shared, err := priv.ECDH(hostKey)
	if err != nil {
		return PairResult{}, fmt.Errorf("ecdh: %w", err)
	}
	seen := watch.observed()
	keys, err := crypto.DerivePairingKeys(shared, strings.TrimSpace(pin), clientPub, start.HostPub, seen)
	if err != nil {
		return PairResult{}, err
	}
	fin, err := postPair(ctx, c, base, dlnet.PairRequest{
		Phase: dlnet.PairPhaseFinish, PairingID: start.PairingID, Proof: keys.ClientProof(), Name: deviceName,
	})
	if err != nil {
		return PairResult{}, err
	}
	if !hmac.Equal(fin.Proof, keys.HostProof()) {
		return PairResult{}, errors.New("the host could not prove it knows the PIN")
	}
	return PairResult{PeerID: fin.PeerID, PeerKey: keys.PeerKey, Fingerprint: seen}, nil
}

// PairWithCode pairs with the host a pairing code names and saves it. Every
// request trusts only the code's certificate, so only the desktop that
// printed the code can answer, at any address: the code's addresses are
// asked for /health at once and the pairing runs at the first that answers.
// That pinned answer also gives the MAC, the host's other addresses and, for
// a new host, its name.
//
// The code decides which saved host it pairs, as on the tablet: the one with
// the code's certificate (the same machine on any network), else hostID (the
// host whose Pair button was used, which may have another certificate that
// the code's then replaces), else the one at the code's address and port,
// else a new one. Pairing a machine saved under other entries folds them into
// one (Absorb). It returns the host as saved.
func PairWithCode(ctx context.Context, s *Store, code PairingCode, hostID, deviceName string) (Profile, error) {
	if code.Fingerprint == "" {
		return Profile{}, ErrNoFingerprint
	}
	probe := Profile{IP: code.IP, Port: code.Port, Fingerprint: code.Fingerprint}.WithAddresses(code.IP, code.Addresses)
	reached, h, err := Reach(ctx, probe)
	if err != nil {
		return Profile{}, err
	}
	res, err := Pair(ctx, reached.IP, code.Port, code.PIN, code.Fingerprint, deviceName)
	if err != nil {
		return Profile{}, err
	}
	p, ok := s.pairingTarget(hostID, code)
	if !ok {
		id, err := newID()
		if err != nil {
			return Profile{}, err
		}
		p = Profile{ID: id, Name: hostName(h.Hostname, code.IP), IP: reached.IP}
	}
	p.PeerID, p.PeerKey, p.Fingerprint, p.Port = res.PeerID, res.PeerKey, res.Fingerprint, code.Port
	if h.MAC != "" {
		p.MAC = h.MAC
	}
	p.IP, p.Addresses = reached.IP, mergeAddresses(reached.IP, reached.Candidates(), p.Candidates())
	return s.Absorb(p)
}

// pairingTarget is the saved host a pairing with code updates, if any.
func (s *Store) pairingTarget(hostID string, code PairingCode) (Profile, bool) {
	if p, ok := s.FindByFingerprint(code.Fingerprint); ok {
		return p, true
	}
	if p, ok := s.Host(hostID); ok {
		return p, true
	}
	return s.FindByAddress(code.IP, code.Port)
}

// hostName is a label for a new host from the hostname its /health
// reported: printable characters only, at most maxNameRunes, else fallback.
func hostName(reported, fallback string) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, reported)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxNameRunes {
		name = string(r[:maxNameRunes])
	}
	if name == "" {
		return fallback
	}
	return name
}

func postPair(ctx context.Context, c *http.Client, base string, body dlnet.PairRequest) (dlnet.PairResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return dlnet.PairResponse{}, fmt.Errorf("encode pairing request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/pair", bytes.NewReader(data))
	if err != nil {
		return dlnet.PairResponse{}, fmt.Errorf("build pairing request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return dlnet.PairResponse{}, fmt.Errorf("reach host: %w", err)
	}
	defer closeBody(resp.Body)
	var out dlnet.PairResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReplyBytes)).Decode(&out); err != nil {
		return dlnet.PairResponse{}, fmt.Errorf("decode pairing response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return dlnet.PairResponse{}, fmt.Errorf("pairing refused: %s", out.Error)
	}
	return out, nil
}

// ErrForgetUnsupported means the host predates DELETE /pair; remove the
// pairing there with desklinkd peers remove.
var ErrForgetUnsupported = errors.New("the host is too old to forget this device remotely")

// ErrForgetUnconfirmed means the host refused the forget request's
// credentials (HTTP 401). The host answers 401 both when it no longer knows
// this device and when the token is wrong, for example after its clock
// changed, so it may still hold the pairing: check with desklinkd peers on
// the host and remove it there.
var ErrForgetUnconfirmed = errors.New("the host did not confirm that it forgot this device")

// Forget asks the host to remove this device's pairing, so it no longer
// accepts the device. Only HTTP 200 confirms the removal; a 401 returns
// ErrForgetUnconfirmed.
func Forget(ctx context.Context, p Profile) error {
	if !p.Paired() {
		return nil
	}
	reached, h, err := Reach(ctx, p)
	if err != nil {
		return err
	}
	// Stamped with the host's clock, as for Connect.
	token, err := crypto.AuthToken(p.PeerID, p.PeerKey, time.UnixMilli(h.TimeMs))
	if err != nil {
		return err
	}
	c, _ := pinnedHTTP(p.Fingerprint)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL(reached.IP, p.Port)+"/pair", nil)
	if err != nil {
		return fmt.Errorf("build forget request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("reach host: %w", err)
	}
	defer closeBody(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrForgetUnconfirmed
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return ErrForgetUnsupported
	default:
		return fmt.Errorf("forget: HTTP %d", resp.StatusCode)
	}
}
