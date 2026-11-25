package desktop

import (
	"context"
	"errors"
	"strings"
)

// MicSourceName names the virtual microphone desklinkd creates for a client
// that streams its microphone; desktop apps see it as "DeskLink microphone".
const MicSourceName = "desklink_mic"

// micFeedName names the silent virtual output the session plays the client's
// microphone into; the virtual microphone records its monitor.
const micFeedName = "desklink_mic_feed"

// MicRoute is a session's virtual microphone: the null sink the client's
// sound plays into, the source that remaps its monitor into a microphone,
// and the source that was the default before, to restore afterwards. Restore
// clears each part it has undone, so a failed Restore can be retried.
type MicRoute struct {
	FeedModule     string
	SourceModule   string
	PreviousSource string
}

// Feed is the output the session plays the client's microphone into.
func (MicRoute) Feed() string { return micFeedName }

// Pending reports whether part of the route still exists in PipeWire.
func (r MicRoute) Pending() bool { return r.FeedModule != "" || r.SourceModule != "" }

// CreateMicrophone creates a virtual microphone named "DeskLink microphone"
// and makes it the default source, so desktop apps that follow the default
// (calls, dictation) record the client. It uses pactl, which PipeWire serves
// through pipewire-pulse.
//
// The microphone is a null sink, which the session plays into, and
// module-remap-source over that sink's monitor. A null sink created with
// media.class=Audio/Source/Virtual would be one device instead of two, but
// WirePlumber links playback streams only to nodes that take input by name,
// so a pulsesink naming that source falls back to the default output and the
// microphone stays silent. The remapped source is a proper Audio/Source, not
// a monitor, so apps that hide monitors list it too.
//
// If any step fails, what was made is removed again with time of its own,
// because ctx may be what ran out. If that fails too, the route comes back
// with the error, so the caller can retry Restore.
func CreateMicrophone(ctx context.Context) (MicRoute, error) {
	prev, err := pactl(ctx, "get-default-source")
	if err != nil {
		return MicRoute{}, err
	}
	r := MicRoute{PreviousSource: prev}
	err = r.create(ctx)
	if err == nil {
		return r, nil
	}
	undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), audioUndoTimeout)
	defer cancel()
	if uerr := r.Restore(undo); uerr != nil {
		return r, errors.Join(err, uerr)
	}
	return MicRoute{}, err
}

func (r *MicRoute) create(ctx context.Context) error {
	var err error
	if r.FeedModule, err = pactl(ctx, "load-module", "module-null-sink", "sink_name="+micFeedName, "channel_map=mono",
		`sink_properties="device.description='DeskLink microphone feed'"`); err != nil {
		return err
	}
	if r.SourceModule, err = pactl(ctx, "load-module", "module-remap-source", "source_name="+MicSourceName,
		"master="+micFeedName+".monitor", "channel_map=mono",
		`source_properties="device.description='DeskLink microphone' device.icon_name=audio-input-microphone"`); err != nil {
		return err
	}
	_, err = pactl(ctx, "set-default-source", MicSourceName)
	return err
}

// Restore gives the default back to the source that had it, if the virtual
// microphone is still the default (a source the user chose meanwhile stays),
// then removes the microphone and its feed; streams recording it move to the
// default. The default goes back first, so that WirePlumber does not pick
// some other source while the microphone disappears.
func (r *MicRoute) Restore(ctx context.Context) error {
	if !r.Pending() {
		return nil
	}
	var errs []error
	if restorable(r.PreviousSource) {
		if cur, err := pactl(ctx, "get-default-source"); err != nil {
			errs = append(errs, err)
		} else if cur == MicSourceName {
			if _, err := pactl(ctx, "set-default-source", r.PreviousSource); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// The remapped source records the feed's monitor, so it goes first.
	for _, m := range []*string{&r.SourceModule, &r.FeedModule} {
		if *m == "" {
			continue
		}
		if _, err := pactl(ctx, "unload-module", *m); err != nil {
			errs = append(errs, err)
			continue
		}
		*m = ""
	}
	return errors.Join(errs...)
}

// restorable reports whether source can be made the default again: not the
// virtual microphone itself, and not a placeholder such as
// @DEFAULT_SOURCE@, which pipewire-pulse reports when there is no source.
func restorable(source string) bool {
	return source != "" && source != MicSourceName && !strings.HasPrefix(source, "@")
}

// RemoveLeftoverMicrophone undoes the virtual microphone of a run that
// crashed: it unloads every leftover microphone and feed and makes previous,
// the source that was the default before (empty when none was saved), the
// default again.
func RemoveLeftoverMicrophone(ctx context.Context, previous string) error {
	out, err := pactl(ctx, "list", "short", "modules")
	if err != nil {
		return err
	}
	sources := modulesWith(out, "module-remap-source", "source_name="+MicSourceName)
	feeds := modulesWith(out, "module-null-sink", "sink_name="+micFeedName)
	if len(sources) == 0 && len(feeds) == 0 {
		// WirePlumber still has the vanished microphone as the chosen
		// default and uses a fallback meanwhile.
		if restorable(previous) {
			_, err := pactl(ctx, "set-default-source", previous)
			return err
		}
		return nil
	}
	var errs []error
	// The first Restore gives the default back; each unloads a source
	// before its feed.
	r := MicRoute{PreviousSource: previous}
	for i := range max(len(sources), len(feeds)) {
		if i < len(sources) {
			r.SourceModule = sources[i]
		}
		if i < len(feeds) {
			r.FeedModule = feeds[i]
		}
		errs = append(errs, r.Restore(ctx))
		r = MicRoute{}
	}
	return errors.Join(errs...)
}
