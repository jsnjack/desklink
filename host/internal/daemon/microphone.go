package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"desklink/internal/desktop"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

// micRouteTimeout bounds creating or removing the virtual microphone.
const micRouteTimeout = 10 * time.Second

// microphone is a running virtual microphone on the desk and what plays the
// client's microphone into it: *liveMicrophone, or a fake in tests.
type microphone interface {
	// Push plays one of the client's Opus frames; it must not block.
	Push(counter uint16, frame []byte)
	// Close stops playing and removes the microphone. A microphone whose
	// removal failed can be closed again.
	Close(ctx context.Context) error
}

// liveMicrophone is the virtual microphone in PipeWire and the GStreamer
// pipeline that plays into it.
type liveMicrophone struct {
	d     *Daemon
	route desktop.MicRoute
	pipe  *video.Microphone
}

// openLiveMicrophone creates the virtual microphone, makes it the default
// source and starts playing into it. On failure, what it made is removed
// again; if that fails too, the microphone comes back with the error, so the
// session's cleanup can retry the removal.
func (d *Daemon) openLiveMicrophone(ctx context.Context) (microphone, error) {
	route, err := desktop.CreateMicrophone(ctx)
	m := &liveMicrophone{d: d, route: route}
	if route.Pending() {
		// The next start gives the default back should this run crash.
		if serr := d.state.SetMicDefaultSource(route.PreviousSource); serr != nil {
			slog.Debug("save the source to restore", "err", serr)
		}
	}
	if err != nil {
		if route.Pending() {
			return m, err
		}
		return nil, err
	}
	if m.pipe, err = video.StartMicrophone(route.Feed()); err != nil {
		undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), micRouteTimeout)
		defer cancel()
		if cerr := m.Close(undo); cerr != nil {
			return m, errors.Join(err, cerr)
		}
		return nil, err
	}
	go func(p *video.Microphone) {
		select {
		case err := <-p.Errors():
			slog.Warn("microphone playback stopped", "err", err)
		case <-p.Done():
		}
	}(m.pipe)
	return m, nil
}

func (m *liveMicrophone) Push(counter uint16, frame []byte) {
	if m.pipe != nil {
		m.pipe.Push(counter, frame)
	}
}

func (m *liveMicrophone) Close(ctx context.Context) error {
	if m.pipe != nil {
		m.d.stalls.finishWithin("the microphone pipeline's teardown", pipelineStopWait, m.pipe.Stop)
		m.pipe = nil
	}
	if err := m.route.Restore(ctx); err != nil {
		return err
	}
	if err := m.d.state.SetMicDefaultSource(""); err != nil {
		slog.Debug("clear saved microphone source", "err", err)
	}
	return nil
}

// startMic gives the desk a virtual microphone that plays the client's
// microphone, unless it has one already.
func (s *session) startMic(ctx context.Context) error {
	s.micMu.Lock()
	on := s.micOn
	s.micMu.Unlock()
	if on {
		return nil
	}
	// A microphone whose removal failed goes first, so two never exist.
	s.removeMic(ctx)
	s.micMu.Lock()
	left := s.mic != nil
	s.micMu.Unlock()
	if left {
		return errors.New("the last microphone could not be removed")
	}
	ctx, cancel := context.WithTimeout(ctx, micRouteTimeout)
	defer cancel()
	end := s.d.stalls.begin("creating the virtual microphone")
	m, err := s.d.openMic(ctx)
	end()
	s.micMu.Lock()
	defer s.micMu.Unlock()
	s.mic, s.micOn = m, err == nil
	return err
}

// removeMic stops the client's microphone and removes the virtual one from
// the desk; one whose removal fails stays for the next try, at the latest in
// cleanup.
func (s *session) removeMic(ctx context.Context) {
	s.micMu.Lock()
	m := s.mic
	s.micOn = false
	s.micMu.Unlock()
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, micRouteTimeout)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		slog.Warn("remove the DeskLink microphone", "err", err)
		return
	}
	s.micMu.Lock()
	s.mic = nil
	s.micMu.Unlock()
}

// setMic turns the virtual microphone on or off as the client asked and
// reports the result in the session description.
func (s *session) setMic(ctx context.Context, on bool) {
	if on {
		if err := s.startMic(ctx); err != nil {
			s.reportError(ctx, errors.New("the desk could not create the DeskLink microphone: "+err.Error()))
		}
	} else {
		s.removeMic(ctx)
	}
	s.sendSessionInfo(ctx)
}

// micState is SessionInfo.Mic: whether the virtual microphone plays the
// client's microphone. It is always present, so clients offer the switch.
func (s *session) micState() *bool {
	s.micMu.Lock()
	defer s.micMu.Unlock()
	on := s.micOn
	return &on
}

// HandleMicrophone implements net.MediaHandler: it plays the client's frame
// while the virtual microphone is on. Frames that arrive while it is off,
// or once the session has ended, are dropped.
func (s *session) HandleMicrophone(p dlnet.AudioPacket) {
	if s.ctx.Err() != nil {
		return
	}
	s.micMu.Lock()
	defer s.micMu.Unlock()
	if s.micOn {
		s.mic.Push(p.Counter, p.Data)
	}
}

// restoreLeftoverMic removes a virtual microphone that a crashed run left
// behind and makes the saved source the default again.
func (d *Daemon) restoreLeftoverMic(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()
	if err := desktop.RemoveLeftoverMicrophone(ctx, d.state.MicDefaultSource()); err != nil {
		slog.Debug("remove microphone left by a previous run", "err", err)
		return
	}
	if err := d.state.SetMicDefaultSource(""); err != nil {
		slog.Debug("clear saved microphone source", "err", err)
	}
}
