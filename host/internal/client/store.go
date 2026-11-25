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
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"desklink/internal/config"
	dlnet "desklink/internal/net"
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
	// Addresses are the host's other addresses, on other networks or VPNs;
	// IP is the one that answered last.
	Addresses []string `json:"addresses,omitempty"`
	// Levels is the auto-quality level last used, by network (NetworkKey).
	Levels map[string]Level `json:"levels,omitempty"`
}

// Level is a stream size and frame rate, how a remembered auto-quality level
// is stored so it survives changes to the host's ladder.
type Level = dlnet.Level

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
	// Screen, Scale and KeepAwake are the user's choices for later sessions,
	// as Request takes them; empty and zero until the user makes one.
	Screen    string  `json:"screen,omitempty"`
	Scale     float64 `json:"scale,omitempty"`
	KeepAwake int     `json:"keep_awake,omitempty"`
	// LockAfter locks the desk when a session ends.
	LockAfter bool `json:"lock_after,omitempty"`
}

// Store holds the saved hosts and stream settings in hosts.json. The GUI and
// `desklink pair` may run at the same time, so every change reloads the file
// under an exclusive lock first, and every read reloads it.
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
// at the default address, saved on the first change.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		s.f.Hosts = []Profile{{ID: id, Name: "Desktop", IP: DefaultIP, Port: DefaultPort}}
		return s, nil
	}
	if err := s.reloadLocked(); err != nil {
		return nil, err
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

// reloadLocked reads the file; a missing file keeps what is in memory.
func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	s.f = f
	return nil
}

// read reloads the file for a reader; a failure keeps the last good copy.
func (s *Store) read() {
	if err := s.reloadLocked(); err != nil {
		slog.Warn("reload hosts", "err", err)
	}
}

// change applies fn to the freshly loaded file and saves it, holding an
// exclusive lock on hosts.json.lock throughout.
func (s *Store) change(fn func(*storeFile) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.path), err)
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			slog.Debug("close lock file", "err", err)
		}
	}()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", s.path, err)
	}
	if err := s.reloadLocked(); err != nil {
		return err
	}
	if err := fn(&s.f); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode hosts: %w", err)
	}
	if err := config.WriteFileAtomic(s.path, data); err != nil {
		return fmt.Errorf("save hosts: %w", err)
	}
	return nil
}

func hostIndex(f *storeFile, id string) int {
	return slices.IndexFunc(f.Hosts, func(p Profile) bool { return p.ID == id })
}

// Hosts returns the saved hosts.
func (s *Store) Hosts() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	return slices.Clone(s.f.Hosts)
}

// Host returns the host with the ID.
func (s *Store) Host(id string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	if i := hostIndex(&s.f, id); i >= 0 {
		return s.f.Hosts[i], true
	}
	return Profile{}, false
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
	return p, s.change(func(f *storeFile) error {
		f.Hosts = append(f.Hosts, p)
		return nil
	})
}

// Update replaces the host with the same ID.
func (s *Store) Update(p Profile) error {
	return s.change(func(f *storeFile) error {
		i := hostIndex(f, p.ID)
		if i < 0 {
			return fmt.Errorf("host %s is not saved", p.ID)
		}
		f.Hosts[i] = p
		return nil
	})
}

// Absorb saves p, just paired, and folds every saved host with p's
// certificate into one, so a machine paired again on another network stays
// one host. The oldest entry survives: it keeps its name and remembered
// levels, takes p's pairing, port, address and MAC, and gains the other
// entries' addresses (after p's) and levels; the others are deleted. A p that
// is not saved yet is added. It returns the host as saved.
func (s *Store) Absorb(p Profile) (Profile, error) {
	if p.Fingerprint == "" {
		return p, ErrNoFingerprint
	}
	var saved Profile
	err := s.change(func(f *storeFile) error {
		if hostIndex(f, p.ID) < 0 {
			f.Hosts = append(f.Hosts, p)
		}
		keep := -1
		addresses := [][]string{p.Candidates()}
		var levels map[string]Level
		kept := f.Hosts[:0]
		for _, h := range f.Hosts {
			if h.ID == p.ID {
				h = p
			}
			if !strings.EqualFold(h.Fingerprint, p.Fingerprint) {
				kept = append(kept, h)
				continue
			}
			addresses = append(addresses, h.Candidates())
			for k, v := range h.Levels {
				if _, ok := levels[k]; !ok {
					if levels == nil {
						levels = map[string]Level{}
					}
					levels[k] = v
				}
			}
			if keep < 0 {
				keep = len(kept)
				kept = append(kept, h)
			}
		}
		h := kept[keep]
		h.PeerID, h.PeerKey, h.Fingerprint, h.Port = p.PeerID, p.PeerKey, p.Fingerprint, p.Port
		if p.MAC != "" {
			h.MAC = p.MAC
		}
		h.IP, h.Addresses = p.IP, mergeAddresses(p.IP, addresses...)
		h.Levels = levels
		kept[keep] = h
		f.Hosts = kept
		saved = h
		return nil
	})
	return saved, err
}

// Learn records the addresses a host answered at and reported, as Reach or
// Confirm returned them in reached, merging them into the saved profile
// rather than overwriting it, so a change made meanwhile (a level, a new
// name) is kept. It refuses when the saved host is no longer paired with
// reached's certificate: only a pinned answer may add addresses. It returns
// the host as saved.
func (s *Store) Learn(reached Profile) (Profile, error) {
	var saved Profile
	err := s.change(func(f *storeFile) error {
		i := hostIndex(f, reached.ID)
		if i < 0 {
			return fmt.Errorf("host %s is not saved", reached.ID)
		}
		h := f.Hosts[i]
		if !h.Paired() || !strings.EqualFold(h.Fingerprint, reached.Fingerprint) {
			return fmt.Errorf("host %s is no longer paired with the certificate that answered", reached.ID)
		}
		h.IP, h.Addresses = reached.IP, mergeAddresses(reached.IP, reached.Addresses, h.Candidates())
		f.Hosts[i] = h
		saved = h
		return nil
	})
	return saved, err
}

// Delete removes the host with the ID.
func (s *Store) Delete(id string) error {
	return s.change(func(f *storeFile) error {
		f.Hosts = slices.DeleteFunc(f.Hosts, func(p Profile) bool { return p.ID == id })
		return nil
	})
}

// FindByFingerprint returns the saved host with this certificate, the same
// machine on whatever network it was paired.
func (s *Store) FindByFingerprint(fingerprint string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	i := slices.IndexFunc(s.f.Hosts, func(p Profile) bool {
		return fingerprint != "" && strings.EqualFold(p.Fingerprint, fingerprint)
	})
	if i < 0 {
		return Profile{}, false
	}
	return s.f.Hosts[i], true
}

// FindByAddress returns the host saved at ip:port.
func (s *Store) FindByAddress(ip string, port int) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	i := slices.IndexFunc(s.f.Hosts, func(p Profile) bool { return p.IP == ip && p.Port == port })
	if i < 0 {
		return Profile{}, false
	}
	return s.f.Hosts[i], true
}

// SetLevel remembers the auto-quality level a host used on a network.
func (s *Store) SetLevel(id, network string, level Level) error {
	return s.change(func(f *storeFile) error {
		i := hostIndex(f, id)
		if i < 0 {
			return fmt.Errorf("host %s is not saved", id)
		}
		if f.Hosts[i].Levels == nil {
			f.Hosts[i].Levels = map[string]Level{}
		}
		f.Hosts[i].Levels[network] = level
		return nil
	})
}

// Preset returns the selected preset.
func (s *Store) Preset() Preset {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	return PresetByID(s.f.Preset)
}

// SetPreset saves the selected preset.
func (s *Store) SetPreset(id string) error {
	return s.change(func(f *storeFile) error {
		f.Preset = id
		return nil
	})
}

// Codec returns the selected codec, empty for automatic.
func (s *Store) Codec() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	return s.f.Codec
}

// SetCodec saves the selected codec.
func (s *Store) SetCodec(c string) error {
	return s.change(func(f *storeFile) error {
		f.Codec = c
		return nil
	})
}

// Screen returns what sessions show (dlnet.ScreenOwn or ScreenMirror),
// empty until the user chose.
func (s *Store) Screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	return s.f.Screen
}

// SetScreen saves what sessions show.
func (s *Store) SetScreen(screen string) error {
	return s.change(func(f *storeFile) error {
		f.Screen = screen
		return nil
	})
}

// Scale returns the own screen's scale the user chose, zero until then.
func (s *Store) Scale() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	return s.f.Scale
}

// SetScale saves the own screen's scale.
func (s *Store) SetScale(scale float64) error {
	return s.change(func(f *storeFile) error {
		f.Scale = scale
		return nil
	})
}

// KeepAwake returns how long the computer stays awake after a session, a
// dlnet.Hello.KeepAwake value.
func (s *Store) KeepAwake() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	return s.f.KeepAwake
}

// SetKeepAwake saves how long the computer stays awake after a session.
func (s *Store) SetKeepAwake(minutes int) error {
	return s.change(func(f *storeFile) error {
		f.KeepAwake = minutes
		return nil
	})
}

// LockAfter returns whether the desk locks when a session ends.
func (s *Store) LockAfter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	return s.f.LockAfter
}

// SetLockAfter saves whether the desk locks when a session ends.
func (s *Store) SetLockAfter(lock bool) error {
	return s.change(func(f *storeFile) error {
		f.LockAfter = lock
		return nil
	})
}
