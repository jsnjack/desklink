package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUsableAddress(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"192.168.178.11", true},
		{"10.8.0.1", true},
		{"100.64.0.7", true},
		{"fd00::5", true},
		{"2001:db8::1", true},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"255.255.255.255", false},
		{"240.0.0.1", false},
		{"224.0.0.251", false},
		{"239.255.255.250", false},
		{"169.254.10.1", false},
		{"127.0.0.1", false},
		{"::", false},
		{"::1", false},
		{"ff02::fb", false},
		{"fe80::1", false},
		{"fe80::1%eth0", false},
		{"::ffff:0.0.0.0", false},
		{"desk.example", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.addr, func(t *testing.T) {
			if got := UsableAddress(c.addr); got != c.ok {
				t.Errorf("UsableAddress(%q) = %v, want %v", c.addr, got, c.ok)
			}
		})
	}
}

func TestWithAddressesKeepsTheNewestWithinTheCap(t *testing.T) {
	var old []string
	for i := range MaxAddresses - 1 {
		old = append(old, fmt.Sprintf("10.0.0.%d", i+1))
	}
	p := Profile{IP: "desk.lan", Addresses: old}
	got := p.WithAddresses("192.168.1.5", []string{"192.168.1.6", "0.0.0.0", "224.0.0.251", "10.0.0.1"})
	want := append([]string{"192.168.1.6", "10.0.0.1", "desk.lan"}, old[1:MaxAddresses-3]...)
	if got.IP != "192.168.1.5" || !slices.Equal(got.Addresses, want) {
		t.Errorf("WithAddresses = %s %v, want %v", got.IP, got.Addresses, want)
	}
	if n := len(got.Candidates()); n != MaxAddresses {
		t.Errorf("%d candidates, want at most %d", n, MaxAddresses)
	}
}

func TestConfirmAddsAnAnnouncedAddressOnlyWhenThePinnedHostAnswers(t *testing.T) {
	host, port, fp := tlsHealth(t, `{"name":"desklinkd","addresses":["192.0.2.10","255.255.255.255"]}`)
	st, err := OpenStore(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	paired, _ := st.Add("Desk", "192.0.2.1", port)
	paired.PeerID, paired.PeerKey, paired.Fingerprint = "a", []byte{1}, fp
	if err := st.Update(paired); err != nil {
		t.Fatal(err)
	}
	// The test server listens on loopback, which Confirm refuses as an
	// announced address, so these cases call confirm, the pinned check
	// behind it.
	t.Run("the paired certificate answers", func(t *testing.T) {
		reached, err := confirm(context.Background(), paired, host)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := st.Learn(reached)
		if err != nil {
			t.Fatal(err)
		}
		if saved.IP != host || !slices.Equal(saved.Addresses, []string{"192.0.2.10", "192.0.2.1"}) {
			t.Errorf("saved %s %v", saved.IP, saved.Addresses)
		}
	})
	cases := []struct {
		name string
		p    Profile
		ip   string
		want error
	}{
		{"another certificate answers", withFingerprint(paired, strings.Repeat("ab", 32)), host, ErrFingerprintMismatch},
		{"an unpaired host", withFingerprint(paired, ""), host, ErrNoFingerprint},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := confirm(context.Background(), c.p, c.ip); !errors.Is(err, c.want) {
				t.Errorf("err %v, want %v", err, c.want)
			}
		})
	}
	t.Run("an address no machine can answer at", func(t *testing.T) {
		for _, ip := range []string{"0.0.0.0", "255.255.255.255", "224.0.0.251", "169.254.1.1"} {
			if _, err := Confirm(context.Background(), paired, ip); err == nil {
				t.Errorf("Confirm accepted %s", ip)
			}
		}
	})
}

func withFingerprint(p Profile, fp string) Profile {
	p.Fingerprint = fp
	return p
}
