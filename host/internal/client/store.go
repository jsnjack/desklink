// Package client is the laptop side of DeskLink without any GUI: saved
// hosts, pairing, the control WebSocket, the UDP transport and frame
// reassembly. The viewer package draws it with GTK.
package client

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"desklink/internal/config"
)

// AppName names the client's directory under the XDG config directory.
const AppName = "desklink-client"

// Defaults for a new host.
const (
	DefaultIP   = "192.168.178.11"
	DefaultPort = 9443
)

// Profile is a saved host. PeerKey is the secret from pairing; the file that
// holds it is written with mode 0600.
type Profile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	PeerID      string `json:"peer_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	PeerKey     []byte `json:"peer_key,omitempty"`
	MAC         string `json:"mac,omitempty"`
	// Levels is the auto-quality level last used, by network (NetworkKey).
	Levels map[string]int `json:"levels,omitempty"`
}

// Paired reports whether the profile holds pairing credentials.
func (p Profile) Paired() bool { return p.PeerID != "" && p.Fingerprint != "" && len(p.PeerKey) > 0 }

// Preset is a stream size, quantizer and frame-rate cap. With Auto the size
// and rate are the top of the host's quality ladder.
type Preset struct {
	ID     string
	Label  string
	Width  int
	Height int
	QP     int
	FPS    int
	Auto   bool
}

// Presets match the Android app's; Auto is the default.
var Presets = []Preset{
	{ID: "auto", Label: "Auto (adapts to the connection)", Width: 2560, Height: 1600, QP: 18, FPS: 120, Auto: true},
	{ID: "crisp", Label: "Crisp 2560×1600", Width: 2560, Height: 1600, QP: 18, FPS: 120},
	{ID: "balanced", Label: "Balanced 1920×1200", Width: 1920, Height: 1200, QP: 20, FPS: 60},
	{ID: "office", Label: "Office Saver QP24", Width: 1920, Height: 1200, QP: 24, FPS: 60},
}

// PresetByID returns the preset with the ID, or the first one.
func PresetByID(id string) Preset {
	if i := slices.IndexFunc(Presets, func(p Preset) bool { return p.ID == id }); i >= 0 {
		return Presets[i]
	}
	return Presets[0]
}

type storeFile struct {
	Hosts  []Profile `json:"hosts"`
	Preset string    `json:"preset"`
	Codec  string    `json:"codec"`
}

// Store holds the saved hosts and stream settings in hosts.json.
type Store struct {
	path string
	mu   sync.Mutex
	f    storeFile
}

// DefaultStorePath is ~/.config/desklink-client/hosts.json.
func DefaultStorePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(base, AppName, "hosts.json"), nil
}

// OpenStore loads the store. A missing file starts with one unpaired host
// at the default address.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		id, err := newID()
		if err != nil {
			return nil, err
		}
		s.f.Hosts = []Profile{{ID: id, Name: "Desktop", IP: DefaultIP, Port: DefaultPort}}
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s.f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random host ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Hosts returns a copy of the saved hosts.
func (s *Store) Hosts() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.f.Hosts)
}

// Host returns the host with the ID.
func (s *Store) Host(id string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.f.Hosts, func(p Profile) bool { return p.ID == id })
	if i < 0 {
		return Profile{}, false
	}
	return s.f.Hosts[i], true
}

// Add saves a new host and returns it.
func (s *Store) Add(name, ip string, port int) (Profile, error) {
	id, err := newID()
	if err != nil {
		return Profile{}, err
	}
	if name == "" {
		name = ip
	}
	p := Profile{ID: id, Name: name, IP: ip, Port: port}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Hosts = append(s.f.Hosts, p)
	return p, s.saveLocked()
}

// Update replaces the host with the same ID.
func (s *Store) Update(p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.f.Hosts, func(h Profile) bool { return h.ID == p.ID })
	if i < 0 {
		return fmt.Errorf("host %s is not saved", p.ID)
	}
	s.f.Hosts[i] = p
	return s.saveLocked()
}

// Delete removes the host with the ID.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Hosts = slices.DeleteFunc(s.f.Hosts, func(p Profile) bool { return p.ID == id })
	return s.saveLocked()
}

// FindByAddress returns the host saved at ip:port.
func (s *Store) FindByAddress(ip string, port int) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.f.Hosts, func(p Profile) bool { return p.IP == ip && p.Port == port })
	if i < 0 {
		return Profile{}, false
	}
	return s.f.Hosts[i], true
}

// SetLevel remembers the auto-quality level a host used on a network.
func (s *Store) SetLevel(id, network string, level int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.f.Hosts, func(p Profile) bool { return p.ID == id })
	if i < 0 {
		return fmt.Errorf("host %s is not saved", id)
	}
	if s.f.Hosts[i].Levels[network] == level {
		return nil
	}
	if s.f.Hosts[i].Levels == nil {
		s.f.Hosts[i].Levels = map[string]int{}
	}
	s.f.Hosts[i].Levels[network] = level
	return s.saveLocked()
}

// Preset returns the selected preset.
func (s *Store) Preset() Preset {
	s.mu.Lock()
	defer s.mu.Unlock()
	return PresetByID(s.f.Preset)
}

// SetPreset saves the selected preset.
func (s *Store) SetPreset(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Preset = id
	return s.saveLocked()
}

// Codec returns the selected codec, empty for automatic.
func (s *Store) Codec() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Codec
}

// SetCodec saves the selected codec.
func (s *Store) SetCodec(c string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Codec = c
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode hosts: %w", err)
	}
	if err := config.WriteFileAtomic(s.path, data); err != nil {
		return fmt.Errorf("save hosts: %w", err)
	}
	return nil
}
