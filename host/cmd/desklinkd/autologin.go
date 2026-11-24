package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/spf13/cobra"

	"desklink/internal/desktop"
)

// autologinTimeout leaves time to type the administrator's password.
const autologinTimeout = 2 * time.Minute

var autologinCmd = &cobra.Command{
	Use:   "autologin [on|off]",
	Short: "Show or change whether GDM logs you in automatically at boot",
	Long: `desklinkd runs in your desktop session, so after a reboot or a wake from
power-off it starts only once you are logged in. With automatic login GDM
opens the session at boot, desklinkd starts with it and locks the session at
once (lock_after_autologin in config.json), and a client can connect with
nobody at the desk.

Without an argument autologin says whether it is on. "on" and "off" change it
through AccountsService, as GNOME Settings does, which asks for an
administrator's password.`,
	Args:      cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs),
	ValidArgs: []string{"on", "off"},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), autologinTimeout)
		defer cancel()
		return withAccountsUser(ctx, func(user desktop.AccountsUser) error {
			if len(args) == 1 {
				on := args[0] == "on"
				if err := user.SetAutomaticLogin(ctx, on); errors.Is(err, desktop.ErrNotAuthorized) {
					return fmt.Errorf("%w\n%s", err, sudoAutologinHint(user.Path(), on))
				} else if err != nil {
					return err
				}
			}
			on, err := user.AutomaticLogin()
			if err != nil {
				return err
			}
			locks := true
			if cfg, _, err := loadConfig(); err == nil {
				locks = cfg.LockAfterAutologin
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), autologinStatus(on, locks))
			return err
		})
	},
}

func init() {
	rootCmd.AddCommand(autologinCmd)
}

// autologinStatus explains what the setting means for reaching this computer;
// locks is lock_after_autologin from config.json.
func autologinStatus(on, locks bool) string {
	if on && !locks {
		return "Automatic login is on: after a reboot GDM logs you in and desklinkd starts.\n" +
			"lock_after_autologin is false in config.json, so the session stays open at the desk."
	}
	if on {
		return "Automatic login is on: after a reboot GDM logs you in, desklinkd starts and locks the session.\n" +
			"The login keyring stays locked until you unlock the desk once with your password."
	}
	return "Automatic login is off: after a reboot or a wake from power-off desklinkd starts only once\n" +
		"someone logs in at the desk. Turn it on with `desklinkd autologin on`."
}

// autologinLine is the line `desklinkd pair` prints; empty when the setting
// could not be read.
func autologinLine(on bool, err error) string {
	switch {
	case err != nil:
		return ""
	case on:
		return "Automatic login: on"
	default:
		return "Automatic login: off; after a reboot desklinkd starts only once someone logs in at the desk " +
			"(turn it on with `desklinkd autologin on`)"
	}
}

// sudoAutologinHint is the same change made as root, which polkit allows
// without a password agent: for a shell over SSH, where nobody can answer
// GNOME's password dialog.
func sudoAutologinHint(path dbus.ObjectPath, on bool) string {
	return fmt.Sprintf("In the desktop session GNOME asks for an administrator's password; over SSH run:\n"+
		"  sudo busctl call org.freedesktop.Accounts %s org.freedesktop.Accounts.User SetAutomaticLogin b %t", path, on)
}

// readAutologin reports whether GDM logs the current user in at boot.
func readAutologin(ctx context.Context) (on bool, err error) {
	err = withAccountsUser(ctx, func(user desktop.AccountsUser) error {
		on, err = user.AutomaticLogin()
		return err
	})
	return on, err
}

// withAccountsUser runs fn with the current user's AccountsService object on
// a system bus connection of its own.
func withAccountsUser(ctx context.Context, fn func(desktop.AccountsUser) error) error {
	sys, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}
	defer func() { _ = sys.Close() }()
	user, err := desktop.FindAccountsUser(ctx, sys, os.Getuid())
	if err != nil {
		return err
	}
	return fn(user)
}
