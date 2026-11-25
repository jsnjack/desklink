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

func TestOwnScreenSizeFor(t *testing.T) {
	// A 2560x1600 laptop panel at 125 % is 2048x1280 logical pixels; the
	// header bar takes 46 of them in a window.
	cases := []struct {
		name         string
		fullscreen   bool
		picW, picH   int
		surfaceScale float64
		want         screenSize
		ok           bool
	}{
		{"fullscreen takes the monitor's device pixels", true, 2048, 1234, 1.25, screenSize{2560, 1600}, true},
		{"a window takes the picture's device pixels", false, 1600, 954, 1.25, screenSize{2000, 1192}, true},
		{"an odd side is made even", false, 801, 601, 1, screenSize{800, 600}, true},
		{"a tiny window still asks for what the host accepts", false, 200, 100, 1, screenSize{320, 320}, true},
		{"a window wider than the host takes shrinks both sides alike", false, 4000, 1000, 2, screenSize{4096, 1024}, true},
		{"a window without a size yet asks nothing", false, 0, 0, 1.25, screenSize{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, h, ok := ownScreenSizeFor(c.fullscreen, c.picW, c.picH, c.surfaceScale, 2048, 1280, 1.25)
			if (screenSize{w, h}) != c.want || ok != c.ok {
				t.Errorf("ownScreenSizeFor = %dx%d %v, want %dx%d %v", w, h, ok, c.want.w, c.want.h, c.ok)
			}
		})
	}
}

func TestScreenFollower(t *testing.T) {
	window, fullscreen := screenSize{2000, 1192}, screenSize{2560, 1600}
	var f screenFollower
	steps := []struct {
		name       string
		want, have screenSize
		ask        bool
	}{
		{"the window as the session started asks nothing", window, window, false},
		{"a new size seen once may still be changing", fullscreen, window, false},
		{"a new size seen twice is asked for", fullscreen, window, true},
		{"and only once while the host restarts the screen", fullscreen, window, false},
		{"the host's new screen asks nothing", fullscreen, fullscreen, false},
		{"a size that keeps changing asks nothing", screenSize{1900, 1100}, fullscreen, false},
		{"", screenSize{1800, 1000}, fullscreen, false},
		{"back to the screen's size asks nothing", fullscreen, fullscreen, false},
		{"a window size the host refused", window, fullscreen, false},
		{"", window, fullscreen, true},
		{"is not asked again", window, fullscreen, false},
		{"until the window was back at the screen's size", fullscreen, fullscreen, false},
		{"", window, fullscreen, false},
		{"", window, fullscreen, true},
	}
	for _, s := range steps {
		if got := f.look(s.want, s.have); got != s.ask {
			t.Errorf("%s: look(%v, %v) = %v, want %v", s.name, s.want, s.have, got, s.ask)
		}
	}
}
