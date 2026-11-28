package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/spf13/cobra"

	"desklink/internal/desktop"
)

var autologinCmd = &cobra.Command{
	Use:   "autologin [on|off]",
	Short: "Show or change whether GDM logs you in by itself",
	Long: `desklinkd runs in your desktop session, so it starts only once you are
logged in. "on" makes GDM log you in by itself: at once at boot (automatic
login), and 10 seconds after its login screen appears at any other time, as
after a crash or a logout (timed login). desklinkd then starts and locks the
session at once (lock_after_autologin in config.json), and a client can
connect with nobody at the desk. "off" turns both off.

Without an argument autologin says what is on. Changing it asks for an
administrator's password (pkexec): GNOME's dialog in the desktop session, a
prompt in the terminal over SSH. GDM reads the setting when it starts, so a
change applies from the next boot.`,
	Args:      cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs),
	ValidArgs: []string{"on", "off"},
	RunE: func(cmd *cobra.Command, args []string) error {
		if os.Geteuid() == 0 {
			return errors.New("run desklinkd autologin as the user GDM should log in, not as root; it asks for the administrator's password itself")
		}
		me, err := user.Current()
		if err != nil {
			return err
		}
		if len(args) == 1 {
			if err := runAsRoot(cmd.Context(), me.Username, args[0] == "on"); err != nil {
				return err
			}
		}
		l, err := desktop.ReadGDMLogin(desktop.GDMConf, me.Username)
		if err != nil {
			return err
		}
		locks := true
		if cfg, _, err := loadConfig(); err == nil {
			locks = cfg.LockAfterAutologin
		}
		text := autologinStatus(l, locks)
		if len(args) == 1 {
			text += "\nGDM reads this when it starts, so the change applies from the next boot."
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), text)
		return err
	},
}

var gdmLoginUser string

// gdmLoginCmd is the root half of `autologin on|off`, which runs it through
// pkexec: it writes GDM's automatic and timed login for one user.
var gdmLoginCmd = &cobra.Command{
	Use:       "gdm-login --user NAME on|off",
	Short:     "Write GDM's automatic and timed login for a user (root; desklinkd autologin runs it)",
	Hidden:    true,
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	ValidArgs: []string{"on", "off"},
	RunE: func(cmd *cobra.Command, args []string) error {
		if os.Geteuid() != 0 {
			return fmt.Errorf("gdm-login writes %s and must run as root; use `desklinkd autologin %s`", desktop.GDMConf, args[0])
		}
		if _, err := user.Lookup(gdmLoginUser); err != nil {
			return fmt.Errorf("user %q: %w", gdmLoginUser, err)
		}
		return desktop.SetGDMLogin(desktop.GDMConf, gdmLoginUser, args[0] == "on")
	},
}

func init() {
	gdmLoginCmd.Flags().StringVar(&gdmLoginUser, "user", "", "the user GDM logs in")
	_ = gdmLoginCmd.MarkFlagRequired("user")
	rootCmd.AddCommand(autologinCmd, gdmLoginCmd)
}

// runAsRoot runs this binary's gdm-login through pkexec, which asks for an
// administrator's password with the session's dialog, or in the terminal
// when there is none.
func runAsRoot(ctx context.Context, name string, on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := gdmLoginArgs(exe, name, on)
	c := exec.CommandContext(ctx, "pkexec", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("change GDM's login settings: %w\nWithout pkexec, run as root:\n  sudo %s", err, strings.Join(args, " "))
	}
	return nil
}

// gdmLoginArgs is the root command that turns login on or off for name.
func gdmLoginArgs(exe, name string, on bool) []string {
	state := "off"
	if on {
		state = "on"
	}
	return []string{exe, "gdm-login", "--user", name, state}
}

// autologinStatus explains what GDM's login settings mean for reaching this
// computer; locks is lock_after_autologin from config.json.
func autologinStatus(l desktop.GDMLogin, locks bool) string {
	var s string
	switch {
	case l.Automatic && l.Timed:
		s = fmt.Sprintf("Automatic login is on: GDM logs you in at boot, and %d s after its login screen appears\n"+
			"(after a crash or a logout, say), so desklinkd always starts by itself.", l.TimedDelay)
	case l.Timed:
		s = fmt.Sprintf("Timed login is on: GDM logs you in %d s after its login screen appears, at boot too,\n"+
			"so desklinkd always starts by itself.", l.TimedDelay)
	case l.Automatic:
		return "Automatic login is on at boot only: if your session ends (a crash or a logout), the login\n" +
			"screen waits for someone at the desk and desklinkd stays off. Run `desklinkd autologin on`\n" +
			"to have GDM log you back in too."
	default:
		return "Automatic login is off: after a reboot, a crash or a logout desklinkd starts only once\n" +
			"someone logs in at the desk. Turn it on with `desklinkd autologin on`."
	}
	if !locks {
		return s + "\nlock_after_autologin is false in config.json, so the session stays open at the desk."
	}
	return s + "\ndesklinkd locks the session at once. The login keyring stays locked until you unlock the\n" +
		"desk once with your password."
}

// readAutologin reads GDM's login settings for the current user.
func readAutologin() (desktop.GDMLogin, error) {
	me, err := user.Current()
	if err != nil {
		return desktop.GDMLogin{}, err
	}
	return desktop.ReadGDMLogin(desktop.GDMConf, me.Username)
}
