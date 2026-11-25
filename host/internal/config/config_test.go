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
	t.Run("a relock owed survives a restart until it is cleared", func(t *testing.T) {
		if err := reopened.SetRelockSession(1764021480000000); err != nil {
			t.Fatal(err)
		}
		again, err := OpenState(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := again.RelockSession(); got != 1764021480000000 {
			t.Errorf("relock session %d after a restart", got)
		}
		if err := again.SetRelockSession(0); err != nil {
			t.Fatal(err)
		}
		if after, _ := OpenState(path); after.RelockSession() != 0 {
			t.Errorf("relock session %d after clearing", after.RelockSession())
		}
	})
}

// TestStateStoreRetryAfterAFailedSave: a relock whose save failed must not
// count as saved, or the retry would skip it and a crash would leave the
// next start without the record it needs to lock the desk again.
func TestStateStoreRetryAfterAFailedSave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "desklink")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	s, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	unblock := blockSaving(t, dir)
	if err := s.SetRelockSession(1764021480000000); err == nil {
		t.Fatal("saving into a blocked directory succeeded")
	}
	if got := s.RelockSession(); got != 0 {
		t.Errorf("the state holds %d after a failed save, want the saved 0", got)
	}
	unblock()
	if err := s.SetRelockSession(1764021480000000); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.RelockSession(); got != 1764021480000000 {
		t.Errorf("relock session %d after the retry and a restart, want it saved", got)
	}
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

func TestPeerStoreRemovalWhoseSaveFails(t *testing.T) {
	open := func(t *testing.T) (*PeerStore, string, string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "desklink")
		path := filepath.Join(dir, "peers.json")
		s, err := OpenPeers(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []Peer{
			{ID: "tab", Name: "Tab", Key: []byte{1}, Paired: time.Unix(10, 0).UTC()},
			{ID: "laptop", Name: "Laptop", Key: []byte{2}, Paired: time.Unix(20, 0).UTC()},
		} {
			if err := s.Add(p); err != nil {
				t.Fatal(err)
			}
		}
		return s, dir, path
	}
	onDisk := func(t *testing.T, path string) []Peer {
		t.Helper()
		s, err := OpenPeers(path)
		if err != nil {
			t.Fatal(err)
		}
		return s.List()
	}
	failRemove := func(t *testing.T, s *PeerStore, dir string) func() {
		t.Helper()
		unblock := blockSaving(t, dir)
		p, ok, err := s.Remove("tab")
		if err == nil || !ok || p.Name != "Tab" {
			t.Fatalf("Remove = %+v %v %v, want the peer and a save error", p, ok, err)
		}
		return unblock
	}

	t.Run("the peer is forgotten at once and listed as unsaved", func(t *testing.T) {
		s, dir, _ := open(t)
		defer failRemove(t, s, dir)()
		if _, ok := s.Get("tab"); ok {
			t.Error("Get still finds the removed peer, so it can still authenticate")
		}
		if got := s.List(); len(got) != 1 || got[0].ID != "laptop" {
			t.Errorf("List = %+v, want only the laptop", got)
		}
		if got := s.Unsaved(); len(got) != 1 || got[0].ID != "tab" {
			t.Errorf("Unsaved = %+v, want the tab", got)
		}
	})
	t.Run("removing it again saves the removal", func(t *testing.T) {
		s, dir, path := open(t)
		failRemove(t, s, dir)()
		if got := onDisk(t, path); len(got) != 2 {
			t.Fatalf("peers.json lists %+v; a restart now would pair the tab again", got)
		}
		if p, ok, err := s.Remove("tab"); err != nil || !ok || p.ID != "tab" {
			t.Fatalf("second Remove = %+v %v %v", p, ok, err)
		}
		if got := onDisk(t, path); len(got) != 1 || got[0].ID != "laptop" {
			t.Errorf("peers.json lists %+v after the retry, want only the laptop", got)
		}
		if got := s.Unsaved(); len(got) != 0 {
			t.Errorf("Unsaved = %+v after a successful save", got)
		}
		if _, ok, err := s.Remove("tab"); ok || err != nil {
			t.Errorf("a third Remove = %v %v, want not found", ok, err)
		}
	})
	t.Run("any later save completes it", func(t *testing.T) {
		s, dir, path := open(t)
		failRemove(t, s, dir)()
		if err := s.Touch("laptop", time.Unix(30, 0).UTC()); err != nil {
			t.Fatal(err)
		}
		if got := onDisk(t, path); len(got) != 1 || got[0].ID != "laptop" {
			t.Errorf("peers.json lists %+v, want only the laptop", got)
		}
		if got := s.Unsaved(); len(got) != 0 {
			t.Errorf("Unsaved = %+v after a successful save", got)
		}
	})
	t.Run("Save retries it", func(t *testing.T) {
		s, dir, path := open(t)
		failRemove(t, s, dir)()
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		if got := onDisk(t, path); len(got) != 1 || got[0].ID != "laptop" {
			t.Errorf("peers.json lists %+v, want only the laptop", got)
		}
	})
	t.Run("an empty store saves an empty list", func(t *testing.T) {
		s, _, path := open(t)
		for _, id := range []string{"tab", "laptop"} {
			if _, _, err := s.Remove(id); err != nil {
				t.Fatal(err)
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(bytes.TrimSpace(data)) != "[]" {
			t.Errorf("peers.json = %q, want []", data)
		}
	})
}

func TestSessionHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if recs, err := ReadSessions(path); err != nil || len(recs) != 0 {
		t.Fatalf("a missing history reads as %v, %v; want empty", recs, err)
	}
	start := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	for i := range MaxSessionRecords + 5 {
		rec := SessionRecord{Device: "Tablet", Start: start.Add(time.Duration(i) * time.Minute), Ended: "client left"}
		if err := AppendSession(path, rec); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := ReadSessions(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != MaxSessionRecords || !recs[0].Start.Equal(start.Add(5*time.Minute)) {
		t.Errorf("kept %d sessions starting %v, want the newest %d", len(recs), recs[0].Start, MaxSessionRecords)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendSession(path, SessionRecord{Device: "Laptop"}); err != nil {
		t.Fatalf("a history that cannot be parsed did not start over: %v", err)
	}
	if recs, _ := ReadSessions(path); len(recs) != 1 || recs[0].Device != "Laptop" {
		t.Errorf("history after starting over = %+v", recs)
	}
}
