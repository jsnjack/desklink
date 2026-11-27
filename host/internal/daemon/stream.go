package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"desklink/internal/desktop"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

// A stream message changes the stream part of the client's hello during the
// session: the quality preset, the codec and whether the client sees its own
// screen or a mirrored monitor. Clients send it when a setting changes in
// their session sheet, so the change applies without connecting again
// (docs/protocol.md).

// setStream answers a stream message. A switch that fails once the old
// pipeline has stopped ends the session, whose cleanup gives the desk back.
// A session that survives describes itself again, also after a failed
// switch, so the client shows what the session really does. The sound
// switches apart from the picture, also when the picture's switch failed.
func (s *session) setStream(ctx context.Context, c dlnet.StreamChange) {
	err := s.applyStream(ctx, c)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.reportError(ctx, err)
	}
	s.sendSessionInfo(ctx)
}

// applyStream applies c: the picture (switchStream), then the sound
// (switchAudio), and returns the picture's error.
func (s *session) applyStream(ctx context.Context, c dlnet.StreamChange) error {
	err := s.switchStream(ctx, c)
	if ctx.Err() == nil && c.Audio != nil {
		s.switchAudio(ctx, *c.Audio)
	}
	return err
}

// switchAudio starts the desktop's sound for the client, which moves it to
// the DeskLink output so the desk's speakers fall silent, or stops it and
// makes the desk's own output the default again, as the end of a session
// does (docs/desktop.md, Sound). A route an earlier stop could not undo is
// undone first, so starting again never loses the output to restore.
// SessionInfo.Audio reports the result: sound that cannot start leaves it
// off.
func (s *session) switchAudio(ctx context.Context, on bool) {
	if on == (s.audio != nil) {
		return
	}
	if !on || s.audioRoute != nil {
		stepCtx, cancel := teardownContext(ctx)
		s.stopAudio(stepCtx)
		cancel()
	}
	if on {
		s.startAudio(ctx)
	}
}

// switchStream applies c. The quality controller is replaced by one for the
// new preset with the same ladder top (the client's screen), which keeps what
// the old one learned about the connection (QualityController.Follow). A
// switch between the own screen and a mirrored monitor replaces the cast
// (switchScreen); otherwise a new codec, or a level of another size or rate,
// restarts the pipeline on the same cast, and a preset that changes only the
// quantizer sets it on the running encoder, which starts a new sequence with
// a keyframe. A request that changes nothing does nothing.
func (s *session) switchStream(ctx context.Context, c dlnet.StreamChange) error {
	if s.capture == nil {
		return errors.New("the session shows no screen to change")
	}
	own := s.capture.Virtual
	switch c.Screen {
	case "":
	case dlnet.ScreenOwn:
		own = true
	case dlnet.ScreenMirror:
		own = false
	case dlnet.ScreenAuto:
		// As at the start: the own screen when nobody was at the desk.
		own = s.wasLocked
	default:
		return fmt.Errorf("unknown screen %q", c.Screen)
	}
	codec := s.codec
	if c.Codec != "" {
		codec = chooseCodec(video.Codec(c.Codec))
	}
	s.qMu.Lock()
	requested := s.requestedQP
	top := s.quality.Levels()[0]
	oldAuto, oldFixed := s.quality.Auto(), s.quality.Fixed()
	s.qMu.Unlock()
	fps := c.FPS
	if fps <= 0 || fps > maxFPS {
		fps = top.FPS
	}
	qp := c.QP
	if qp <= 0 {
		qp = requested
	}
	auto := c.Auto && !c.Fixed
	screen := own != s.capture.Virtual
	samePreset := qp == requested && fps == top.FPS && auto == oldAuto && c.Fixed == oldFixed
	if !screen && codec == s.codec && samePreset {
		return nil
	}
	s.qMu.Lock()
	next := s.followQuality(s.quality, top, qp, fps, auto, c.Fixed, c.Start)
	s.qMu.Unlock()
	l := next.Levels()[next.Level()]
	if !screen && codec == s.codec && l.Width == s.width && l.Height == s.height && l.FPS == s.fps {
		s.qMu.Lock()
		s.quality, s.requestedQP = next, qp
		applied := next.QP()
		s.qMu.Unlock()
		s.forgetLevelChange()
		s.setQP(applied)
		return nil
	}
	if screen {
		// Text being typed through the old cast finishes before it stops;
		// text after it waits for the new one.
		s.holdTyping()
		defer s.releaseTyping()
	}
	if !s.stopForRestart() {
		return errPipelineStuck
	}
	s.qMu.Lock()
	s.quality, s.requestedQP = next, qp
	s.qMu.Unlock()
	s.forgetLevelChange()
	s.codec = codec
	s.width, s.height, s.fps = l.Width, l.Height, l.FPS
	if screen {
		return s.switchScreen(ctx, own, c.Monitor)
	}
	s.updatePointerMap(ctx)
	if err := s.startPipeline(ctx); err != nil {
		err = fmt.Errorf("restart the pipeline: %w", err)
		s.cancel(err)
		return err
	}
	// The new pipeline asks for the same capture size, so Mutter keeps the
	// virtual monitor. Should it have made a new one anyway, arrange that.
	if s.capture.Virtual && !s.virtualScreenShown(ctx) {
		slog.Info("the client's screen left the layout on a stream change; arranging it again")
		s.arrangeVirtualScreen(ctx)
	}
	return nil
}

// followQuality is the quality controller for a new preset: the ladder from
// top's size at fps (only its top without auto), starting at the level the
// client remembers for the network, or on the old controller's rung when
// auto stays on. The caller holds qMu, since telemetry moves old.
func (s *session) followQuality(old *video.QualityController, top video.Level, qp, fps int, auto, fixed bool, remembered *dlnet.Level) *video.QualityController {
	cfg := s.d.opts.Config
	levels := ladder(top.Width, top.Height, fps)
	if !auto {
		levels = levels[:1]
	}
	start := 0
	switch {
	case auto && remembered != nil:
		want := video.Level{Width: remembered.Width, Height: remembered.Height, FPS: remembered.FPS}
		start = video.StartLevel(levels, turnedLike(want, top.Width, top.Height))
	case auto && old.Auto():
		start = min(old.Level(), len(levels)-1)
	}
	return old.Follow(video.QualityConfig{
		Requested: qp, MinQP: cfg.MinQP, MaxQP: cfg.MaxQP,
		Levels: levels, Start: start, Auto: auto, Fixed: fixed,
	}, time.Now())
}

// forgetLevelChange drops a level change the old quality controller chose
// that the control loop has not taken yet: the new controller starts on its
// own level.
func (s *session) forgetLevelChange() {
	select {
	case <-s.levelChanges:
	default:
	}
}

// switchScreen replaces the cast of the stopped pipeline with the other kind
// and starts the pipeline again. The own screen is placed as at the start of
// a session, with the client's primary and desk choices. Leaving it gives
// the desk back as the end of a session does (leaveOwnScreen); a desk the
// session found locked then shows the unlocked desktop while the client
// mirrors it, as when a client asks to mirror from the start. If the own
// screen cannot start, the session mirrors again and reports why; any other
// failure ends the session.
func (s *session) switchScreen(ctx context.Context, own bool, monitor string) error {
	fail := func(err error) error {
		s.cancel(err)
		return err
	}
	if !own {
		s.leaveOwnScreen(ctx)
		if err := ctx.Err(); err != nil {
			return err // the session ended meanwhile; the desk is back
		}
		if err := s.startMonitorCapture(ctx, monitor); err != nil {
			return fail(fmt.Errorf("mirror a monitor: %w", err))
		}
		if err := s.startPipeline(ctx); err != nil {
			return fail(fmt.Errorf("start the pipeline for %s: %w", s.captured, err))
		}
		return nil
	}
	mirrored := s.captured
	old := s.capture
	s.useCapture(nil)
	s.stopReplacedCast(ctx, old)
	if err := ctx.Err(); err != nil {
		return err
	}
	if capErr := s.startVirtualCapture(ctx); capErr != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		slog.Warn("no virtual screen; mirroring a monitor again", "err", capErr)
		if err := s.startMonitorCapture(ctx, mirrored); err != nil {
			return fail(fmt.Errorf("mirror %s again: %w", mirrored, err))
		}
		if err := s.startPipeline(ctx); err != nil {
			return fail(fmt.Errorf("start the pipeline for %s: %w", s.captured, err))
		}
		return fmt.Errorf("the computer could not add a screen for this device, so it mirrors %s: %w", s.captured, capErr)
	}
	if err := s.startPipeline(ctx); err != nil {
		return fail(fmt.Errorf("start the pipeline for the client's screen: %w", err))
	}
	s.arrangeVirtualScreen(ctx)
	return nil
}

// leaveOwnScreen closes the client's own screen for a switch to mirroring
// and gives the desk back as the end of a session does: its monitors on and,
// when the session left them as it arranged them, the layout from before the
// session (giveDeskBack, closeOwnScreen). Each step runs to the end even when
// the session ends meanwhile (teardownContext): once the capture is
// replaced, cleanup no longer knows the old cast.
func (s *session) leaveOwnScreen(ctx context.Context) {
	stepCtx, cancel := teardownContext(ctx)
	restore := s.giveDeskBack(stepCtx)
	cancel()
	stepCtx, cancel = teardownContext(ctx)
	s.closeOwnScreen(stepCtx, restore)
	cancel()
	s.useCapture(nil)
	s.captured = ""
	s.captureW, s.captureH, s.captureFPS = 0, 0, 0
	s.appliedScale = 0
	s.ownAlone, s.ownPrimary, s.turnedDeskOff = false, false, false
	s.deskArranged = nil
}

// soundRouter moves the desktop's sound to a session and captures it.
type soundRouter interface {
	// Redirect makes a silent DeskLink output the default
	// (desktop.RedirectAudio).
	Redirect(ctx context.Context) (desktop.AudioRoute, error)
	// Restore makes the output from before the default again.
	Restore(ctx context.Context, r desktop.AudioRoute) error
	// Capture encodes what plays on device (video.StartAudio).
	Capture(device string, onFrame func(video.Frame)) (soundCapture, error)
}

// soundCapture is a running sound pipeline: *video.AudioEncoder, or a fake
// in tests.
type soundCapture interface {
	pipeline
	Errors() <-chan error
	Done() <-chan struct{}
}

// liveSound is soundRouter on the desk: pactl through pipewire-pulse, and
// GStreamer.
type liveSound struct{}

func (liveSound) Redirect(ctx context.Context) (desktop.AudioRoute, error) {
	return desktop.RedirectAudio(ctx)
}

func (liveSound) Restore(ctx context.Context, r desktop.AudioRoute) error { return r.Restore(ctx) }

func (liveSound) Capture(device string, onFrame func(video.Frame)) (soundCapture, error) {
	enc, err := video.StartAudio(device, onFrame)
	if err != nil {
		return nil, err
	}
	return enc, nil
}
