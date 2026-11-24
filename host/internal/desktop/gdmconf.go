package desktop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// GDMConf is GDM's configuration file, the one GNOME Settings' Automatic
// Login switch writes. GDM reads it when it starts, so a change applies from
// the next boot.
const GDMConf = "/etc/gdm/custom.conf"

// DefaultTimedLoginDelay is how many seconds GDM's login screen waits before
// it logs the user in by itself.
const DefaultTimedLoginDelay = 10

// GDMLogin is what GDM's configuration says about opening one user's session
// without a password.
type GDMLogin struct {
	// Automatic is automatic login: GDM opens the session once per boot.
	Automatic bool
	// Timed is timed login: the login screen opens the session TimedDelay
	// seconds after it appears, at boot and after a session ended alike.
	Timed      bool
	TimedDelay int
}

// The [daemon] keys GDM reads for automatic and timed login.
const (
	keyAutoEnable  = "AutomaticLoginEnable"
	keyAutoUser    = "AutomaticLogin"
	keyTimedEnable = "TimedLoginEnable"
	keyTimedUser   = "TimedLogin"
	keyTimedDelay  = "TimedLoginDelay"
)

// ReadGDMLogin reads user's automatic and timed login from the GDM
// configuration at path. A missing file means both are off.
func ReadGDMLogin(path, user string) (GDMLogin, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return GDMLogin{}, nil
	}
	if err != nil {
		return GDMLogin{}, fmt.Errorf("read GDM's login settings: %w", err)
	}
	keys := daemonKeys(string(data))
	l := GDMLogin{
		Automatic: gdmBool(keys[keyAutoEnable]) && keys[keyAutoUser] == user,
		Timed:     gdmBool(keys[keyTimedEnable]) && keys[keyTimedUser] == user,
	}
	if l.Timed {
		l.TimedDelay = timedDelay(keys[keyTimedDelay])
	}
	return l, nil
}

// SetGDMLogin turns automatic and timed login on for user, or turns off
// those that name user, in the GDM configuration at path. It changes only
// those keys of the [daemon] section and writes the file in place, so its
// owner, mode and SELinux label stay as they were. Turning on keeps a
// TimedLoginDelay that is already set.
func SetGDMLogin(path, user string, on bool) error {
	if user == "" || strings.ContainsAny(user, "\n\r=[]# \t") {
		return fmt.Errorf("%q is not a user name", user)
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read GDM's login settings: %w", err)
	}
	keys := daemonKeys(string(data))
	var set []keyValue
	switch {
	case on:
		set = []keyValue{
			{keyAutoEnable, "True"}, {keyAutoUser, user},
			{keyTimedEnable, "True"}, {keyTimedUser, user},
			{keyTimedDelay, strconv.Itoa(timedDelay(keys[keyTimedDelay]))},
		}
	default:
		if keys[keyAutoUser] == user {
			set = append(set, keyValue{keyAutoEnable, "False"})
		}
		if keys[keyTimedUser] == user {
			set = append(set, keyValue{keyTimedEnable, "False"})
		}
	}
	out := setDaemonKeys(string(data), set)
	if out == string(data) {
		return nil
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write GDM's login settings: %w", err)
	}
	return nil
}

type keyValue struct{ key, value string }

// gdmBool is GDM's reading of a boolean: true or false in any case, or 1
// or 0; anything else counts as off.
func gdmBool(v string) bool {
	return strings.EqualFold(v, "true") || v == "1"
}

// timedDelay is TimedLoginDelay when it is a positive number of seconds,
// otherwise DefaultTimedLoginDelay.
func timedDelay(v string) int {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return DefaultTimedLoginDelay
}

// daemonKeys returns the keys of data's [daemon] section; the last one wins.
func daemonKeys(data string) map[string]string {
	keys := map[string]string{}
	in := false
	for _, line := range strings.Split(data, "\n") {
		if section, ok := sectionName(line); ok {
			in = section == "daemon"
			continue
		}
		if k, v, ok := keyLine(line); ok && in {
			keys[k] = v
		}
	}
	return keys
}

// setDaemonKeys returns data with each key of set given its value in the
// [daemon] section: in place where the key is, otherwise after the
// section's last key, and in a new [daemon] section at the top when there is
// none. Every other line stays as it was.
func setDaemonKeys(data string, set []keyValue) string {
	if len(set) == 0 {
		return data
	}
	lines := strings.Split(data, "\n")
	if data == "" {
		lines = nil
	}
	header, last := -1, -1
	for i, line := range lines {
		if section, ok := sectionName(line); ok {
			if header >= 0 {
				break
			}
			if section == "daemon" {
				header, last = i, i
			}
			continue
		}
		if header < 0 {
			continue
		}
		k, _, ok := keyLine(line)
		if !ok {
			continue
		}
		last = i
		for j, kv := range set {
			if kv.key == k {
				lines[i] = k + "=" + kv.value
				set = append(set[:j:j], set[j+1:]...)
				break
			}
		}
	}
	added := make([]string, 0, len(set)+1)
	if header < 0 {
		added = append(added, "[daemon]")
	}
	for _, kv := range set {
		added = append(added, kv.key+"="+kv.value)
	}
	at := last + 1
	if header < 0 {
		at = 0
	}
	if len(added) > 0 {
		lines = append(lines[:at], append(added, lines[at:]...)...)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// sectionName is the name of a "[section]" line.
func sectionName(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if len(t) < 2 || t[0] != '[' || t[len(t)-1] != ']' {
		return "", false
	}
	return strings.TrimSpace(t[1 : len(t)-1]), true
}

// keyLine splits a "key=value" line; comments and blank lines are not keys.
func keyLine(line string) (key, value string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || t[0] == '#' {
		return "", "", false
	}
	k, v, found := strings.Cut(t, "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}
