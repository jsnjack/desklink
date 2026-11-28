package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkKind(t *testing.T) {
	sysfs := t.TempDir()
	for iface, link := range map[string]string{"wlp1s0": "1\n", "enp3s0": "1\n", "wg0": "65534\n", "ppp0": "512\n", "lo": "772\n"} {
		if err := os.MkdirAll(filepath.Join(sysfs, iface), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sysfs, iface, "type"), []byte(link), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, iface, want string
	}{
		{"Wi-Fi is a LAN", "wlp1s0", NetworkLAN},
		{"Ethernet is a LAN", "enp3s0", NetworkLAN},
		{"WireGuard has no link layer, so it is a VPN", "wg0", NetworkVPN},
		{"PPP is a VPN", "ppp0", NetworkVPN},
		{"loopback is neither", "lo", ""},
		{"an interface that is gone is unknown", "tun9", ""},
		{"no route to the host is unknown", defaultNetwork, ""},
		{"a name with a slash is not looked up", "../wg0", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := networkKind(sysfs, c.iface); got != c.want {
				t.Errorf("networkKind(%q) = %q, want %q", c.iface, got, c.want)
			}
		})
	}
}
