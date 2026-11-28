package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWakeFor(t *testing.T) {
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
		iface   string
		problem string // substring of Problem; "" means none
		fix     string
	}{
		{"wlan0", "Wi-Fi", ""},
		{"eth-random", "changed MAC", ""},
		{"eth-off", "off on eth-off", "sudo ethtool -s eth-off wol g"},
		{"eth-on", "", ""},
		{"virtual", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.iface, func(t *testing.T) {
			got := wakeFor(tt.iface)
			if got.Interface != tt.iface {
				t.Errorf("Interface = %q, want %q", got.Interface, tt.iface)
			}
			if tt.problem == "" && got.Problem != "" || tt.problem != "" && !strings.Contains(got.Problem, tt.problem) {
				t.Errorf("Problem = %q, want it to contain %q", got.Problem, tt.problem)
			}
			if got.Fix != tt.fix {
				t.Errorf("Fix = %q, want %q", got.Fix, tt.fix)
			}
			if note := got.Note(); (note == "") != (tt.problem == "") || tt.fix != "" && !strings.Contains(note, "`"+tt.fix+"`") {
				t.Errorf("Note() = %q, want the problem and the fix in backquotes", note)
			}
		})
	}
}
