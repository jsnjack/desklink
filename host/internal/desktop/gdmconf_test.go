package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

// fedoraConf is GDM's custom.conf as Fedora ships it after GNOME Settings
// turned automatic login off again.
const fedoraConf = `# GDM configuration storage

[daemon]
# Uncomment the line below to force the login screen to use Xorg
#WaylandEnable=false
AutomaticLoginEnable=False
AutomaticLogin=jsn

[security]

[xdmcp]

[chooser]

[debug]
# Uncomment the line below to turn on debugging
#Enable=true
`

func writeConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "custom.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readConf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReadGDMLogin(t *testing.T) {
	cases := []struct {
		name, conf string
		want       GDMLogin
	}{
		{"Fedora's file with automatic login off", fedoraConf, GDMLogin{}},
		{"automatic login for this user", "[daemon]\nAutomaticLoginEnable=True\nAutomaticLogin=jsn\n", GDMLogin{Automatic: true}},
		{"GDM's other spellings of true", "[daemon]\nAutomaticLoginEnable=true\nAutomaticLogin=jsn\nTimedLoginEnable=1\nTimedLogin=jsn\n",
			GDMLogin{Automatic: true, Timed: true, TimedDelay: DefaultTimedLoginDelay}},
		{"timed login with its delay", "[daemon]\nTimedLoginEnable=True\nTimedLogin=jsn\nTimedLoginDelay=30\n", GDMLogin{Timed: true, TimedDelay: 30}},
		{"another user's", "[daemon]\nAutomaticLoginEnable=True\nAutomaticLogin=anna\nTimedLoginEnable=True\nTimedLogin=anna\n", GDMLogin{}},
		{"keys outside [daemon] do not count", "[debug]\nAutomaticLoginEnable=True\nAutomaticLogin=jsn\n", GDMLogin{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadGDMLogin(writeConf(t, c.conf), "jsn")
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
	t.Run("a missing file means off", func(t *testing.T) {
		got, err := ReadGDMLogin(filepath.Join(t.TempDir(), "custom.conf"), "jsn")
		if err != nil || got != (GDMLogin{}) {
			t.Errorf("got %+v, %v", got, err)
		}
	})
}

func TestSetGDMLoginOnChangesOnlyTheLoginKeys(t *testing.T) {
	path := writeConf(t, fedoraConf)
	if err := SetGDMLogin(path, "jsn", true); err != nil {
		t.Fatal(err)
	}
	want := `# GDM configuration storage

[daemon]
# Uncomment the line below to force the login screen to use Xorg
#WaylandEnable=false
AutomaticLoginEnable=True
AutomaticLogin=jsn
TimedLoginEnable=True
TimedLogin=jsn
TimedLoginDelay=10

[security]

[xdmcp]

[chooser]

[debug]
# Uncomment the line below to turn on debugging
#Enable=true
`
	if got := readConf(t, path); got != want {
		t.Errorf("custom.conf:\n%s\nwant:\n%s", got, want)
	}
	if l, _ := ReadGDMLogin(path, "jsn"); l != (GDMLogin{Automatic: true, Timed: true, TimedDelay: 10}) {
		t.Errorf("read back %+v", l)
	}
	if err := SetGDMLogin(path, "jsn", true); err != nil {
		t.Fatal(err)
	}
	if got := readConf(t, path); got != want {
		t.Errorf("turning it on twice changed the file:\n%s", got)
	}
}

func TestSetGDMLoginOnKeepsAChosenDelay(t *testing.T) {
	path := writeConf(t, "[daemon]\nTimedLoginDelay=30\n")
	if err := SetGDMLogin(path, "jsn", true); err != nil {
		t.Fatal(err)
	}
	if l, _ := ReadGDMLogin(path, "jsn"); l.TimedDelay != 30 {
		t.Errorf("delay %d, want the 30 s already set", l.TimedDelay)
	}
}

func TestSetGDMLoginOnWithoutADaemonSection(t *testing.T) {
	for name, conf := range map[string]string{"empty": "", "other sections only": "[debug]\nEnable=true\n"} {
		t.Run(name, func(t *testing.T) {
			path := writeConf(t, conf)
			if err := SetGDMLogin(path, "jsn", true); err != nil {
				t.Fatal(err)
			}
			want := "[daemon]\nAutomaticLoginEnable=True\nAutomaticLogin=jsn\nTimedLoginEnable=True\nTimedLogin=jsn\nTimedLoginDelay=10\n" + conf
			if got := readConf(t, path); got != want {
				t.Errorf("custom.conf:\n%s\nwant:\n%s", got, want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "custom.conf")
		if err := SetGDMLogin(path, "jsn", true); err != nil {
			t.Fatal(err)
		}
		if l, _ := ReadGDMLogin(path, "jsn"); !l.Automatic || !l.Timed {
			t.Errorf("read back %+v", l)
		}
	})
}

func TestSetGDMLoginOff(t *testing.T) {
	path := writeConf(t, "[daemon]\nAutomaticLoginEnable=True\nAutomaticLogin=jsn\nTimedLoginEnable=True\nTimedLogin=jsn\nTimedLoginDelay=10\n")
	if err := SetGDMLogin(path, "jsn", false); err != nil {
		t.Fatal(err)
	}
	want := "[daemon]\nAutomaticLoginEnable=False\nAutomaticLogin=jsn\nTimedLoginEnable=False\nTimedLogin=jsn\nTimedLoginDelay=10\n"
	if got := readConf(t, path); got != want {
		t.Errorf("custom.conf:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetGDMLoginOffLeavesAnotherUsersLogin(t *testing.T) {
	conf := "[daemon]\nAutomaticLoginEnable=True\nAutomaticLogin=anna\nTimedLoginEnable=True\nTimedLogin=anna\n"
	path := writeConf(t, conf)
	if err := SetGDMLogin(path, "jsn", false); err != nil {
		t.Fatal(err)
	}
	if got := readConf(t, path); got != conf {
		t.Errorf("custom.conf changed:\n%s", got)
	}
}

func TestSetGDMLoginRefusesWhatIsNotAUserName(t *testing.T) {
	for _, name := range []string{"", "jsn\nAutomaticLoginEnable=True", "a=b", "[daemon]"} {
		path := writeConf(t, fedoraConf)
		if err := SetGDMLogin(path, name, true); err == nil {
			t.Errorf("accepted %q", name)
		}
		if got := readConf(t, path); got != fedoraConf {
			t.Errorf("%q changed the file", name)
		}
	}
}
