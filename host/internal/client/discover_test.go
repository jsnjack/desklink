package client

import "testing"

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
