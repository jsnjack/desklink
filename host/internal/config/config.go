// Package config loads the desklinkd configuration and persists paired peers
// and the monitor layout to restore in the user's config directory.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AppName names the directory under the XDG config directory.
const AppName = "desklink"

// MaxQP is the highest quantizer the HEVC scale allows.
const MaxQP = 51

// PinnedApp is an application shown first in the client's app drawer. It is
// matched against installed .desktop entries by ID or by executable name.
type PinnedApp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Exec string `json:"exec"`
}

// Config is the daemon configuration read from config.json.
type Config struct {
	ControlAddr string `json:"control_addr"`
	MediaAddr   string `json:"media_addr"`
	// AdvertiseIP goes into the pairing QR code; empty means the source
	// address of the default route.
	AdvertiseIP    string      `json:"advertise_ip"`
	AllowedSubnets []string    `json:"allowed_subnets"`
	PinnedApps     []PinnedApp `json:"pinned_apps"`
	// Monitor is the connector of the monitor to stream, such as "DP-1";
	// empty means the primary monitor.
	Monitor        string `json:"monitor"`
	WaylandDisplay string `json:"wayland_display"`
	MinQP          int    `json:"min_qp"`
	MaxQP          int    `json:"max_qp"`
	// LockAfterAutologin locks a session that GDM opened by automatic login
	// as soon as desklinkd starts, so a host that logs in by itself after a
	// reboot is reachable without leaving its desktop open.
	LockAfterAutologin bool `json:"lock_after_autologin"`
}

// Default returns the configuration used when config.json is missing.
func Default() Config {
	return Config{
		ControlAddr: "0.0.0.0:9443",
		MediaAddr:   "0.0.0.0:9444",
		// RFC 1918 plus loopback, so `desklinkd pair` and local tests work.
		AllowedSubnets:     []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8"},
		PinnedApps:         []PinnedApp{{ID: "bunk-gui", Name: "Bunk Terminal", Exec: "bunk-gui"}},
		WaylandDisplay:     "wayland-0",
		MinQP:              16,
		MaxQP:              26,
		LockAfterAutologin: true,
	}
}

// Dir returns ~/.config/desklink, honouring $XDG_CONFIG_HOME.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(base, AppName), nil
}

// Load reads the configuration at path. A missing file yields Default.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks addresses, subnets and the quantizer range.
func (c Config) Validate() error {
	if _, err := c.Subnets(); err != nil {
		return err
	}
	if c.AdvertiseIP != "" {
		if _, err := netip.ParseAddr(c.AdvertiseIP); err != nil {
			return fmt.Errorf("advertise_ip %q: %w", c.AdvertiseIP, err)
		}
	}
	if c.MinQP < 0 || c.MaxQP > MaxQP || c.MinQP > c.MaxQP {
		return fmt.Errorf("qp range %d..%d must lie within 0..%d", c.MinQP, c.MaxQP, MaxQP)
	}
	return nil
}

// Subnets parses AllowedSubnets.
func (c Config) Subnets() ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(c.AllowedSubnets))
	for _, s := range c.AllowedSubnets {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("allowed_subnets %q: %w", s, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Paths names the files desklinkd keeps in its config directory.
type Paths struct {
	Dir string
}

// Config is the path of config.json.
func (p Paths) Config() string { return filepath.Join(p.Dir, "config.json") }

// Peers is the path of peers.json.
func (p Paths) Peers() string { return filepath.Join(p.Dir, "peers.json") }

// State is the path of state.json.
func (p Paths) State() string { return filepath.Join(p.Dir, "state.json") }

// TLSCert is the path of the self-signed certificate.
func (p Paths) TLSCert() string { return filepath.Join(p.Dir, "tls.crt") }

// TLSKey is the path of the certificate's private key.
func (p Paths) TLSKey() string { return filepath.Join(p.Dir, "tls.key") }

// Peer is a paired client device.
type Peer struct {
	ID     string    `json:"peer_id"`
	Name   string    `json:"name"`
	Key    []byte    `json:"peer_key"`
	Paired time.Time `json:"paired"`
}

// PeerStore holds paired peers and saves them to peers.json.
type PeerStore struct {
	path  string
	mu    sync.RWMutex
	peers map[string]Peer
}

// OpenPeers loads the peer store at path. A missing file is an empty store.
func OpenPeers(path string) (*PeerStore, error) {
	s := &PeerStore{path: path, peers: map[string]Peer{}}
	var list []Peer
	if err := readJSON(path, &list); err != nil {
		return nil, fmt.Errorf("load peers: %w", err)
	}
	for _, p := range list {
		s.peers[p.ID] = p
	}
	return s, nil
}

// Get returns the peer with the given ID.
func (s *PeerStore) Get(id string) (Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peers[id]
	return p, ok
}

// Add stores a peer and saves the file.
func (s *PeerStore) Add(p Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[p.ID] = p
	list := make([]Peer, 0, len(s.peers))
	for _, v := range s.peers {
		list = append(list, v)
	}
	if err := writeJSON(s.path, list); err != nil {
		return fmt.Errorf("save peers: %w", err)
	}
	return nil
}

// State is what desklinkd remembers between runs.
type State struct {
	// DisplaySnapshot is the monitor layout to restore after a session that
	// changed the resolution. It survives a crash so the next start can undo it.
	DisplaySnapshot json.RawMessage `json:"display_snapshot,omitempty"`
	// AudioDefaultSink is the output that was the default before a session
	// moved the sound to its virtual output; set while a session runs, so
	// the next start can undo a crashed run.
	AudioDefaultSink string `json:"audio_default_sink,omitempty"`
}

// StateStore guards State and saves it to state.json on every change.
type StateStore struct {
	path string
	mu   sync.Mutex
	st   State
}

// OpenState loads state.json. A missing file is an empty state.
func OpenState(path string) (*StateStore, error) {
	s := &StateStore{path: path}
	if err := readJSON(path, &s.st); err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	return s, nil
}

// DisplaySnapshot returns the saved monitor layout, or nil.
func (s *StateStore) DisplaySnapshot() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.DisplaySnapshot
}

// SetDisplaySnapshot saves or, with nil, clears the monitor layout.
func (s *StateStore) SetDisplaySnapshot(raw json.RawMessage) error {
	return s.update(func(st *State) { st.DisplaySnapshot = raw })
}

// AudioDefaultSink returns the output to restore after a session's virtual
// output, or "".
func (s *StateStore) AudioDefaultSink() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.AudioDefaultSink
}

// SetAudioDefaultSink saves or, with "", clears the output to restore.
func (s *StateStore) SetAudioDefaultSink(sink string) error {
	return s.update(func(st *State) { st.AudioDefaultSink = sink })
}

func (s *StateStore) update(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
	if err := writeJSON(s.path, s.st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return WriteFileAtomic(path, data)
}

// WriteFileAtomic writes data with mode 0600 through a temporary file and a
// rename, so a crash never leaves a truncated file. It creates the directory
// with mode 0700 when missing.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", name, err), tmp.Close(), os.Remove(name))
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync %s: %w", name, err), tmp.Close(), os.Remove(name))
	}
	if err := tmp.Close(); err != nil {
		return errors.Join(fmt.Errorf("close %s: %w", name, err), os.Remove(name))
	}
	if err := os.Rename(name, path); err != nil {
		return errors.Join(fmt.Errorf("rename %s: %w", path, err), os.Remove(name))
	}
	return nil
}
