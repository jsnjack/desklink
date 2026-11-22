package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"desklink/internal/config"
)

func TestParseDesktopEntry(t *testing.T) {
	cases := []struct {
		name     string
		entry    string
		wantOK   bool
		wantName string
		wantExec string
	}{
		{"application", "[Desktop Entry]\nType=Application\nName=Files\nName[de]=Dateien\nExec=nautilus --new-window %U\n", true, "Files", "nautilus --new-window"},
		{"no display", "[Desktop Entry]\nType=Application\nName=Helper\nExec=helper\nNoDisplay=true\n", false, "", ""},
		{"hidden", "[Desktop Entry]\nType=Application\nName=Old\nExec=old\nHidden=true\n", false, "", ""},
		{"link entry", "[Desktop Entry]\nType=Link\nName=Site\nURL=https://example.com\n", false, "", ""},
		{"action groups are ignored", "[Desktop Entry]\nType=Application\nName=Term\nExec=term\n[Desktop Action new]\nName=New\nExec=term --new\n", true, "Term", "term"},
		{"escaped percent", "[Desktop Entry]\nType=Application\nName=P\nExec=calc 100%%\n", true, "P", "calc 100%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app, ok, err := parseDesktopEntry(strings.NewReader(c.entry))
			if err != nil {
				t.Fatal(err)
			}
			if ok != c.wantOK {
				t.Fatalf("ok %v, want %v", ok, c.wantOK)
			}
			if ok && (app.Name != c.wantName || app.Exec != c.wantExec) {
				t.Errorf("got %q %q", app.Name, app.Exec)
			}
		})
	}
}

func writeEntry(t *testing.T, dir, id, name, execLine string) {
	t.Helper()
	body := "[Desktop Entry]\nType=Application\nName=" + name + "\nExec=" + execLine + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".desktop"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanApps(t *testing.T) {
	system, user := t.TempDir(), t.TempDir()
	writeEntry(t, system, "org.gnome.Nautilus", "Files", "nautilus %U")
	writeEntry(t, system, "io.github.jsnjack.BunkGui", "bunk-gui", "bunk-gui")
	writeEntry(t, system, "firefox", "Firefox", "firefox %u")
	writeEntry(t, user, "firefox", "Firefox Nightly", "firefox-nightly")
	pinned := []config.PinnedApp{{ID: "bunk-gui", Name: "Bunk Terminal", Exec: "bunk-gui"}, {ID: "absent", Name: "Absent", Exec: "absent-app"}}

	apps := ScanApps([]string{system, user, filepath.Join(system, "missing")}, pinned)
	var names []string
	for _, a := range apps {
		names = append(names, a.Name)
	}
	if got := strings.Join(names, ","); got != "Bunk Terminal,Absent,Files,Firefox Nightly" {
		t.Fatalf("order %s", got)
	}
	t.Run("pinned app takes over the matching entry", func(t *testing.T) {
		if apps[0].ID != "bunk-gui" || !strings.HasSuffix(apps[0].path, "io.github.jsnjack.BunkGui.desktop") {
			t.Errorf("pinned app %+v", apps[0])
		}
	})
	t.Run("pinned app without an entry runs its Exec", func(t *testing.T) {
		if apps[1].path != "" || apps[1].Exec != "absent-app" {
			t.Errorf("pinned app %+v", apps[1])
		}
	})
	t.Run("user entries override system entries", func(t *testing.T) {
		if apps[3].Exec != "firefox-nightly" {
			t.Errorf("firefox %+v", apps[3])
		}
	})
	t.Run("find by id", func(t *testing.T) {
		a, err := Find(apps, "org.gnome.Nautilus", "")
		if err != nil || a.Name != "Files" {
			t.Errorf("got %+v, %v", a, err)
		}
	})
	t.Run("find by exec", func(t *testing.T) {
		a, err := Find(apps, "", "bunk-gui")
		if err != nil || a.ID != "bunk-gui" {
			t.Errorf("got %+v, %v", a, err)
		}
	})
	t.Run("arbitrary commands are refused", func(t *testing.T) {
		if _, err := Find(apps, "", "rm -rf ~"); !errors.Is(err, ErrUnknownApp) {
			t.Errorf("got %v, want ErrUnknownApp", err)
		}
	})
}
