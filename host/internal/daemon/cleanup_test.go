package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakePipeline is a GStreamer pipeline whose teardown runs stop.
type fakePipeline struct{ stop func() }

func (p *fakePipeline) Stop()               { p.stop() }
func (p *fakePipeline) ForceKeyframe() bool { return true }
func (p *fakePipeline) SetQP(int)           {}
func (p *fakePipeline) SetMaxFPS(int)       {}

// The desk gets its lock and its monitors back before the pipelines stop, so
// a teardown that hangs cannot leave the desk dark with every window on the
// client's screen.
func TestTheDeskComesBackBeforeThePipelinesStop(t *testing.T) {
	f, m := ownScreenFixture(t, true)
	f.s.d.ctl.lock = func(context.Context) error { m.log = append(m.log, "lock"); return nil }
	f.s.enc = &fakePipeline{stop: func() { m.log = append(m.log, "stop video") }}
	f.s.audio = &fakePipeline{stop: func() { m.log = append(m.log, "stop sound") }}
	f.s.cleanup()
	want := []string{"lock", "apply DP-5 eDP-1 Meta-0", "stop video", "stop sound", "stop cast", "Mutter lays out the desk"}
	if !slices.Equal(m.log, want) {
		t.Errorf("cleanup did %q, want %q", m.log, want)
	}
}

// TestAResizeEndsTheSessionWhenThePipelineWillNotStop: starting a new
// pipeline while the old one still stops in the background could put two
// encoders' frames on the same stream, so the session ends instead.
func TestAResizeEndsTheSessionWhenThePipelineWillNotStop(t *testing.T) {
	saved := pipelineStopWait
	pipelineStopWait = 20 * time.Millisecond
	t.Cleanup(func() { pipelineStopWait = saved })
	f := newSessionFixture(t)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	f.s.enc = &fakePipeline{stop: func() { <-hang }}
	f.s.width, f.s.height, f.s.fps = 2560, 1600, 120
	f.s.resize(f.s.ctx, 1920, 1200, 60)
	if cause := context.Cause(f.s.ctx); !errors.Is(cause, errPipelineStuck) {
		t.Errorf("session ended with %v, want errPipelineStuck", cause)
	}
	if f.s.width != 2560 || f.s.enc != nil {
		t.Errorf("the resize went on: %dx%d, pipeline %v", f.s.width, f.s.height, f.s.enc)
	}
}

// On 2026-11-24 a session's cleanup never got past the pipeline's teardown:
// the desk stayed dark and the next connection waited forever. Cleanup now
// gives up waiting, still stops the cast, and the teardown it left running
// counts as stalled, so the watchdog restarts desklinkd if it never ends.
func TestCleanupEndsWhenAPipelineTeardownHangs(t *testing.T) {
	saved := pipelineStopWait
	pipelineStopWait = 20 * time.Millisecond
	t.Cleanup(func() { pipelineStopWait = saved })
	f, m := ownScreenFixture(t, true)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	f.s.enc = &fakePipeline{stop: func() { <-hang }}

	done := make(chan struct{})
	go func() {
		f.s.cleanup()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup is still waiting for the pipeline")
	}
	want := []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk"}
	if !slices.Equal(m.log, want) {
		t.Errorf("cleanup did %q, want %q", m.log, want)
	}
	err := f.s.d.stalls.check(time.Now().Add(stallLimit+time.Second), stallLimit)
	if err == nil || !strings.Contains(err.Error(), "video pipeline") {
		t.Errorf("the hanging teardown is not reported to the watchdog: %v", err)
	}
	if err := f.s.d.stalls.check(time.Now(), stallLimit); err != nil {
		t.Errorf("a teardown that just began counts as stalled: %v", err)
	}
}

func TestStallTracker(t *testing.T) {
	var st stallTracker
	now := time.Now()
	endCleanup := st.begin("the session's cleanup")
	if err := st.check(now.Add(time.Second), stallLimit); err != nil {
		t.Errorf("an operation within the limit counts as stalled: %v", err)
	}
	err := st.check(now.Add(stallLimit+time.Minute), stallLimit)
	if err == nil || !strings.Contains(err.Error(), "the session's cleanup") {
		t.Errorf("a stalled operation is not named: %v", err)
	}
	endCleanup()
	if err := st.check(now.Add(time.Hour), stallLimit); err != nil {
		t.Errorf("an ended operation still counts: %v", err)
	}

	endSetup := st.beginWithin("starting the session", 2*stallLimit)
	if err := st.check(now.Add(stallLimit+time.Second), stallLimit); err != nil {
		t.Errorf("an operation within its own longer limit counts as stalled: %v", err)
	}
	if err := st.check(now.Add(2*stallLimit+time.Second), stallLimit); err == nil || !strings.Contains(err.Error(), "starting the session") {
		t.Errorf("an operation past its own limit is not named: %v", err)
	}
	endSetup()

	if !st.finishWithin("quick", time.Second, func() {}) {
		t.Error("a quick stop was not waited for")
	}
	release := make(chan struct{})
	if st.finishWithin("slow", 10*time.Millisecond, func() { <-release }) {
		t.Error("a hanging stop was reported as finished")
	}
	if err := st.check(time.Now().Add(time.Hour), stallLimit); err == nil {
		t.Error("a stop left running is not tracked")
	}
	close(release)
	waitFor(t, func() bool { return st.check(time.Now().Add(time.Hour), stallLimit) == nil })
}
