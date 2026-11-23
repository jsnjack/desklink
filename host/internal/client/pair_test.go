package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// codeFor starts a pairing window on h and returns the code desklinkd pair
// would print for it.
func codeFor(t *testing.T, h *fakeHost) PairingCode {
	t.Helper()
	pin, _, err := h.pairer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	return PairingCode{IP: h.addr.Addr().String(), Port: int(h.addr.Port()), PIN: pin, Fingerprint: h.fp}
}

func TestPairWithCode(t *testing.T) {
	h := startFakeHost(t)
	st, err := OpenStore(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("a wrong PIN saves nothing", func(t *testing.T) {
		code := codeFor(t, h)
		wrong := "000000"
		if code.PIN == wrong {
			wrong = "111111"
		}
		code.PIN = wrong
		before := len(st.Hosts())
		if _, err := PairWithCode(ctx, st, code, "", "laptop"); err == nil {
			t.Fatal("paired with a wrong PIN")
		}
		if n := len(st.Hosts()); n != before {
			t.Errorf("%d hosts after a failed pairing, want %d", n, before)
		}
	})

	var first Profile
	t.Run("pairs and saves what the pinned /health reports", func(t *testing.T) {
		first, err = PairWithCode(ctx, st, codeFor(t, h), "", "laptop")
		if err != nil {
			t.Fatal(err)
		}
		if !first.Paired() || first.Fingerprint != h.fp || first.Name != "desk" || first.MAC != fakeMAC {
			t.Errorf("saved %+v, want paired with the code's certificate, named and with the MAC from /health", first)
		}
		if first.IP != h.addr.Addr().String() || !slices.Equal(first.Addresses, []string{"192.0.2.10"}) {
			t.Errorf("addresses %s %v, want the code's and only the usable one /health reported", first.IP, first.Addresses)
		}
		if _, ok := h.peers.Get(first.PeerID); !ok {
			t.Error("the host does not know the pairing")
		}
	})

	t.Run("pairing the same host again keeps one entry", func(t *testing.T) {
		again, err := PairWithCode(ctx, st, codeFor(t, h), first.ID, "laptop")
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID || again.PeerID == first.PeerID {
			t.Errorf("re-paired %+v, want entry %s with a new pairing", again, first.ID)
		}
		if n := countFingerprint(st, h.fp); n != 1 {
			t.Errorf("%d entries with the host's certificate, want 1", n)
		}
	})

	t.Run("the saved host with the code's certificate wins over the one chosen", func(t *testing.T) {
		other := addPaired(t, st, "Other desktop", "192.0.2.77", strings.Repeat("cd", 32))
		got, err := PairWithCode(ctx, st, codeFor(t, h), other.ID, "laptop")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != first.ID {
			t.Errorf("paired into %s, want the entry with the code's certificate %s", got.ID, first.ID)
		}
		if kept, _ := st.Host(other.ID); kept.Fingerprint != other.Fingerprint || kept.PeerID != other.PeerID {
			t.Errorf("the other host became %+v", kept)
		}
	})

	t.Run("the chosen host takes a new certificate from the code", func(t *testing.T) {
		// The desktop was reinstalled: a new certificate the store has not seen.
		reinstalled := startFakeHost(t)
		old := addPaired(t, st, "Reinstalled", "192.0.2.78", strings.Repeat("ef", 32))
		got, err := PairWithCode(ctx, st, codeFor(t, reinstalled), old.ID, "laptop")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != old.ID || got.Name != "Reinstalled" || got.Fingerprint != reinstalled.fp {
			t.Errorf("paired %+v, want entry %s with the new certificate", got, old.ID)
		}
	})
}

// addPaired saves a host paired with the certificate fp.
func addPaired(t *testing.T, st *Store, name, ip, fp string) Profile {
	t.Helper()
	p, err := st.Add(name, ip, 9443)
	if err != nil {
		t.Fatal(err)
	}
	p.PeerID, p.PeerKey, p.Fingerprint = name, []byte{9}, fp
	if err := st.Update(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func countFingerprint(st *Store, fp string) int {
	n := 0
	for _, p := range st.Hosts() {
		if strings.EqualFold(p.Fingerprint, fp) {
			n++
		}
	}
	return n
}

func TestPairingNeedsTheCertificateFingerprint(t *testing.T) {
	// An impostor answers at the code's address with its own certificate: it
	// must not get a single request, or it could test every PIN offline.
	var requests atomic.Int32
	impostor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"name":"desklinkd","addresses":["192.0.2.66"]}`))
	}))
	defer impostor.Close()
	host, portText, _ := strings.Cut(strings.TrimPrefix(impostor.URL, "https://"), ":")
	port, _ := strconv.Atoi(portText)
	real := startFakeHost(t)
	st, err := OpenStore(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cases := []struct {
		name string
		run  func() error
		want error
	}{
		{"a code for another certificate", func() error {
			code := codeFor(t, real)
			code.IP, code.Port = host, port
			_, err := PairWithCode(ctx, st, code, "", "laptop")
			return err
		}, ErrFingerprintMismatch},
		{"a code without a fingerprint", func() error {
			code := codeFor(t, real)
			code.IP, code.Port, code.Fingerprint = host, port, ""
			_, err := PairWithCode(ctx, st, code, "", "laptop")
			return err
		}, ErrNoFingerprint},
		{"Pair without a fingerprint", func() error {
			_, err := Pair(ctx, host, port, "123456", "", "laptop")
			return err
		}, ErrNoFingerprint},
		{"Health without a fingerprint", func() error {
			_, err := Health(ctx, host, port, "")
			return err
		}, ErrNoFingerprint},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.run(); !errors.Is(err, c.want) {
				t.Errorf("err %v, want %v", err, c.want)
			}
		})
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("the impostor got %d requests", n)
	}
	if n := countFingerprint(st, real.fp); n != 0 || len(st.Hosts()) != 1 {
		t.Errorf("hosts %+v, want only the default one", st.Hosts())
	}
}

func TestHealthReplyIsLimited(t *testing.T) {
	huge := `{"name":"desklinkd","hostname":"` + strings.Repeat("a", 4*maxReplyBytes) + `"}`
	host, port, fp := tlsHealth(t, huge)
	if _, err := Health(context.Background(), host, port, fp); err == nil {
		t.Error("decoded a /health reply larger than the limit")
	}
}

func TestHostName(t *testing.T) {
	cases := []struct {
		name, reported, want string
	}{
		{"plain", "desk", "desk"},
		{"control characters dropped", "de\x1b[31msk\n", "de[31msk"},
		{"empty falls back", " \t", "10.0.0.5"},
		{"long names are cut", strings.Repeat("é", 100), strings.Repeat("é", maxNameRunes)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hostName(c.reported, "10.0.0.5"); got != c.want {
				t.Errorf("hostName(%q) = %q, want %q", c.reported, got, c.want)
			}
		})
	}
}
