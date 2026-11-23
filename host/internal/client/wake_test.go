package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestMagicPacket(t *testing.T) {
	pkt, err := MagicPacket("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt) != 102 || !bytes.Equal(pkt[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		t.Fatalf("header or length wrong: %d bytes", len(pkt))
	}
	mac := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	if !bytes.Equal(pkt[6:], bytes.Repeat(mac, 16)) {
		t.Error("the MAC is not repeated sixteen times")
	}
	for _, bad := range []string{"", "aa:bb", "not a mac", "aa:bb:cc:dd:ee:ff:00:11"} {
		if _, err := MagicPacket(bad); err == nil {
			t.Errorf("MagicPacket(%q) accepted a bad MAC", bad)
		}
	}
}

func TestWakeTargets(t *testing.T) {
	got := wakeTargets("192.168.178.11")
	want := []string{"255.255.255.255", "192.168.178.255", "192.168.178.11"}
	if !slices.Equal(got, want) {
		t.Errorf("wakeTargets = %v, want %v", got, want)
	}
}

func TestWakeAndWaitWithoutMACLeavesTheErrorToConnect(t *testing.T) {
	// Nothing listens on port 1 of loopback, so the host is down; with no MAC
	// there is nothing to send and the caller's connect reports the failure.
	p := Profile{Name: "down", IP: "127.0.0.1", Port: 1}
	if _, err := WakeAndWait(context.Background(), p, func(string) { t.Error("reported waking without a MAC") }); err != nil {
		t.Fatalf("WakeAndWait = %v, want nil", err)
	}
}

func TestCandidatesAndWithAddresses(t *testing.T) {
	p := Profile{IP: "192.168.178.32", Addresses: []string{"192.168.1.139", "192.168.178.32", ""}}
	if got := p.Candidates(); !slices.Equal(got, []string{"192.168.178.32", "192.168.1.139"}) {
		t.Errorf("Candidates = %v", got)
	}
	q := p.WithAddresses("192.168.1.139", []string{"192.168.1.7", "192.168.1.139"})
	if q.IP != "192.168.1.139" || !slices.Equal(q.Addresses, []string{"192.168.178.32", "192.168.1.7"}) {
		t.Errorf("WithAddresses = %s %v, want the reached address first and the rest kept", q.IP, q.Addresses)
	}
}

func TestReachUsesAnyAnsweringAddress(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"desklinkd","hostname":"h","addresses":["10.9.9.9"]}`))
	}))
	defer srv.Close()
	host, portText, _ := strings.Cut(strings.TrimPrefix(srv.URL, "https://"), ":")
	port, _ := strconv.Atoi(portText)
	// The first address is a closed loopback port; the server answers on host.
	p := Profile{IP: "127.0.0.2", Addresses: []string{host}, Port: port}
	reached, h, err := Reach(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if reached.IP != host || h.Hostname != "h" || !slices.Contains(reached.Addresses, "10.9.9.9") || !slices.Contains(reached.Addresses, "127.0.0.2") {
		t.Errorf("Reach = %s %v %+v", reached.IP, reached.Addresses, h)
	}
}

func TestAbsorbMergesTheSameMachine(t *testing.T) {
	st, err := OpenStore(t.TempDir() + "/hosts.json")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := st.Add("Laptop", "192.168.178.32", 9443)
	home.Fingerprint, home.PeerID, home.PeerKey, home.MAC = "fp", "a", []byte{1}, "aa:bb:cc:dd:ee:ff"
	home.Levels = map[string]Level{"wifi": {Width: 1920, Height: 1200, FPS: 60}}
	if err := st.Update(home); err != nil {
		t.Fatal(err)
	}
	other, _ := st.Add("Desktop", "192.168.178.11", 9443)
	other.Fingerprint = "other"
	if err := st.Update(other); err != nil {
		t.Fatal(err)
	}
	office, _ := st.Add("192.168.1.139", "192.168.1.139", 9443)
	office.Fingerprint, office.PeerID, office.PeerKey = "FP", "b", []byte{2}
	got, err := st.Absorb(office)
	if err != nil {
		t.Fatal(err)
	}
	// The older entry survives with its name, levels and MAC, and takes the
	// new pairing and the address that just answered.
	if got.ID != home.ID || got.Name != "Laptop" || got.MAC != home.MAC || got.Levels["wifi"].FPS != 60 {
		t.Errorf("Absorb = %+v, want the home entry kept", got)
	}
	if got.PeerID != "b" || got.IP != "192.168.1.139" || !slices.Contains(got.Addresses, "192.168.178.32") {
		t.Errorf("Absorb = %+v, want the office pairing and both addresses", got)
	}
	var ids []string
	for _, h := range st.Hosts() {
		if h.Name != "Desktop" || h.ID == other.ID { // not the seeded default host
			ids = append(ids, h.ID)
		}
	}
	if !slices.Equal(ids, []string{home.ID, other.ID}) {
		t.Errorf("hosts after Absorb %v, want the laptop once and the desktop", ids)
	}
	if found, ok := st.FindByFingerprint("fp"); !ok || found.ID != home.ID {
		t.Errorf("FindByFingerprint = %+v %v", found, ok)
	}
}

func TestLearnKeepsChangesMadeMeanwhile(t *testing.T) {
	st, err := OpenStore(t.TempDir() + "/hosts.json")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.Add("Laptop", "192.168.178.32", 9443)
	// A level is saved while a health check of the old copy p is running.
	if err := st.SetLevel(p.ID, "wifi", Level{Width: 1280, Height: 800, FPS: 60}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Learn(p.ID, "192.168.1.139", []string{"10.0.0.2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.IP != "192.168.1.139" || !slices.Equal(got.Addresses, []string{"192.168.178.32", "10.0.0.2"}) {
		t.Errorf("Learn = %s %v", got.IP, got.Addresses)
	}
	if saved, _ := st.Host(p.ID); saved.Levels["wifi"].Width != 1280 {
		t.Errorf("Learn dropped the level saved meanwhile: %+v", saved.Levels)
	}
}
