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
