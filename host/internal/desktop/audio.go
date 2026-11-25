package desktop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// AudioSinkName names the virtual output desklinkd creates for a session.
const AudioSinkName = "desklink"

// DefaultMonitor is the PulseAudio name of the default output's monitor,
// captured when the virtual output cannot be created.
const DefaultMonitor = "@DEFAULT_MONITOR@"

// audioUndoTimeout bounds undoing a virtual output that could not be made
// the default.
const audioUndoTimeout = 5 * time.Second

// AudioRoute is a session's virtual output: the null sink's module and the
// output that was the default before, to restore afterwards.
type AudioRoute struct {
	Module       string
	PreviousSink string
}

// Monitor is the source that captures what plays on the virtual output.
func (AudioRoute) Monitor() string { return AudioSinkName + ".monitor" }

// RedirectAudio creates a silent virtual output named "DeskLink" and makes it
// the default, so the desktop's sound plays on the client instead of the
// desk's speakers. WirePlumber moves streams that follow the default along
// with it. It uses pactl, which PipeWire serves through pipewire-pulse.
//
// If the output cannot be made the default, it is removed again and the
// previous default restored, since a set-default-sink that timed out may have
// worked. That undo gets time of its own, because ctx may be what ran out.
// If the undo fails too, the route comes back with the error, so the
// caller's cleanup can retry it.
func RedirectAudio(ctx context.Context) (AudioRoute, error) {
	prev, err := pactl(ctx, "get-default-sink")
	if err != nil {
		return AudioRoute{}, err
	}
	module, err := pactl(ctx, "load-module", "module-null-sink", "sink_name="+AudioSinkName,
		"sink_properties=device.description=DeskLink")
	if err != nil {
		return AudioRoute{}, err
	}
	r := AudioRoute{Module: module, PreviousSink: prev}
	if _, err := pactl(ctx, "set-default-sink", AudioSinkName); err != nil {
		undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), audioUndoTimeout)
		defer cancel()
		if uerr := r.Restore(undo); uerr != nil {
			return r, errors.Join(err, uerr)
		}
		return AudioRoute{}, err
	}
	return r, nil
}

// Restore makes the previous output the default again and removes the
// virtual one; streams on it move to the default.
func (r AudioRoute) Restore(ctx context.Context) error {
	var errs []error
	if r.PreviousSink != "" && r.PreviousSink != AudioSinkName {
		if _, err := pactl(ctx, "set-default-sink", r.PreviousSink); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(append(errs, r.unload(ctx))...)
}

func (r AudioRoute) unload(ctx context.Context) error {
	if r.Module == "" {
		return nil
	}
	_, err := pactl(ctx, "unload-module", r.Module)
	return err
}

// RemoveLeftoverAudio undoes the virtual output of a run that crashed:
// it unloads every null sink named desklink and, when previous is known,
// makes it the default again.
func RemoveLeftoverAudio(ctx context.Context, previous string) error {
	out, err := pactl(ctx, "list", "short", "modules")
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range leftoverModules(out) {
		errs = append(errs, AudioRoute{Module: id}.unload(ctx))
	}
	if previous != "" && previous != AudioSinkName {
		if _, err := pactl(ctx, "set-default-sink", previous); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// leftoverModules returns the IDs of desklink null sinks in the output of
// `pactl list short modules`: ID, module name and arguments, tab-separated.
func leftoverModules(list string) []string {
	var ids []string
	sc := bufio.NewScanner(strings.NewReader(list))
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), "\t", 3)
		if len(f) == 3 && f[1] == "module-null-sink" && strings.Contains(" "+f[2]+" ", " sink_name="+AudioSinkName+" ") {
			ids = append(ids, f[0])
		}
	}
	return ids
}

func pactl(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "pactl", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("pactl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("pactl %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
