package main

import (
	"errors"
	"strings"
	"testing"
)

func TestAutologinLine(t *testing.T) {
	if got := autologinLine(true, nil); got != "Automatic login: on" {
		t.Errorf("on: got %q", got)
	}
	if got := autologinLine(false, nil); !strings.Contains(got, "desklinkd autologin on") {
		t.Errorf("off must say how to turn it on: got %q", got)
	}
	if got := autologinLine(false, errors.New("no AccountsService")); got != "" {
		t.Errorf("an unreadable setting must be left out, not reported as off: got %q", got)
	}
}

func TestAutologinStatusSaysHowToTurnItOn(t *testing.T) {
	if got := autologinStatus(false, true); !strings.Contains(got, "desklinkd autologin on") {
		t.Errorf("got %q", got)
	}
	if got := autologinStatus(true, true); !strings.Contains(got, "locks the session") || !strings.Contains(got, "keyring") {
		t.Errorf("on must say the session is locked and the keyring stays locked: got %q", got)
	}
	if got := autologinStatus(true, false); strings.Contains(got, "locks the session") || !strings.Contains(got, "stays open") {
		t.Errorf("without lock_after_autologin it must not promise a lock: got %q", got)
	}
}

func TestSudoAutologinHintIsARunnableCommand(t *testing.T) {
	got := sudoAutologinHint("/org/freedesktop/Accounts/User1000", true)
	want := "sudo busctl call org.freedesktop.Accounts /org/freedesktop/Accounts/User1000 " +
		"org.freedesktop.Accounts.User SetAutomaticLogin b true"
	if !strings.Contains(got, want) {
		t.Errorf("got %q, want it to contain %q", got, want)
	}
	if got := sudoAutologinHint("/org/freedesktop/Accounts/User1000", false); !strings.HasSuffix(got, "SetAutomaticLogin b false") {
		t.Errorf("off: got %q", got)
	}
}
