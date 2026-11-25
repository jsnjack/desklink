package desktop

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLeftoverModules(t *testing.T) {
	list := "536870913\tmodule-always-sink\t\n" +
		"536870920\tmodule-null-sink\tsink_name=desklink sink_properties=device.description=DeskLink\n" +
		"536870921\tmodule-null-sink\tsink_name=desklinkother\n" +
		"536870922\tmodule-null-sink\tsink_properties=x sink_name=desklink\n" +
		"536870923\tmodule-loopback\tsink_name=desklink\n"
	got := leftoverModules(list)
	want := []string{"536870920", "536870922"}
	if !slices.Equal(got, want) {
		t.Errorf("leftoverModules = %v, want %v", got, want)
	}
}

// fakePactl puts on PATH a pactl that logs each call to the returned file and
// answers like pipewire-pulse: the default output is alsa_output.desk and the
// null sink loads as module 42. Making DeskLink the default hangs when hang
// is set and fails otherwise; unloading fails when failUnload is set.
func fakePactl(t *testing.T, hang, failUnload bool) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$*" >>"` + calls + `"
case "$1 $2" in
"get-default-sink "*) echo alsa_output.desk ;;
"load-module "*) echo 42 ;;
"set-default-sink desklink")
	if [ -n "` + map[bool]string{true: "hang"}[hang] + `" ]; then exec sleep 10; fi
	exit 1 ;;
"unload-module "*) ` + map[bool]string{true: "exit 1", false: ":"}[failUnload] + ` ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "pactl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func pactlCalls(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestRedirectAudioUndoesAFailedRoute: a virtual output that could not be
// made the default must not stay behind, even when the deadline is what ran
// out, and one whose removal fails too must reach the session's cleanup.
func TestRedirectAudioUndoesAFailedRoute(t *testing.T) {
	undo := []string{"set-default-sink alsa_output.desk", "unload-module 42"}
	cases := []struct {
		name             string
		hang, failUnload bool
		wantRoute        AudioRoute
	}{
		{"a refused default is undone", false, false, AudioRoute{}},
		{"a default that outlasts the deadline is undone with time of its own", true, false, AudioRoute{}},
		{"a route that cannot be undone goes back to the caller", false, true, AudioRoute{Module: "42", PreviousSink: "alsa_output.desk"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := fakePactl(t, c.hang, c.failUnload)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			route, err := RedirectAudio(ctx)
			if err == nil {
				t.Fatal("RedirectAudio succeeded")
			}
			if route != c.wantRoute {
				t.Errorf("route %+v, want %+v", route, c.wantRoute)
			}
			got := pactlCalls(t, calls)
			if len(got) < 2 || !slices.Equal(got[len(got)-2:], undo) {
				t.Errorf("pactl calls %q, want them to end with the undo %q", got, undo)
			}
		})
	}
}
