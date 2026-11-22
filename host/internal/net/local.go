package net

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	stdnet "net"
	"net/http"
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
}

// PairingStatus reports the pairing window and the screen-sharing grant.
type PairingStatus struct {
	State  string `json:"state"`
	Device string `json:"device,omitempty"`
	// Capture is "pending", "granted" or an error from the ScreenCast portal.
	Capture string `json:"capture"`
}

// PairingControl starts and reports pairing for the local API.
type PairingControl interface {
	BeginPairing(ctx context.Context) (PairingTicket, error)
	PairingStatus() PairingStatus
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
func ServeLocal(ctx context.Context, path string, pc PairingControl, onListen func(stdnet.Addr)) error {
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
