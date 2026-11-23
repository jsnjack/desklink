package client

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	stdnet "net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

const httpTimeout = 10 * time.Second

// ErrFingerprintMismatch means the host presented a different certificate
// than the one the profile was paired with.
var ErrFingerprintMismatch = errors.New("certificate fingerprint does not match the paired host")

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

// pinnedTLS trusts only the certificate with the given fingerprint. With an
// empty fingerprint it trusts the first certificate and records it, for
// pairing by PIN without a QR code.
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
			fp := crypto.Fingerprint(raw[0])
			if fingerprint != "" && !strings.EqualFold(fp, fingerprint) {
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

// Health calls GET /health. An empty fingerprint accepts any certificate.
func Health(ctx context.Context, ip string, port int, fingerprint string) (dlnet.Health, error) {
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
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
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
}

// ParsePairingCode reads the pairing JSON.
func ParsePairingCode(text string) (PairingCode, error) {
	var c PairingCode
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &c); err != nil {
		return PairingCode{}, fmt.Errorf("not a DeskLink pairing code: %w", err)
	}
	if c.IP == "" || c.Port == 0 || len(c.PIN) != 6 {
		return PairingCode{}, errors.New("pairing code lacks the address or PIN")
	}
	return c, nil
}

// PairResult is what a successful pairing stores in the profile.
type PairResult struct {
	PeerID      string
	PeerKey     []byte
	Fingerprint string
}

// Pair runs the two-step ECDH and PIN pairing against POST /pair. Without a
// fingerprint, the first certificate seen is trusted and bound into the keys.
func Pair(ctx context.Context, ip string, port int, pin, fingerprint, deviceName string) (PairResult, error) {
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
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return dlnet.PairResponse{}, fmt.Errorf("decode pairing response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return dlnet.PairResponse{}, fmt.Errorf("pairing refused: %s", out.Error)
	}
	return out, nil
}
