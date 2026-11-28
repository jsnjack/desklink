package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"desklink/internal/config"
	"desklink/internal/doctor"
	dlnet "desklink/internal/net"
)

func testReport() doctor.Report {
	return doctor.Report{Groups: []doctor.Group{
		{Title: "Desk", Checks: []doctor.Check{
			{Status: doctor.OK, Name: "GNOME on Wayland", Detail: "session of jsn"},
			{Status: doctor.Problem, Name: "HEVC 10-bit", Detail: "not available", Fix: "sudo dnf swap mesa-va-drivers mesa-va-drivers-freeworld", Missing: "HEVC is not available"},
		}},
		{Title: "Staying up", Checks: []doctor.Check{
			{Status: doctor.Warning, Name: "Automatic login", Detail: "off; after a reboot someone has to log in", Fix: "desklinkd autologin on"},
		}},
	}}
}

func TestWriteReport(t *testing.T) {
	var plain strings.Builder
	if err := writeReport(&plain, palette{}, "DeskLink 0.64.0 on workstation", testReport()); err != nil {
		t.Fatal(err)
	}
	want := `DeskLink 0.64.0 on workstation

Desk
  ✓ GNOME on Wayland  session of jsn
  ✗ HEVC 10-bit       not available → sudo dnf swap mesa-va-drivers mesa-va-drivers-freeworld
Staying up
  ! Automatic login   off; after a reboot someone has to log in → desklinkd autologin on

1 problem, 1 warning. DeskLink works; HEVC is not available.
`
	if plain.String() != want {
		t.Errorf("got\n%s\nwant\n%s", plain.String(), want)
	}

	var coloured strings.Builder
	if err := writeReport(&coloured, palette{on: true}, "DeskLink 0.64.0 on workstation", testReport()); err != nil {
		t.Fatal(err)
	}
	if got := stripSGR(coloured.String()); got != want {
		t.Errorf("colour changed the text:\n%s", got)
	}
	for _, s := range []string{sgrBold + "Desk" + sgrReset, sgrRed + "✗" + sgrReset, sgrDim + "→ desklinkd autologin on" + sgrReset} {
		if !strings.Contains(coloured.String(), s) {
			t.Errorf("lacks %q", s)
		}
	}
	if !strings.HasSuffix(coloured.String(), "\n1 problem, 1 warning. DeskLink works; HEVC is not available.\n") {
		t.Error("the summary is coloured")
	}
}

func TestDoctorReadsDevices(t *testing.T) {
	now := time.Now()
	t.Run("from the running daemon", func(t *testing.T) {
		ctx := serveFakeDaemon(t, &fakeDaemon{peers: []dlnet.PeerInfo{
			{ID: "aa", Name: "Galaxy Tab S9", Connected: true},
			{ID: "bb", Name: "ThinkPad", LastSeen: now.Add(-time.Hour)},
			{ID: "cc", Name: "Removed", RemovalNotSaved: true},
		}})
		var f doctor.Facts
		readDaemonFacts(ctx, &f, config.Paths{Dir: t.TempDir()})
		if !f.Running {
			t.Error("the daemon answered, but doctor says it does not run")
		}
		var names []string
		for _, d := range f.Devices {
			names = append(names, d.Name)
		}
		if !slices.Equal(names, []string{"Galaxy Tab S9", "ThinkPad"}) || !f.Devices[0].Connected {
			t.Errorf("devices %+v; a removed one must not count", f.Devices)
		}
	})
	t.Run("from peers.json without a daemon", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
		paths := config.Paths{Dir: t.TempDir()}
		store, err := config.OpenPeers(paths.Peers())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Add(config.Peer{ID: "aa", Name: "Galaxy Tab S9", Paired: now, LastSeen: now.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		var f doctor.Facts
		readDaemonFacts(context.Background(), &f, paths)
		if f.Running || f.DevicesErr != nil || len(f.Devices) != 1 || f.Devices[0].Name != "Galaxy Tab S9" || f.Devices[0].LastSeen.IsZero() {
			t.Errorf("running %v, devices %+v, err %v", f.Running, f.Devices, f.DevicesErr)
		}
	})
}

func TestDaemonPorts(t *testing.T) {
	got, err := daemonPorts(config.Default())
	if err != nil || len(got) != 2 || got[0].String() != "9443/tcp" || got[1].String() != "9444/udp" {
		t.Errorf("default ports %v, %v", got, err)
	}
	cfg := config.Default()
	cfg.MediaAddr = "no port"
	if _, err := daemonPorts(cfg); err == nil || !strings.Contains(err.Error(), "media_addr") {
		t.Errorf("a broken media_addr gave %v", err)
	}
}
