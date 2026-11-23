package client

import (
	"strings"
	"testing"
)

func TestAHostIsLostOnlyFromEveryInterface(t *testing.T) {
	var seen announcements
	seen.add("DeskLink on laptop", 2) // Wi-Fi
	seen.add("DeskLink on laptop", 5) // VPN
	seen.add("DeskLink on laptop", 5) // announced again
	if seen.remove("DeskLink on laptop", 2) {
		t.Error("lost while still announced on the VPN")
	}
	if !seen.remove("DeskLink on laptop", 5) {
		t.Error("not lost after leaving every interface")
	}
	if seen.remove("DeskLink on desktop", 2) {
		t.Error("lost a host that was never found")
	}
}

func TestAnnouncementsNeedAUsableAddressAndAFingerprint(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	txt := [][]byte{[]byte("fp=" + strings.ToUpper(fp))}
	cases := []struct {
		name    string
		address string
		port    uint16
		txt     [][]byte
		ok      bool
	}{
		{"a LAN host", "192.168.178.11", 9443, txt, true},
		{"unspecified", "0.0.0.0", 9443, txt, false},
		{"broadcast", "255.255.255.255", 9443, txt, false},
		{"multicast", "224.0.0.251", 9443, txt, false},
		{"link-local", "169.254.3.4", 9443, txt, false},
		{"loopback", "127.0.0.1", 9443, txt, false},
		{"IPv6", "fd00::5", 9443, txt, false},
		{"no port", "192.168.178.11", 0, txt, false},
		{"no fingerprint", "192.168.178.11", 9443, nil, false},
		{"a short fingerprint", "192.168.178.11", 9443, [][]byte{[]byte("fp=ab12")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, ok := nearby("DeskLink on desk", c.address, c.port, c.txt)
			if ok != c.ok {
				t.Fatalf("ok %v, want %v", ok, c.ok)
			}
			if ok && (n.Fingerprint != fp || n.IP != c.address || n.Host() != "desk") {
				t.Errorf("nearby %+v", n)
			}
		})
	}
}
