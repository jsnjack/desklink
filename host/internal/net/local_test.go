package net

import (
	"context"
	stdnet "net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeLocal struct {
	peers []PeerInfo
}

func (f *fakeLocal) BeginPairing(context.Context) (PairingTicket, error) { return PairingTicket{}, nil }
func (f *fakeLocal) PairingStatus() PairingStatus                        { return PairingStatus{} }
func (f *fakeLocal) ListPeers() []PeerInfo                               { return f.peers }

func (f *fakeLocal) RemovePeer(id string) (PeerInfo, error) {
	for i, p := range f.peers {
		if p.ID == id {
			f.peers = append(f.peers[:i], f.peers[i+1:]...)
			return p, nil
		}
	}
	return PeerInfo{}, ErrUnknownPeer
}

func TestLocalAPIListsAndRemovesPeers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.sock")
	ctl := &fakeLocal{peers: []PeerInfo{
		{ID: "aa11", Name: "Tab S9", Paired: time.Unix(10, 0).UTC(), Connected: true},
		{ID: "bb22", Name: "laptop", Paired: time.Unix(20, 0).UTC()},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ready := make(chan struct{})
	go func() {
		if err := ServeLocal(ctx, path, ctl, func(stdnet.Addr) { close(ready) }); err != nil {
			t.Log(err)
		}
	}()
	<-ready
	c := NewLocalClient(path)
	peers, err := c.Peers(ctx)
	if err != nil || len(peers) != 2 || peers[0].Name != "Tab S9" || !peers[0].Connected {
		t.Fatalf("Peers = %+v, %v", peers, err)
	}
	removed, err := c.RemovePeer(ctx, "bb22")
	if err != nil || removed.Name != "laptop" {
		t.Fatalf("RemovePeer = %+v, %v", removed, err)
	}
	if _, err := c.RemovePeer(ctx, "bb22"); err == nil || !strings.Contains(err.Error(), ErrUnknownPeer.Error()) {
		t.Errorf("removing twice: %v", err)
	}
	if peers, _ := c.Peers(ctx); len(peers) != 1 {
		t.Errorf("%d peers left, want 1", len(peers))
	}
}
