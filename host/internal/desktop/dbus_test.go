package desktop

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestMayHaveActed(t *testing.T) {
	denied := dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"a call that succeeded", nil, true},
		{"an error reply refused it", denied, false},
		{"a wrapped error reply refused it", fmt.Errorf("unlock all sessions: %w", denied), false},
		{"an error reply by pointer refused it", dbus.NewError("org.freedesktop.login1.NoSuchSession", nil), false},
		{"a reply lost to the timeout may have worked", fmt.Errorf("unlock: %w", context.DeadlineExceeded), true},
		{"a reply lost to cancellation may have worked", context.Canceled, true},
		{"a lost connection may have worked", errors.New("dbus: connection closed by user"), true},
		{"the bus gave up waiting for the reply", dbus.Error{Name: "org.freedesktop.DBus.Error.NoReply"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mayHaveActed(c.err); got != c.want {
				t.Errorf("mayHaveActed(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
