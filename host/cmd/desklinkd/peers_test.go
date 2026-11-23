package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

type fakePeers struct {
	peers   []dlnet.PeerInfo
	removed []string
}

func (f *fakePeers) Peers(context.Context) ([]dlnet.PeerInfo, error) { return f.peers, nil }

func (f *fakePeers) RemovePeer(_ context.Context, id string) (dlnet.PeerInfo, error) {
	f.removed = append(f.removed, id)
	for _, p := range f.peers {
		if p.ID == id {
			return p, nil
		}
	}
	return dlnet.PeerInfo{}, dlnet.ErrUnknownPeer
}

func testPeers() []dlnet.PeerInfo {
	return []dlnet.PeerInfo{
		{ID: "a1b2c3d4e5f60708", Name: "Yauhen's Tab S9", Connected: true},
		{ID: "a1b2ffff00001111", Name: "laptop"},
		{ID: "9f9f9f9f9f9f9f9f", Name: "laptop"},
	}
}

func TestRemovePeersByIDPrefixOrName(t *testing.T) {
	f := &fakePeers{peers: testPeers()}
	var out bytes.Buffer
	if err := removePeers(context.Background(), f, []string{"a1b2c3", "yauhen's tab s9"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.removed, ",") != "a1b2c3d4e5f60708,a1b2c3d4e5f60708" {
		t.Errorf("removed %v", f.removed)
	}
	if !strings.Contains(out.String(), "its session ended") {
		t.Errorf("output %q does not say the session ended", out.String())
	}
}

func TestRemovePeersRefusesUnclearNames(t *testing.T) {
	for _, name := range []string{"laptop", "a1b2", "a1b", "nobody"} {
		f := &fakePeers{peers: testPeers()}
		// "a1b2c3" is fine, but one bad name must stop every removal.
		if err := removePeers(context.Background(), f, []string{"a1b2c3", name}, &bytes.Buffer{}); err == nil {
			t.Errorf("%q: removed without an error", name)
		}
		if len(f.removed) != 0 {
			t.Errorf("%q: removed %v despite the bad name", name, f.removed)
		}
	}
}

func TestWritePeers(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	peers := []dlnet.PeerInfo{
		{ID: "aa", Name: "Tab", Paired: now.Add(-72 * time.Hour), Connected: true},
		{ID: "bb", Name: "laptop", Paired: now.Add(-time.Hour), LastSeen: now.Add(-5 * time.Minute)},
		{ID: "cc", Name: "test", Paired: now.Add(-time.Hour)},
	}
	var out bytes.Buffer
	if err := writePeers(&out, peers, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"connected now", "5 min ago", "never"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := writePeers(&out, nil, now); err != nil || !strings.Contains(out.String(), "desklinkd pair") {
		t.Errorf("empty list: %q %v", out.String(), err)
	}
}
