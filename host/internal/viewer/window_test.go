package viewer

import (
	"strings"
	"testing"

	"desklink/internal/client"
)

func TestPairingStartSaysWhenTheCodeHasAnotherCertificate(t *testing.T) {
	codeFP, otherFP := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	code := client.PairingCode{IP: "192.168.178.11", Port: 9443, PIN: "123456", Fingerprint: codeFP}
	cases := []struct {
		name   string
		target *client.Profile
		nearby *client.Nearby
		warns  bool
	}{
		{"pasted on its own", nil, nil, false},
		{"announced with the same certificate", nil, &client.Nearby{Name: "DeskLink on desk", Fingerprint: strings.ToUpper(codeFP)}, false},
		{"announced with another certificate", nil, &client.Nearby{Name: "DeskLink on desk", Fingerprint: otherFP}, true},
		{"a saved host that is not paired", &client.Profile{Name: "Desktop"}, nil, false},
		{"a host paired with the same certificate", &client.Profile{Name: "Desktop", PeerID: "a", PeerKey: []byte{1}, Fingerprint: codeFP}, nil, false},
		{"a host paired with another certificate", &client.Profile{Name: "Desktop", PeerID: "a", PeerKey: []byte{1}, Fingerprint: otherFP}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := pairingStart(code, c.target, c.nearby)
			if !strings.Contains(msg, "192.168.178.11:9443") {
				t.Errorf("message %q does not name the code's address", msg)
			}
			if warns := strings.Contains(msg, "is not the one"); warns != c.warns {
				t.Errorf("message %q, want a warning: %v", msg, c.warns)
			}
		})
	}
}

func TestSecretClipboard(t *testing.T) {
	cases := []struct {
		name  string
		types []string
		want  bool
	}{
		{"plain text", []string{"text/plain;charset=utf-8", "UTF8_STRING"}, false},
		{"a password manager's secret", []string{"text/plain;charset=utf-8", "x-kde-passwordManagerHint"}, true},
		{"nothing", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := secretClipboard(c.types); got != c.want {
				t.Errorf("secretClipboard(%v) = %v, want %v", c.types, got, c.want)
			}
		})
	}
}
