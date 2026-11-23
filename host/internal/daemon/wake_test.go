package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWakeNoteFor(t *testing.T) {
	root := t.TempDir()
	old := sysNet
	sysNet = root
	t.Cleanup(func() { sysNet = old })
	write := func(iface, rel, content string) {
		p := filepath.Join(root, iface, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("wlan0", "wireless/.keep", "")
	write("eth-random", "addr_assign_type", "3\n")
	write("eth-off", "addr_assign_type", "0\n")
	write("eth-off", "device/power/wakeup", "disabled\n")
	write("eth-on", "addr_assign_type", "0\n")
	write("eth-on", "device/power/wakeup", "enabled\n")
	write("virtual", "addr_assign_type", "0\n")

	tests := []struct {
		iface string
		want  string // substring of the note; "" means no note
	}{
		{"wlan0", "Wi-Fi"},
		{"eth-random", "changed MAC"},
		{"eth-off", "ethtool -s eth-off wol g"},
		{"eth-on", ""},
		{"virtual", ""},
	}
	for _, tt := range tests {
		t.Run(tt.iface, func(t *testing.T) {
			got := wakeNoteFor(tt.iface)
			if tt.want == "" && got != "" || tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("wakeNoteFor(%q) = %q, want it to contain %q", tt.iface, got, tt.want)
			}
		})
	}
}
