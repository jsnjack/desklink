package main

import (
	"strings"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

func TestRenderPairing(t *testing.T) {
	text, err := renderPairing(dlnet.PairingTicket{
		IP: "192.168.178.11", Port: 9443, PIN: "123456", Fingerprint: "ab12", Expires: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, want string }{
		{"pairing JSON", `{"ip":"192.168.178.11","port":9443,"pin":"123456","fingerprint":"ab12"}`},
		{"QR code blocks", "█"},
		{"PIN line", "PIN 123456, valid until"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(text, c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, text)
			}
		})
	}
}
