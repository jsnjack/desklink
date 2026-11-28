package net

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	stdnet "net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// localSocketName is created in $XDG_RUNTIME_DIR with mode 0600, so only the
// desktop user can start pairing.
const localSocketName = "desklinkd.sock"

// PairingTicket is what `desklinkd pair` shows: the QR payload and the expiry.
type PairingTicket struct {
	IP          string    `json:"ip"`
	Port        int       `json:"port"`
	PIN         string    `json:"pin"`
	Fingerprint string    `json:"fingerprint"`
	Expires     time.Time `json:"expires"`
	// Addresses are all the host's usable addresses, IP among them.
	Addresses []string `json:"addresses,omitempty"`
}

// PairingStatus reports the pairing window.
type PairingStatus struct {
	State  string `json:"state"`
	Device string `json:"device,omitempty"`
}

// PairingControl starts and reports pairing for the local API.
type PairingControl interface {
	BeginPairing(ctx context.Context) (PairingTicket, error)
	PairingStatus() PairingStatus
}

// PeerInfo describes a paired device for `desklinkd peers`; it leaves out
// the key.
type PeerInfo struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Paired   time.Time `json:"paired"`
	LastSeen time.Time `json:"last_seen,omitzero"`
	// Connected is set for the peer whose session runs now.
	Connected bool `json:"connected,omitempty"`
	// RemovalNotSaved marks a device that was removed and can no longer
	// connect, but that peers.json still lists because saving it failed.
	RemovalNotSaved bool `json:"removal_not_saved,omitempty"`
}

// ErrUnknownPeer is returned for a peer ID that is not paired.
var ErrUnknownPeer = errors.New("no paired device has that ID")

// LocalControl is what the local API serves: pairing and the paired devices.
type LocalControl interface {
	PairingControl
	ListPeers() []PeerInfo
	// RemovePeer forgets a device and ends its session if it has one.
	RemovePeer(id string) (PeerInfo, error)
}

// LocalSocketPath returns $XDG_RUNTIME_DIR/desklinkd.sock.
func LocalSocketPath() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set: run desklinkd inside the desktop session")
	}
	return filepath.Join(dir, localSocketName), nil
}

// ServeLocal serves the pairing API on a Unix socket until ctx ends.
func ServeLocal(ctx context.Context, path string, pc LocalControl, onListen func(stdnet.Addr)) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	ln, err := stdnet.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	onListen(ln.Addr())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pair/begin", func(w http.ResponseWriter, r *http.Request) {
		t, err := pc.BeginPairing(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, t)
	})
	mux.HandleFunc("GET /pair/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, pc.PairingStatus())
	})
	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, pc.ListPeers())
	})
	mux.HandleFunc("DELETE /peers/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := pc.RemovePeer(r.PathValue("id"))
		switch {
		case errors.Is(err, ErrUnknownPeer):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusOK, p)
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		if err := srv.Close(); err != nil {
			slog.Debug("close local server", "err", err)
		}
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve %s: %w", path, err)
	}
	return nil
}

// LocalClient talks to a running daemon over its Unix socket.
type LocalClient struct {
	http *http.Client
}

// NewLocalClient returns a client for the socket at path.
func NewLocalClient(path string) *LocalClient {
	return &LocalClient{http: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (stdnet.Conn, error) {
			var d stdnet.Dialer
			return d.DialContext(ctx, "unix", path)
		}},
	}}
}

// BeginPairing asks the daemon for a new PIN.
func (c *LocalClient) BeginPairing(ctx context.Context) (PairingTicket, error) {
	var t PairingTicket
	return t, c.do(ctx, http.MethodPost, "/pair/begin", &t)
}

// Status returns the current pairing status.
func (c *LocalClient) Status(ctx context.Context) (PairingStatus, error) {
	var s PairingStatus
	return s, c.do(ctx, http.MethodGet, "/pair/status", &s)
}

// Peers lists the paired devices.
func (c *LocalClient) Peers(ctx context.Context) ([]PeerInfo, error) {
	var p []PeerInfo
	return p, c.do(ctx, http.MethodGet, "/peers", &p)
}

// RemovePeer forgets the paired device with the given ID.
func (c *LocalClient) RemovePeer(ctx context.Context, id string) (PeerInfo, error) {
	var p PeerInfo
	return p, c.do(ctx, http.MethodDelete, "/peers/"+url.PathEscape(id), &p)
}

func (c *LocalClient) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://desklinkd"+path, nil)
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach desklinkd (is it running? systemctl --user start desklinkd): %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Log(ctx, LevelTrace, "close response body", "err", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			return fmt.Errorf("%s: HTTP %s", path, strconv.Itoa(resp.StatusCode))
		}
		return fmt.Errorf("%s: %s", path, e.Error)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
