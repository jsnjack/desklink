package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"desklink/internal/config"
	dlnet "desklink/internal/net"
)

// peerDaemon is a Daemon with only what pairing management needs: a peer
// store in a temporary directory and session control.
func peerDaemon(t *testing.T, peers ...config.Peer) (*Daemon, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "desklink")
	store, err := config.OpenPeers(filepath.Join(dir, "peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if err := store.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{peers: store}
	d.ctl = &sessionControl{paired: d.isPaired, lock: func(context.Context) error { return nil }}
	return d, dir
}

// blockSaving makes writing files in dir fail by putting a file where the
// directory was; the returned function puts the directory back unchanged.
func blockSaving(t *testing.T, dir string) func() {
	t.Helper()
	aside := dir + ".aside"
	if err := os.Rename(dir, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(aside, dir); err != nil {
			t.Fatal(err)
		}
	}
}

func savedPeerIDs(t *testing.T, dir string) []string {
	t.Helper()
	s, err := config.OpenPeers(filepath.Join(dir, "peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range s.List() {
		ids = append(ids, p.ID)
	}
	return ids
}

func listed(d *Daemon, id string) (dlnet.PeerInfo, bool) {
	for _, p := range d.ListPeers() {
		if p.ID == id {
			return p, true
		}
	}
	return dlnet.PeerInfo{}, false
}

func TestARemovalThatCannotBeSaved(t *testing.T) {
	tab := config.Peer{ID: "tablet", Name: "Tab S9", Key: []byte{1}, Paired: time.Unix(10, 0)}
	lap := config.Peer{ID: "laptop", Name: "Laptop", Key: []byte{2}, Paired: time.Unix(20, 0)}
	d, dir := peerDaemon(t, tab, lap)
	var cc cancelCounter
	if _, _, err := d.ctl.admit(tab, cc.cancel); err != nil {
		t.Fatal(err)
	}
	unblock := blockSaving(t, dir)

	info, err := d.RemovePeer(tab.ID)
	t.Run("is reported as incomplete", func(t *testing.T) {
		if err == nil || !strings.Contains(err.Error(), "not saved") || !strings.Contains(err.Error(), "desklinkd peers remove tablet") {
			t.Errorf("err = %v, want an incomplete removal that names the retry", err)
		}
		if !info.RemovalNotSaved || info.ID != tab.ID {
			t.Errorf("info = %+v", info)
		}
	})
	t.Run("still ends the session and refuses the device", func(t *testing.T) {
		if cc.n.Load() != 1 || !info.Connected {
			t.Errorf("session cancelled %d times, reported connected %v", cc.n.Load(), info.Connected)
		}
		if _, _, err := d.ctl.admit(tab, func() {}); !errors.Is(err, errRevoked) {
			t.Errorf("admit = %v, want errRevoked", err)
		}
	})
	t.Run("is listed until saved", func(t *testing.T) {
		if p, ok := listed(d, tab.ID); !ok || !p.RemovalNotSaved || p.Connected {
			t.Errorf("listed %+v %v, want marked as not saved", p, ok)
		}
		if p, ok := listed(d, lap.ID); !ok || p.RemovalNotSaved {
			t.Errorf("the laptop is listed as %+v %v", p, ok)
		}
	})

	unblock()
	t.Run("removing it again saves it", func(t *testing.T) {
		if _, err := d.RemovePeer(tab.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := listed(d, tab.ID); ok {
			t.Error("still listed after the removal was saved")
		}
		if ids := savedPeerIDs(t, dir); strings.Join(ids, ",") != "laptop" {
			t.Errorf("peers.json lists %v, want only the laptop", ids)
		}
		if _, err := d.RemovePeer(tab.ID); !errors.Is(err, dlnet.ErrUnknownPeer) {
			t.Errorf("removing it a third time: %v, want ErrUnknownPeer", err)
		}
	})
}

func TestStoppingSavesAnUnsavedRemoval(t *testing.T) {
	tab := config.Peer{ID: "tablet", Name: "Tab S9", Key: []byte{1}, Paired: time.Unix(10, 0)}
	d, dir := peerDaemon(t, tab)
	unblock := blockSaving(t, dir)
	if _, err := d.RemovePeer(tab.ID); err == nil {
		t.Fatal("the removal was saved despite the blocked directory")
	}
	d.saveRemovals() // still blocked: logs and keeps the removal pending
	if p, ok := listed(d, tab.ID); !ok || !p.RemovalNotSaved {
		t.Fatalf("listed %+v %v after a failed retry", p, ok)
	}
	unblock()
	d.saveRemovals()
	if ids := savedPeerIDs(t, dir); len(ids) != 0 {
		t.Errorf("peers.json lists %v after stopping, want nothing", ids)
	}
}
