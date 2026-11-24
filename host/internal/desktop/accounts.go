package desktop

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	accountsBus  = "org.freedesktop.Accounts"
	accountsPath = "/org/freedesktop/Accounts"
	accountsUser = accountsBus + ".User"
	// accountsDenied is AccountsService's answer when polkit refuses.
	accountsDenied = accountsBus + ".Error.PermissionDenied"
)

// ErrNotAuthorized means polkit refused to change the login settings: no
// administrator's password was given, or there is no agent to ask for one
// (over SSH, say).
var ErrNotAuthorized = errors.New("not authorized to change the login screen settings")

// AccountsUser is a user's object in AccountsService, which keeps GDM's
// login settings (/etc/gdm/custom.conf) the way GNOME Settings does.
type AccountsUser struct {
	obj dbus.BusObject
}

// FindAccountsUser returns the AccountsService object of the user with uid.
func FindAccountsUser(ctx context.Context, sys *dbus.Conn, uid int) (AccountsUser, error) {
	var path dbus.ObjectPath
	err := sys.Object(accountsBus, accountsPath).CallWithContext(ctx, accountsBus+".FindUserById", 0, int64(uid)).Store(&path)
	if err != nil {
		return AccountsUser{}, fmt.Errorf("find user %d in AccountsService: %w", uid, err)
	}
	return AccountsUser{obj: sys.Object(accountsBus, path)}, nil
}

// Path is the user's object path, for running the call by hand.
func (u AccountsUser) Path() dbus.ObjectPath { return u.obj.Path() }

// AutomaticLogin reports whether GDM logs this user in at boot, so the
// user's services, desklinkd among them, start without anyone at the desk.
func (u AccountsUser) AutomaticLogin() (bool, error) {
	v, err := u.obj.GetProperty(accountsUser + ".AutomaticLogin")
	if err != nil {
		return false, fmt.Errorf("read automatic login: %w", err)
	}
	var on bool
	if err := v.Store(&on); err != nil {
		return false, fmt.Errorf("decode automatic login: %w", err)
	}
	return on, nil
}

// SetAutomaticLogin turns automatic login on or off for this user. Polkit
// (org.freedesktop.accounts.set-login-option) asks for an administrator's
// password through the desktop session's agent; ErrNotAuthorized when it
// refuses.
func (u AccountsUser) SetAutomaticLogin(ctx context.Context, on bool) error {
	err := u.obj.CallWithContext(ctx, accountsUser+".SetAutomaticLogin", dbus.FlagAllowInteractiveAuthorization, on).Err
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) && dbusErr.Name == accountsDenied {
		return fmt.Errorf("%w: %v", ErrNotAuthorized, err)
	}
	if err != nil {
		return fmt.Errorf("set automatic login: %w", err)
	}
	return nil
}
