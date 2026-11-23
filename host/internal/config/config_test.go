package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name    string
		path    string
		wantErr bool
		check   func(Config) bool
	}{
		{"missing file gives defaults", filepath.Join(dir, "none.json"), false, func(c Config) bool {
			return c.ControlAddr == "0.0.0.0:9443" && c.MediaAddr == "0.0.0.0:9444" && c.PinnedApps[0].ID == "bunk-gui"
		}},
		{"fields override defaults", write("ip.json", `{"advertise_ip":"10.0.0.5","max_qp":30}`), false, func(c Config) bool {
			return c.AdvertiseIP == "10.0.0.5" && c.MaxQP == 30 && c.MinQP == 16
		}},
		{"bad subnet", write("subnet.json", `{"allowed_subnets":["nope"]}`), true, nil},
		{"inverted qp range", write("qp.json", `{"min_qp":30,"max_qp":20}`), true, nil},
		{"invalid JSON", write("bad.json", `{`), true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Load(c.path)
			if (err != nil) != c.wantErr {
				t.Fatalf("err %v", err)
			}
			if c.check != nil && !c.check(cfg) {
				t.Errorf("config %+v", cfg)
			}
		})
	}
}

func TestPeerStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "peers.json")
	s, err := OpenPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	p := Peer{ID: "abc", Name: "Tab", Key: []byte{1, 2, 3}, Paired: time.Unix(5, 0).UTC()}
	if err := s.Add(p); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("peers.json mode %v, want 0600", info.Mode().Perm())
	}
	reopened, err := OpenPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get("abc")
	if !ok || got.Name != "Tab" || string(got.Key) != string(p.Key) || !got.Paired.Equal(p.Paired) {
		t.Errorf("reloaded %+v", got)
	}
}

func TestPeerStoreListsTouchesAndRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	s, err := OpenPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Peer{
		{ID: "new", Name: "Laptop", Paired: time.Unix(20, 0).UTC()},
		{ID: "old", Name: "Tab", Paired: time.Unix(10, 0).UTC()},
	} {
		if err := s.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.List(); len(got) != 2 || got[0].ID != "old" || got[1].ID != "new" {
		t.Fatalf("List = %+v, want oldest pairing first", got)
	}
	seen := time.Unix(30, 0).UTC()
	if err := s.Touch("old", seen); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch("gone", seen); err != nil {
		t.Errorf("Touch of an unknown peer: %v", err)
	}
	if p, ok, err := s.Remove("new"); err != nil || !ok || p.Name != "Laptop" {
		t.Fatalf("Remove = %+v %v %v", p, ok, err)
	}
	if _, ok, err := s.Remove("new"); ok || err != nil {
		t.Errorf("removing twice = %v %v", ok, err)
	}
	reopened, err := OpenPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.List()
	if len(got) != 1 || got[0].ID != "old" || !got[0].LastSeen.Equal(seen) {
		t.Errorf("after reload %+v, want only the touched old peer", got)
	}
}

func TestStateStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisplaySnapshot(json.RawMessage(`{"layout_mode":1}`)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("display snapshot survives a restart", func(t *testing.T) {
		var compact bytes.Buffer
		if err := json.Compact(&compact, reopened.DisplaySnapshot()); err != nil {
			t.Fatal(err)
		}
		if compact.String() != `{"layout_mode":1}` {
			t.Errorf("snapshot %s", compact.String())
		}
	})
	t.Run("clearing the snapshot", func(t *testing.T) {
		if err := reopened.SetDisplaySnapshot(nil); err != nil {
			t.Fatal(err)
		}
		again, err := OpenState(path)
		if err != nil {
			t.Fatal(err)
		}
		if again.DisplaySnapshot() != nil {
			t.Errorf("state after clear: %s", again.DisplaySnapshot())
		}
	})
}
