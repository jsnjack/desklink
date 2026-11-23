package client

import (
	"bytes"
	"context"
	"slices"
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
	if err := WakeAndWait(context.Background(), p, func(string) { t.Error("reported waking without a MAC") }); err != nil {
		t.Fatalf("WakeAndWait = %v, want nil", err)
	}
}
