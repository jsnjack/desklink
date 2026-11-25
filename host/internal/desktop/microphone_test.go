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

// micPactl is a fake pactl on PATH that answers like pipewire-pulse and
// keeps the default source in a file, so a test sees what a sequence of
// calls leaves behind. Modules load as 42, 43, ...; `list short modules`
// prints modules; a call whose arguments start with a line of fail exits
// with 1.
type micPactl struct {
	dir   string
	calls string
}

func newMicPactl(t *testing.T, defaultSource, modules, fail string) micPactl {
	t.Helper()
	dir := t.TempDir()
	f := micPactl{dir: dir, calls: filepath.Join(dir, "calls")}
	for name, content := range map[string]string{"default": defaultSource, "next": "42", "modules": modules, "fail": fail + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
d="` + dir + `"
echo "$*" >>"$d/calls"
while IFS= read -r fail; do
	case "$*" in "$fail"*) [ -n "$fail" ] && exit 1 ;; esac
done <"$d/fail"
case "$1" in
get-default-source) cat "$d/default" ;;
set-default-source) printf %s "$2" >"$d/default" ;;
load-module) n=$(cat "$d/next"); echo "$n"; echo $((n + 1)) >"$d/next" ;;
list) cat "$d/modules" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "pactl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f micPactl) defaultSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "default"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (f micPactl) unloaded(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, c := range pactlCalls(t, f.calls) {
		if id, ok := strings.CutPrefix(c, "unload-module "); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestCreateMicrophoneBecomesTheDefaultSource(t *testing.T) {
	f := newMicPactl(t, "alsa_input.desk", "", "")
	r, err := CreateMicrophone(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := MicRoute{FeedModule: "42", SourceModule: "43", PreviousSource: "alsa_input.desk"}
	if r != want {
		t.Errorf("route %+v, want %+v", r, want)
	}
	if got := f.defaultSource(t); got != MicSourceName {
		t.Errorf("default source %q, want the virtual microphone", got)
	}
	calls := pactlCalls(t, f.calls)
	for _, w := range []string{"sink_name=desklink_mic_feed", "master=desklink_mic_feed.monitor", "source_name=desklink_mic", "device.description='DeskLink microphone'"} {
		if !slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, w) }) {
			t.Errorf("pactl calls %q lack %q", calls, w)
		}
	}
}

func TestRestoreMicrophone(t *testing.T) {
	cases := []struct {
		name string
		// chosen is the default source when the session ends.
		chosen      string
		previous    string
		wantDefault string
	}{
		{"gives the default back", MicSourceName, "alsa_input.desk", "alsa_input.desk"},
		{"leaves a source the user chose meanwhile", "bluez_input.headset", "alsa_input.desk", "bluez_input.headset"},
		{"sets no placeholder from a desk without a source", MicSourceName, "@DEFAULT_SOURCE@", MicSourceName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMicPactl(t, c.chosen, "", "")
			r := MicRoute{FeedModule: "42", SourceModule: "43", PreviousSource: c.previous}
			if err := r.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := f.defaultSource(t); got != c.wantDefault {
				t.Errorf("default source %q, want %q", got, c.wantDefault)
			}
			if got := f.unloaded(t); !slices.Equal(got, []string{"43", "42"}) {
				t.Errorf("unloaded %q, want the source before its feed", got)
			}
			if r.Pending() {
				t.Errorf("route %+v still pending after Restore", r)
			}
		})
	}
}

func TestARestoreThatFailedCanBeRetried(t *testing.T) {
	f := newMicPactl(t, MicSourceName, "", "unload-module 42")
	r := MicRoute{FeedModule: "42", SourceModule: "43", PreviousSource: "alsa_input.desk"}
	if err := r.Restore(context.Background()); err == nil {
		t.Fatal("Restore succeeded although the feed could not be unloaded")
	}
	if want := (MicRoute{FeedModule: "42", PreviousSource: "alsa_input.desk"}); r != want {
		t.Fatalf("route %+v after the failure, want only the feed left: %+v", r, want)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.unloaded(t); !slices.Equal(got, []string{"43", "42", "42"}) {
		t.Errorf("unloaded %q, want the source once and the feed again", got)
	}
}

// TestCreateMicrophoneUndoesAFailedStep: a microphone that could not be made
// the default must not stay behind, and one whose removal fails too goes
// back to the caller to retry.
func TestCreateMicrophoneUndoesAFailedStep(t *testing.T) {
	cases := []struct {
		name, fail   string
		wantUnloaded []string
		wantPending  bool
	}{
		{"a refused default removes source and feed", "set-default-source desklink_mic", []string{"43", "42"}, false},
		{"a source that cannot load removes the feed", "load-module module-remap-source", []string{"42"}, false},
		{"a route that cannot be removed goes back to the caller", "set-default-source desklink_mic\nunload-module", []string{"43", "42"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMicPactl(t, "alsa_input.desk", "", c.fail)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r, err := CreateMicrophone(ctx)
			if err == nil {
				t.Fatal("CreateMicrophone succeeded")
			}
			if r.Pending() != c.wantPending {
				t.Errorf("route %+v pending %v, want %v", r, r.Pending(), c.wantPending)
			}
			if got := f.defaultSource(t); got != "alsa_input.desk" {
				t.Errorf("default source %q after the failure, want the desk's again", got)
			}
			if got := f.unloaded(t); !slices.Equal(got, c.wantUnloaded) {
				t.Errorf("unload attempts %q, want %q", got, c.wantUnloaded)
			}
		})
	}
}

func TestRemoveLeftoverMicrophone(t *testing.T) {
	list := "536870913\tmodule-always-sink\t\n" +
		"536870920\tmodule-null-sink\tsink_name=desklink_mic_feed channel_map=mono\n" +
		"536870921\tmodule-remap-source\tsource_name=desklink_mic master=desklink_mic_feed.monitor\n" +
		"536870922\tmodule-null-sink\tsink_name=desklink sink_properties=device.description=DeskLink\n" +
		"536870923\tmodule-remap-source\tsource_name=desklink_micro\n"
	cases := []struct {
		name, modules, current, previous string
		wantUnloaded                     []string
		wantDefault                      string
	}{
		{"unloads the microphone before its feed and gives the default back", list, MicSourceName, "alsa_input.desk",
			[]string{"536870921", "536870920"}, "alsa_input.desk"},
		{"gives the default back after the microphone vanished with its run", "", "alsa_input.webcam", "alsa_input.desk",
			nil, "alsa_input.desk"},
		{"without a saved default leaves the default alone", "", "alsa_input.webcam", "", nil, "alsa_input.webcam"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMicPactl(t, c.current, c.modules, "")
			if err := RemoveLeftoverMicrophone(context.Background(), c.previous); err != nil {
				t.Fatal(err)
			}
			if got := f.unloaded(t); !slices.Equal(got, c.wantUnloaded) {
				t.Errorf("unloaded %q, want %q", got, c.wantUnloaded)
			}
			if got := f.defaultSource(t); got != c.wantDefault {
				t.Errorf("default source %q, want %q", got, c.wantDefault)
			}
		})
	}
}
