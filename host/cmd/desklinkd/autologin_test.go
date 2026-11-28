package main

import (
	"slices"
	"strings"
	"testing"

	"desklink/internal/desktop"
)

var (
	loginOff       = desktop.GDMLogin{}
	loginAtBoot    = desktop.GDMLogin{Automatic: true}
	loginTimedOnly = desktop.GDMLogin{Timed: true, TimedDelay: 10}
	loginOn        = desktop.GDMLogin{Automatic: true, Timed: true, TimedDelay: 10}
)

func TestAutologinStatus(t *testing.T) {
	cases := []struct {
		name  string
		l     desktop.GDMLogin
		locks bool
		want  []string
		not   []string
	}{
		{"off says how to turn it on", loginOff, true, []string{"desklinkd autologin on"}, []string{"locks the session"}},
		{"at boot only says a crash leaves it off", loginAtBoot, true, []string{"at boot only", "crash", "desklinkd autologin on"}, nil},
		{"on says it always comes back, locked", loginOn, true, []string{"at boot", "10 s after", "locks the session", "keyring"}, nil},
		{"timed only counts as on", loginTimedOnly, true, []string{"10 s after", "locks the session"}, nil},
		{"without lock_after_autologin no lock is promised", loginOn, false, []string{"stays open"}, []string{"locks the session"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := autologinStatus(c.l, c.locks)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("lacks %q: %q", w, got)
				}
			}
			for _, n := range c.not {
				if strings.Contains(got, n) {
					t.Errorf("must not say %q: %q", n, got)
				}
			}
		})
	}
}

// pkexec runs the root half of the change, and the same arguments make the
// sudo line printed when pkexec fails.
func TestGDMLoginArgs(t *testing.T) {
	if got, want := gdmLoginArgs("/usr/bin/desklinkd", "jsn", true), []string{"/usr/bin/desklinkd", "gdm-login", "--user", "jsn", "on"}; !slices.Equal(got, want) {
		t.Errorf("on: %q, want %q", got, want)
	}
	if got := gdmLoginArgs("/usr/bin/desklinkd", "jsn", false); got[len(got)-1] != "off" {
		t.Errorf("off: %q", got)
	}
}

func TestGDMLoginRunsOnlyAsRootWithAUser(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"gdm-login"})
	if err != nil || cmd != gdmLoginCmd {
		t.Fatalf("gdm-login is not a command: %v", err)
	}
	if !gdmLoginCmd.Hidden {
		t.Error("gdm-login is for pkexec and must not be listed in help")
	}
	if f := gdmLoginCmd.Flags().Lookup("user"); f == nil || f.Annotations["cobra_annotation_bash_completion_one_required_flag"] == nil {
		t.Error("--user must be required")
	}
}
