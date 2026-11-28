package daemon

import (
	"net/netip"
	"testing"

	"desklink/internal/config"
	dlnet "desklink/internal/net"
)

func TestAdvertiseIP(t *testing.T) {
	lan := dlnet.AllowList([]netip.Prefix{netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("10.0.0.0/8")})
	none := func(netip.Addr) bool { return false }
	tests := []struct {
		name       string
		advertise  string
		allow      func(netip.Addr) bool
		want       string
		wantErr    bool
		anyAllowed bool
	}{
		{"configured address wins", "192.168.178.11", none, "192.168.178.11", false, false},
		{"configured address must parse", "not-an-ip", none, "", true, false},
		{"nothing allowed is an error", "", none, "", true, false},
		{"detected address is allowed and not loopback", "", lan, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{opts: Options{Config: config.Config{AdvertiseIP: tt.advertise}}, allow: tt.allow}
			got, err := d.advertiseIP()
			if tt.anyAllowed {
				if err != nil {
					t.Skipf("this machine has no private IPv4 address: %v", err)
				}
				if got.IsLoopback() || !tt.allow(got) {
					t.Fatalf("advertiseIP() = %v, want an allowed non-loopback address", got)
				}
				return
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("advertiseIP() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got.String() != tt.want {
				t.Fatalf("advertiseIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

// AdvertiseIP finds, from the config alone, the address the daemon would.
func TestAdvertiseIPFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.AdvertiseIP = "192.168.178.11"
	if got, err := AdvertiseIP(cfg); err != nil || got.String() != "192.168.178.11" {
		t.Errorf("configured: %v, %v", got, err)
	}
	cfg.AdvertiseIP = ""
	cfg.AllowedSubnets = []string{"not a subnet"}
	if _, err := AdvertiseIP(cfg); err == nil {
		t.Error("a broken allowed_subnets gave an address")
	}
	cfg = config.Default()
	subnets, err := cfg.Subnets()
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{opts: Options{Config: cfg}, allow: dlnet.AllowList(subnets)}
	want, wantErr := d.advertiseIP()
	if got, err := AdvertiseIP(cfg); got != want || (err == nil) != (wantErr == nil) {
		t.Errorf("AdvertiseIP = %v, %v; the daemon finds %v, %v", got, err, want, wantErr)
	}
}

func TestIsBridge(t *testing.T) {
	for name, want := range map[string]bool{"lxdbr0": true, "docker0": true, "virbr0": true, "br-1a2b": true, "eno1": false, "wlp194s0": false, "wg0": false} {
		if got := isBridge(name); got != want {
			t.Errorf("isBridge(%q) = %v, want %v", name, got, want)
		}
	}
}
