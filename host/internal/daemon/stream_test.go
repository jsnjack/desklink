package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"desklink/internal/config"
	"desklink/internal/desktop"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

// autoPreset is the tablet's Auto preset, which useFakeMutter's session runs.
var autoPreset = dlnet.StreamChange{FPS: 120, QP: 18, Auto: true}

// withScreen is the Auto preset asking for another screen.
func withScreen(screen, monitor string) dlnet.StreamChange {
	c := autoPreset
	c.Screen, c.Monitor = screen, monitor
	return c
}

func TestStreamPreset(t *testing.T) {
	t.Run("the preset the session runs changes nothing", func(t *testing.T) {
		f, m := mirrorFixture(t)
		if err := f.s.switchStream(f.s.ctx, autoPreset); err != nil {
			t.Fatal(err)
		}
		if len(m.pipelines) != 1 || len(m.log) != 0 {
			t.Errorf("did %q with %d pipelines, want nothing", m.log, len(m.pipelines))
		}
	})
	t.Run("a preset that changes only the quantizer keeps the pipeline", func(t *testing.T) {
		f, m := mirrorFixture(t)
		if err := f.s.switchStream(f.s.ctx, dlnet.StreamChange{FPS: 120, QP: 24, Auto: true}); err != nil {
			t.Fatal(err)
		}
		if len(m.pipelines) != 1 {
			t.Errorf("%d pipelines, want the one running", len(m.pipelines))
		}
		if f.s.requestedQP != 24 || f.s.quality.QP() != 24 || !f.s.quality.Auto() {
			t.Errorf("requested %d, quantizer %d, auto %v; want 24, 24, auto", f.s.requestedQP, f.s.quality.QP(), f.s.quality.Auto())
		}
	})
	t.Run("leaving auto on a lower rung restarts the pipeline at the top", func(t *testing.T) {
		f, m := mirrorFixture(t)
		f.s.quality = video.NewQualityController(video.QualityConfig{Requested: 18, MinQP: 16, MaxQP: 26, Levels: ladder(2560, 1600, 120), Start: 2, Auto: true})
		l := f.s.quality.Levels()[2]
		f.s.width, f.s.height, f.s.fps = l.Width, l.Height, l.FPS
		// Sharp: the top, its quantizer whatever the connection does.
		if err := f.s.switchStream(f.s.ctx, dlnet.StreamChange{FPS: 120, QP: 12, Fixed: true}); err != nil {
			t.Fatal(err)
		}
		last := m.pipelines[len(m.pipelines)-1]
		if len(m.pipelines) != 2 || last.Width != 2560 || last.Height != 1600 || last.FPS != 120 || last.QP != 12 {
			t.Errorf("%d pipelines, the last %+v; want a second at 2560x1600, 120 fps, quantizer 12", len(m.pipelines), last)
		}
		if !f.s.quality.Fixed() || f.s.quality.Auto() || len(f.s.quality.Levels()) != 1 {
			t.Errorf("fixed %v, auto %v, %d levels; want a fixed single level", f.s.quality.Fixed(), f.s.quality.Auto(), len(f.s.quality.Levels()))
		}
		if f.s.ctx.Err() != nil {
			t.Error("the session ended")
		}
	})
	t.Run("a lower frame rate restarts the pipeline at that rate", func(t *testing.T) {
		f, m := mirrorFixture(t)
		// Balanced: 60 fps, quantizer 20, no auto.
		if err := f.s.switchStream(f.s.ctx, dlnet.StreamChange{FPS: 60, QP: 20}); err != nil {
			t.Fatal(err)
		}
		last := m.pipelines[len(m.pipelines)-1]
		if len(m.pipelines) != 2 || last.FPS != 60 || last.Width != 2560 {
			t.Errorf("%d pipelines, the last %+v; want a second at 2560 wide and 60 fps", len(m.pipelines), last)
		}
		if top := f.s.quality.Levels()[0]; top.FPS != 60 || f.s.quality.Auto() {
			t.Errorf("ladder top %+v, auto %v; want 60 fps without auto", top, f.s.quality.Auto())
		}
	})
	t.Run("auto starts at the level the client remembers for the network", func(t *testing.T) {
		f, m := mirrorFixture(t)
		f.s.quality = video.NewQualityController(video.QualityConfig{Requested: 12, MinQP: 16, MaxQP: 26, Levels: ladder(2560, 1600, 120)[:1], Fixed: true})
		remembered := ladder(2560, 1600, 120)[1]
		c := dlnet.StreamChange{FPS: 120, QP: 12, Auto: true, Start: &dlnet.Level{Width: remembered.Width, Height: remembered.Height, FPS: remembered.FPS}}
		if err := f.s.switchStream(f.s.ctx, c); err != nil {
			t.Fatal(err)
		}
		if got := f.s.quality.Level(); got != 1 || !f.s.quality.Auto() {
			t.Errorf("level %d, auto %v; want the remembered level 1 with auto", got, f.s.quality.Auto())
		}
		if last := m.pipelines[len(m.pipelines)-1]; last.FPS != remembered.FPS || last.Width != remembered.Width {
			t.Errorf("the pipeline runs %+v, want the remembered level %+v", last, remembered)
		}
	})
}

func TestStreamCodec(t *testing.T) {
	f, m := mirrorFixture(t)
	c := autoPreset
	c.Codec = string(video.CodecHEVC)
	if err := f.s.switchStream(f.s.ctx, c); err != nil {
		t.Fatal(err)
	}
	if last := m.pipelines[len(m.pipelines)-1]; len(m.pipelines) != 2 || last.Codec != video.CodecHEVC || last.NodeID != f.s.capture.NodeID {
		t.Errorf("%d pipelines, the last %+v; want a second on the same cast with HEVC", len(m.pipelines), last)
	}
	if len(m.log) != 0 {
		t.Errorf("did %q to the casts, want nothing", m.log)
	}
	if f.s.codec != video.CodecHEVC {
		t.Errorf("the session reports codec %q, want hevc", f.s.codec)
	}
}

func TestStreamScreen(t *testing.T) {
	virtualOnly := desktop.DisplayLayout{LayoutMode: 1, Logical: []desktop.LogicalConfig{
		{Scale: 2, Primary: true, Monitors: []desktop.MonitorAssignment{{Connector: virtualConnector, ModeID: "v"}}},
	}}
	// mirrored checks that the session mirrors connector with the desk laid
	// out as before the session.
	mirrored := func(t *testing.T, f sessionFixture, m *fakeMutter, connector string) {
		t.Helper()
		if f.s.capture.Virtual || f.s.captured != connector || f.s.screenMode() != dlnet.ScreenMirror {
			t.Errorf("shows %q (virtual %v, mode %q), want %s mirrored", f.s.captured, f.s.capture.Virtual, f.s.screenMode(), connector)
		}
		if got := m.layout(t); !got.Equal(deskBefore()) {
			t.Errorf("layout %+v, want the desk's from before", got.Logical)
		}
		last := m.pipelines[len(m.pipelines)-1]
		if last.CaptureWidth != 0 || last.NodeID != f.s.capture.NodeID {
			t.Errorf("the pipeline runs %+v, want the mirror's cast without the own screen's size", last)
		}
		if p, d := f.s.primaryState(), f.s.deskOffState(); p != nil || d != nil {
			t.Errorf("session info offers primary %v and desk %v while mirroring, want neither", p, d)
		}
		if f.s.ctx.Err() != nil {
			t.Error("the session ended")
		}
	}
	cases := []struct {
		name   string
		locked bool
		before func(*testing.T, *session)
		log    []string
	}{
		{
			name: "the own screen beside an unlocked desk gives way to a mirrored monitor",
			log:  []string{"stop cast", "Mutter lays out the desk", "record DP-5"},
		},
		{
			name: "desk monitors the client turned off come back on before the own screen goes",
			before: func(t *testing.T, s *session) {
				if _, err := s.switchDesk(s.ctx, true); err != nil {
					t.Fatal(err)
				}
			},
			log: []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk", "record DP-5"},
		},
		{
			// As a client that asks to mirror from the start sees it.
			name:   "a desk found locked comes back on too, showing the unlocked desktop",
			locked: true,
			log:    []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk", "record DP-5"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, m := ownScreenFixture(t, c.locked)
			if c.before != nil {
				c.before(t, f.s)
				m.log = nil
			}
			if err := f.s.switchStream(f.s.ctx, withScreen(dlnet.ScreenMirror, "DP-5")); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(m.log, c.log) {
				t.Errorf("did %q, want %q", m.log, c.log)
			}
			mirrored(t, f, m, "DP-5")
		})
	}

	t.Run("a mirrored monitor gives way to the own screen, primary beside the desk", func(t *testing.T) {
		f, m := mirrorFixture(t)
		f.s.scale = 2 // the hello's default
		if err := f.s.switchStream(f.s.ctx, withScreen(dlnet.ScreenOwn, "")); err != nil {
			t.Fatal(err)
		}
		if want := []string{"stop cast", "record virtual", "apply DP-5 eDP-1 Meta-0"}; !slices.Equal(m.log, want) {
			t.Errorf("did %q, want %q", m.log, want)
		}
		if got := m.layout(t); !got.Equal(beside(true)) {
			t.Errorf("layout %+v, want the client's screen primary beside the desk", got.Logical)
		}
		last := m.pipelines[len(m.pipelines)-1]
		if f.s.captured != virtualConnector || last.CaptureWidth != 2560 || last.CaptureHeight != 1600 || f.s.screenMode() != dlnet.ScreenOwn {
			t.Errorf("shows %q in mode %q, the pipeline %+v; want Meta-0 at 2560x1600", f.s.captured, f.s.screenMode(), last)
		}
	})
	t.Run("the own screen takes the choices the client remembers", func(t *testing.T) {
		cases := []struct {
			name  string
			hello dlnet.Hello
			want  desktop.DisplayLayout
		}{
			{"primary on the desk", dlnet.Hello{Primary: new(false)}, beside(false)},
			{"the desk's monitors off", dlnet.Hello{DeskOff: true}, virtualOnly},
		}
		for _, c := range cases {
			f, m := mirrorFixture(t)
			f.s.scale = 2
			f.s.rememberChoices(c.hello)
			if err := f.s.switchStream(f.s.ctx, withScreen(dlnet.ScreenOwn, "")); err != nil {
				t.Fatal(err)
			}
			if got := m.layout(t); !got.Equal(c.want) {
				t.Errorf("%s: layout %+v, want %+v", c.name, got.Logical, c.want.Logical)
			}
		}
	})
	t.Run("there and back: the own screen after mirroring is placed as before", func(t *testing.T) {
		f, m := ownScreenFixture(t, false)
		if err := f.s.switchStream(f.s.ctx, withScreen(dlnet.ScreenMirror, "eDP-1")); err != nil {
			t.Fatal(err)
		}
		if err := f.s.switchStream(f.s.ctx, withScreen(dlnet.ScreenOwn, "")); err != nil {
			t.Fatal(err)
		}
		if got := m.layout(t); f.s.captured != virtualConnector || !layoutBesideWithMode(got, "v1") {
			t.Errorf("shows %q with layout %+v, want Meta-0 primary beside the desk", f.s.captured, got.Logical)
		}
	})
	t.Run("without a virtual screen the session mirrors again and says why", func(t *testing.T) {
		f, m := mirrorFixture(t)
		m.failCast = 1
		err := f.s.switchStream(f.s.ctx, withScreen(dlnet.ScreenOwn, ""))
		if err == nil {
			t.Fatal("the switch reported no error")
		}
		if f.s.capture.Virtual || f.s.captured != "eDP-1" || f.s.ctx.Err() != nil || len(m.pipelines) != 2 {
			t.Errorf("shows %q (virtual %v) with %d pipelines, session ended %v; want eDP-1 mirrored again on a new pipeline",
				f.s.captured, f.s.capture.Virtual, len(m.pipelines), f.s.ctx.Err() != nil)
		}
	})
	t.Run("auto means the own screen only when the session found the desk locked", func(t *testing.T) {
		f, _ := mirrorFixture(t)
		f.s.wasLocked = true
		if err := f.s.switchStream(f.s.ctx, withScreen(dlnet.ScreenAuto, "")); err != nil || !f.s.capture.Virtual {
			t.Errorf("a desk found locked: virtual %v (%v), want the own screen", f.s.capture.Virtual, err)
		}
		g, _ := ownScreenFixture(t, false)
		if err := g.s.switchStream(g.s.ctx, withScreen(dlnet.ScreenAuto, "")); err != nil || g.s.capture.Virtual {
			t.Errorf("an unlocked desk: virtual %v (%v), want a mirrored monitor", g.s.capture.Virtual, err)
		}
	})
	t.Run("an unknown screen changes nothing", func(t *testing.T) {
		f, m := mirrorFixture(t)
		if err := f.s.switchStream(f.s.ctx, withScreen("both", "")); err == nil || len(m.log) != 0 || len(m.pipelines) != 1 {
			t.Errorf("err %v, did %q with %d pipelines; want an error and nothing done", err, m.log, len(m.pipelines))
		}
	})
}

// layoutBesideWithMode reports whether l is beside(true) with the virtual
// screen in mode: a second virtual monitor gets a new mode from the fake.
func layoutBesideWithMode(l desktop.DisplayLayout, mode string) bool {
	want := beside(true)
	want.Logical[2].Monitors[0].ModeID = mode
	return l.Equal(want)
}

func TestRememberedChoices(t *testing.T) {
	cases := []struct {
		name        string
		locked      bool
		hello       dlnet.Hello
		wantPrimary *bool
		wantDeskOff bool
	}{
		{"none: the own screen takes the primary", false, dlnet.Hello{}, nil, false},
		{"primary left on the desk", false, dlnet.Hello{Primary: new(false)}, new(false), false},
		{"desk monitors off, which the client may turn on again", false, dlnet.Hello{DeskOff: true}, nil, true},
		{"a desk found locked stays dark whatever the client remembers", true, dlnet.Hello{DeskOff: true}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSessionFixture(t)
			f.s.wasLocked = c.locked
			f.s.rememberChoices(c.hello)
			if (f.s.primaryChoice == nil) != (c.wantPrimary == nil) || f.s.primaryChoice != nil && *f.s.primaryChoice != *c.wantPrimary {
				t.Errorf("primary choice %v, want %v", f.s.primaryChoice, c.wantPrimary)
			}
			if f.s.deskOffByClient != c.wantDeskOff {
				t.Errorf("desk off by client %v, want %v", f.s.deskOffByClient, c.wantDeskOff)
			}
		})
	}
}

// fakeSound routes and captures sound without touching the machine's real
// outputs; log records what it did.
type fakeSound struct {
	log         []string
	failRestore int
}

func (f *fakeSound) Redirect(context.Context) (desktop.AudioRoute, error) {
	f.log = append(f.log, "redirect")
	return desktop.AudioRoute{Module: "42", PreviousSink: "speakers"}, nil
}

func (f *fakeSound) Restore(_ context.Context, r desktop.AudioRoute) error {
	if f.failRestore > 0 {
		f.failRestore--
		f.log = append(f.log, "restore failed")
		return errors.New("pactl timed out")
	}
	f.log = append(f.log, "restore "+r.PreviousSink)
	return nil
}

func (f *fakeSound) Capture(device string, _ func(video.Frame)) (soundCapture, error) {
	f.log = append(f.log, "capture "+device)
	return &fakeEncoder{errs: make(chan error, 1), done: make(chan struct{})}, nil
}

func TestStreamSound(t *testing.T) {
	sound := func(t *testing.T) (sessionFixture, *fakeSound) {
		t.Helper()
		f, _ := mirrorFixture(t)
		s := &fakeSound{}
		f.s.d.sound = s
		state, err := config.OpenState(filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		f.s.d.state = state
		return f, s
	}
	t.Run("sound on moves the desk's sound to the client", func(t *testing.T) {
		f, s := sound(t)
		f.s.switchAudio(f.s.ctx, true)
		if want := []string{"redirect", "capture desklink.monitor"}; !slices.Equal(s.log, want) || f.s.audio == nil {
			t.Errorf("did %q, sound running %v; want %q and sound", s.log, f.s.audio != nil, want)
		}
	})
	t.Run("sound off gives the desk its speakers back", func(t *testing.T) {
		f, s := sound(t)
		f.s.switchAudio(f.s.ctx, true)
		s.log = nil
		f.s.switchAudio(f.s.ctx, false)
		if want := []string{"restore speakers"}; !slices.Equal(s.log, want) || f.s.audio != nil || f.s.audioRoute != nil {
			t.Errorf("did %q, sound running %v, route %v; want %q and neither", s.log, f.s.audio != nil, f.s.audioRoute, want)
		}
	})
	t.Run("asking for what runs changes nothing", func(t *testing.T) {
		f, s := sound(t)
		f.s.switchAudio(f.s.ctx, false)
		f.s.switchAudio(f.s.ctx, true)
		s.log = nil
		f.s.switchAudio(f.s.ctx, true)
		if len(s.log) != 0 {
			t.Errorf("did %q, want nothing", s.log)
		}
	})
	t.Run("a route a stop could not undo is undone before sound starts again", func(t *testing.T) {
		f, s := sound(t)
		f.s.switchAudio(f.s.ctx, true)
		s.failRestore = 1
		f.s.switchAudio(f.s.ctx, false)
		s.log = nil
		f.s.switchAudio(f.s.ctx, true)
		if want := []string{"restore speakers", "redirect", "capture desklink.monitor"}; !slices.Equal(s.log, want) {
			t.Errorf("did %q, want %q", s.log, want)
		}
	})
	t.Run("a stream message switches the sound apart from the picture", func(t *testing.T) {
		f, s := sound(t)
		c := autoPreset
		c.Audio = new(true)
		if err := f.s.applyStream(f.s.ctx, c); err != nil {
			t.Fatal(err)
		}
		if f.s.audio == nil || !slices.Contains(s.log, "redirect") {
			t.Errorf("did %q, want the sound on", s.log)
		}
		c.Audio = nil
		s.log = nil
		if err := f.s.applyStream(f.s.ctx, c); err != nil {
			t.Fatal(err)
		}
		if f.s.audio == nil || len(s.log) != 0 {
			t.Errorf("a message without audio did %q and left sound running %v; want nothing changed", s.log, f.s.audio != nil)
		}
	})
}
