package daemon

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"desklink/internal/crypto"
	"desklink/internal/desktop"
	"desklink/internal/input"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

const (
	helloTimeout   = 10 * time.Second
	controlQueue   = 64
	captureTimeout = 15 * time.Second
	applyTimeout   = 10 * time.Second
	hostNonceSize  = 16
	keyframeMinGap = 150 * time.Millisecond
	copyReadDelay  = 250 * time.Millisecond
	minStreamDim   = 320
	maxStreamDim   = 4096
	defaultFPS     = 60
	maxFPS         = 120
	inhibitApp     = "DeskLink"
	inhibitReason  = "Active remote session"
	// defaultScale makes a 2560x1600 tablet screen 1280x800 points, the size
	// of a 13-inch laptop's interface.
	defaultScale       = 2.0
	virtualMonitorWait = 5 * time.Second
	// virtualMonitorPoll is short: until the virtual monitor is arranged,
	// Mutter's own layout may have moved the desk's monitors.
	virtualMonitorPoll = 50 * time.Millisecond
)

// session is one connected client.
type session struct {
	d      *Daemon
	conn   *dlnet.ControlConn
	active *activeSession
	ctx    context.Context
	cancel context.CancelCauseFunc

	codec         video.Codec
	width, height int
	fps           int

	media   *dlnet.MediaSession
	capture *desktop.Capture
	apps    []desktop.App

	// encMu guards enc but is never held across a pipeline start or stop,
	// because the UDP goroutine takes it for keyframes and QP.
	encMu sync.Mutex
	enc   *video.Encoder

	qMu     sync.Mutex
	quality *video.QualityController

	rttNanos     atomic.Int64
	rttFresh     atomic.Bool
	lastKeyframe atomic.Int64
	copyDetector input.CopyDetector

	uninhibit func() error
	wasLocked bool

	// audio streams the desktop's sound; audioRoute is the virtual output
	// it plays into while the session runs. Both are nil without audio.
	audio      *video.AudioEncoder
	audioRoute *desktop.AudioRoute
	display    string
	// scale is the virtual screen's scale; captureW, captureH and captureFPS
	// its mode, zero when mirroring a monitor. deskMonitors are the
	// connectors present before the cast, to tell the virtual monitor apart.
	scale                          float64
	appliedScale                   float64
	captureW, captureH, captureFPS int
	deskMonitors                   []string
	// deskState is the monitor state before the cast, which the virtual
	// screen's layout keeps and the end of the session returns to.
	deskState desktop.DisplayState

	// captured is the connector the portal captures, once known.
	captured string
	// levelChanges carries the latest level the controller chose from the
	// UDP goroutine to the session loop, which owns the pipeline.
	levelChanges chan int

	pointerMu sync.Mutex
	pointer   pointerMap
	// screenW and screenH are the client's screen size, which relative
	// motion is measured in. They are set before input arrives and never
	// change, so a level change does not change the trackpad's speed.
	screenW, screenH int
	// cursor is the client's pointer on its screen; only the UDP goroutine
	// uses it.
	cursor clientCursor
	// penTilt is the tilt the client last reported for its pen; only the
	// UDP goroutine uses it.
	penTilt struct {
		known bool
		x, y  int32
	}
}

func (d *Daemon) runSession(parent context.Context, conn *dlnet.ControlConn, active *activeSession) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	hello, err := receiveHello(ctx, conn)
	if err != nil {
		return err
	}
	// coder/websocket answers pings only inside Read, and setup can wait up to
	// captureTimeout for the portal, so a reader runs from now on. It reads
	// with parent, not ctx: cancelling a pending read closes the connection,
	// and the caller still has to send a setup error over it.
	msgs := make(chan dlnet.Message, controlQueue)
	go func() {
		for {
			m, err := conn.Receive(parent)
			if err != nil {
				if parent.Err() == nil && !dlnet.IsNormalClose(err) {
					slog.Warn("control connection failed", "peer", conn.Peer.Name, "err", err)
				}
				cancel(nil)
				return
			}
			select {
			case msgs <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	cfg := d.opts.Config
	s := &session{d: d, conn: conn, active: active, ctx: ctx, cancel: cancel, cursor: newClientCursor()}
	s.codec = chooseCodec(video.Codec(hello.Codec))
	s.width, s.height = clampDim(hello.Width, 2560), clampDim(hello.Height, 1600)
	s.screenW, s.screenH = s.width, s.height
	s.fps = hello.FPS
	if s.fps <= 0 || s.fps > maxFPS {
		s.fps = defaultFPS
	}
	levels := video.Levels(s.width, s.height, s.fps)
	if !hello.Auto {
		levels = levels[:1]
	}
	start := 0
	if hello.Start != nil {
		start = video.LevelFor(levels, video.Level{Width: hello.Start.Width, Height: hello.Start.Height, FPS: hello.Start.FPS})
	}
	s.quality = video.NewQualityController(hello.QP, cfg.MinQP, cfg.MaxQP, levels, start, hello.Auto)
	top := s.quality.Levels()[s.quality.Level()]
	s.width, s.height, s.fps = top.Width, top.Height, top.FPS
	s.levelChanges = make(chan int, 1)
	s.captured = cfg.Monitor
	defer s.cleanup()

	s.prepareDesktop(ctx)
	s.scale = hello.Scale
	if s.scale <= 0 {
		s.scale = defaultScale
	}
	// GNOME re-plans every monitor for a moment when a screen is added, so
	// the client's own screen is added by default only when nobody uses the
	// desk: the session was locked when the client came.
	own := hello.Screen == dlnet.ScreenOwn || (hello.Screen != dlnet.ScreenMirror && s.wasLocked)
	capErr := errMirrorChosen
	if own {
		capErr = s.startVirtualCapture(ctx)
	}
	if capErr != nil {
		if ctx.Err() != nil {
			return nil // the client left while the cast was starting
		}
		if own {
			slog.Warn("no virtual screen; mirroring a monitor instead", "err", capErr)
		}
		if err := s.startMonitorCapture(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}

	sessionID, hostNonce, err := newSessionIDs()
	if err != nil {
		return err
	}
	keys, err := crypto.DeriveSessionKeys(conn.Peer.Key, sessionID, hostNonce)
	if err != nil {
		return err
	}
	if s.media, err = d.media.Open(sessionID, keys, conn.RemoteIP, s); err != nil {
		return err
	}
	if err := s.startPipeline(ctx); err != nil {
		return err
	}
	if s.capture.Virtual {
		s.arrangeVirtualScreen(ctx)
	}
	if hello.Audio {
		s.startAudio(ctx)
	}
	info := s.sessionInfo()
	info.HostNonce = hostNonce
	if err := conn.Send(ctx, dlnet.MsgSession, info); err != nil {
		return err
	}
	s.apps = desktop.ScanApps(desktop.DefaultAppDirs(), cfg.PinnedApps)
	if err := conn.Send(ctx, dlnet.MsgApps, s.apps); err != nil {
		return err
	}

	go func() {
		if err := s.media.RunKeepalive(ctx); err != nil {
			cancel(err)
		}
	}()
	go s.watchClipboard(ctx)

	for {
		select {
		case msg := <-msgs:
			s.handleMessage(ctx, msg)
		case level := <-s.levelChanges:
			s.applyLevel(ctx, level)
		case <-ctx.Done():
			if cause := context.Cause(ctx); !errors.Is(cause, context.Canceled) {
				return cause
			}
			return nil
		}
	}
}

func receiveHello(ctx context.Context, conn *dlnet.ControlConn) (dlnet.Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	msg, err := conn.Receive(ctx)
	if err != nil {
		return dlnet.Hello{}, fmt.Errorf("wait for hello: %w", err)
	}
	if msg.Type != dlnet.MsgHello {
		return dlnet.Hello{}, fmt.Errorf("first message is %q, want hello", msg.Type)
	}
	var h dlnet.Hello
	if err := json.Unmarshal(msg.Payload, &h); err != nil {
		return dlnet.Hello{}, fmt.Errorf("decode hello: %w", err)
	}
	return h, nil
}

func chooseCodec(want video.Codec) video.Codec {
	if want == video.CodecHEVC || !video.Available(video.CodecAV1) {
		return video.CodecHEVC
	}
	return video.CodecAV1
}

// clampDim keeps a requested dimension even and within encoder limits.
func clampDim(v, fallback int) int {
	if v < minStreamDim || v > maxStreamDim {
		return fallback
	}
	return v &^ 1
}

func newSessionIDs() (uint32, []byte, error) {
	b := make([]byte, 4+hostNonceSize)
	if _, err := rand.Read(b); err != nil {
		return 0, nil, fmt.Errorf("random session ID: %w", err)
	}
	id := binary.BigEndian.Uint32(b)
	if id == 0 {
		id = 1
	}
	return id, b[4:], nil
}

// prepareDesktop wakes the screen, unlocks the session and stops idle
// blanking. Failures are logged; the stream may still be usable.
func (s *session) prepareDesktop(ctx context.Context) {
	bus := s.d.bus
	if err := bus.WakeDisplay(ctx); err != nil {
		slog.Warn("wake display", "err", err)
	}
	locked, err := bus.UnlockSession(ctx)
	if err != nil {
		slog.Warn("unlock session", "err", err)
	}
	// A session this one replaced may have unlocked the desk already.
	s.wasLocked = s.d.takeRelock() || locked
	release, err := bus.InhibitIdle(ctx, inhibitApp, inhibitReason)
	if err != nil {
		slog.Warn("inhibit idle", "err", err)
		return
	}
	s.uninhibit = release
}

// startVirtualCapture gives the session its own screen: a virtual monitor
// the size of the top quality level, which exists while the cast runs.
// Mutter adds it once the pipeline negotiates its size, and when the cast
// stops it returns the desk's monitors to the user's saved layout by itself,
// so no snapshot is kept. arrangeVirtualScreen then sets its scale.
func (s *session) startVirtualCapture(ctx context.Context) error {
	s.qMu.Lock()
	top := s.quality.Levels()[0]
	s.qMu.Unlock()
	st, err := s.d.bus.DisplayState(ctx)
	if err != nil {
		return err
	}
	s.deskMonitors = st.Connectors()
	s.deskState = st
	// Until the virtual monitor shows up, the session records no monitor of
	// the desk: input and cleanup must not act on the configured one.
	s.captured = ""
	capCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()
	c, err := s.d.cast.StartVirtual(capCtx)
	if err != nil {
		return err
	}
	s.capture = c
	s.captureW, s.captureH, s.captureFPS = top.Width, top.Height, top.FPS
	s.display = fmt.Sprintf("own %dx%d screen", top.Width, top.Height)
	return nil
}

// arrangeVirtualScreen waits for the virtual monitor to join the layout,
// puts the desk's monitors back where they were (Mutter lays out an unknown
// monitor set from scratch), places the virtual screen to their right at the
// session's scale and maps input onto it. The primary moves to the virtual
// screen only when the desk was locked, so nobody is using it.
func (s *session) arrangeVirtualScreen(ctx context.Context) {
	defer s.updatePointerMap(ctx)
	conn, err := s.waitVirtualMonitor(ctx)
	if err != nil {
		slog.Warn("virtual screen did not appear in the layout", "err", err)
		return
	}
	s.captured = conn
	if err := s.placeVirtualScreen(ctx); err != nil {
		slog.Warn("place the virtual screen", "err", err)
	}
}

// placeVirtualScreen lays out the desk as before the session with the
// virtual screen to its right at the session's scale, or the supported scale
// nearest it, and records the scale applied.
func (s *session) placeVirtualScreen(ctx context.Context) error {
	s.d.displayMu.Lock()
	defer s.d.displayMu.Unlock()
	st, err := s.d.bus.DisplayState(ctx)
	if err != nil {
		return fmt.Errorf("read monitor state: %w", err)
	}
	// The virtual screen becomes primary (top bar, overview) only when
	// nobody uses the desk: the session was locked when the client came.
	layout, err := desktop.PlanVirtual(s.deskState, st, s.captured, s.scale, s.wasLocked)
	if err != nil {
		return err
	}
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applyTimeout)
	defer cancel()
	if err := s.d.bus.ApplyLayout(applyCtx, st.Serial, layout); err != nil {
		return fmt.Errorf("apply the layout: %w", err)
	}
	s.appliedScale = layout.Logical[len(layout.Logical)-1].Scale
	s.display = fmt.Sprintf("own %dx%d screen at %.0f%%", s.captureW, s.captureH, s.appliedScale*100)
	return nil
}

// Own screen scales a client may ask for.
const (
	minScale = 1.0
	maxScale = 3.0
)

// setScale applies a new scale to the client's own screen at once, keeping
// the desk's monitors as they are. Mirroring ignores it until the client's
// own screen is used.
func (s *session) setScale(ctx context.Context, scale float64) {
	if scale < minScale || scale > maxScale {
		s.reportError(ctx, fmt.Errorf("scale %.2f is outside %.0f–%.0f", scale, minScale, maxScale))
		return
	}
	s.scale = scale
	if s.capture != nil && s.capture.Virtual && s.captured != "" {
		if err := s.placeVirtualScreen(ctx); err != nil {
			s.reportError(ctx, fmt.Errorf("change the scale: %w", err))
			return
		}
		s.updatePointerMap(ctx)
		// Mutter's cast of the virtual screen stops delivering frames once
		// its scale changes; a new pipeline connects to it again.
		s.stopPipeline()
		if err := s.startPipeline(ctx); err != nil {
			s.cancel(fmt.Errorf("restart the pipeline after a scale change: %w", err))
			return
		}
	}
	s.sendSessionInfo(ctx)
}

// waitVirtualMonitor polls the layout for a monitor that was not there
// before the cast and has a place in it.
func (s *session) waitVirtualMonitor(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, virtualMonitorWait)
	defer cancel()
	t := time.NewTicker(virtualMonitorPoll)
	defer t.Stop()
	for {
		if st, err := s.d.bus.DisplayState(ctx); err == nil {
			for _, lm := range st.Logical {
				for _, c := range lm.Connectors {
					if !slices.Contains(s.deskMonitors, c) {
						return c, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.C:
		}
	}
}

// restoreDesk returns the desk's monitors to the layout from before the
// session when Mutter did not: it picks the user's saved layout for the
// monitors left, which differs only if the layout was not saved. An unchanged
// desk is left alone.
func (s *session) restoreDesk(ctx context.Context) {
	layout, err := s.deskState.CurrentLayout()
	if err != nil {
		return
	}
	s.d.displayMu.Lock()
	defer s.d.displayMu.Unlock()
	if err := s.d.bus.RestoreLayout(ctx, layout); err != nil {
		slog.Warn("restore the desk's monitor layout", "err", err)
	}
}

// waitVirtualGone waits until the stopped cast's virtual monitor has left the
// layout, so restoring the desk's layout does not race Mutter removing it.
func (s *session) waitVirtualGone(ctx context.Context) {
	if s.captured == "" || slices.Contains(s.deskMonitors, s.captured) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, virtualMonitorWait)
	defer cancel()
	t := time.NewTicker(virtualMonitorPoll)
	defer t.Stop()
	for {
		st, err := s.d.bus.DisplayState(ctx)
		if err != nil || !slices.Contains(st.Connectors(), s.captured) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// startMonitorCapture mirrors a physical monitor without changing it: the
// configured one, otherwise the one whose shape is closest to the client's.
// Anything at the desk stays as it is.
func (s *session) startMonitorCapture(ctx context.Context) error {
	if st, err := s.d.bus.DisplayState(ctx); err != nil {
		slog.Debug("read monitor layout to pick the monitor to mirror", "err", err)
	} else {
		s.captured = st.BestFit(s.captured, s.width, s.height)
	}
	capCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()
	c, err := s.d.cast.Start(capCtx, s.captured)
	if err != nil {
		return err
	}
	s.capture = c
	s.learnCapturedMonitor(ctx)
	s.updatePointerMap(ctx)
	s.display = "mirroring " + s.captured
	return nil
}

// learnCapturedMonitor names the recorded monitor from the stream's position
// when the layout could not be read before the cast started.
func (s *session) learnCapturedMonitor(ctx context.Context) {
	if s.captured != "" {
		return
	}
	st, err := s.d.bus.DisplayState(ctx)
	if err != nil {
		slog.Debug("read monitor layout for the recorded monitor", "err", err)
		return
	}
	if connector, ok := st.ConnectorAt(s.capture.X, s.capture.Y); ok {
		s.captured = connector
	}
}

// startPipeline starts a pipeline for the current size. The caller has
// stopped the previous one.
func (s *session) startPipeline(ctx context.Context) error {
	s.qMu.Lock()
	qp := s.quality.QP()
	s.qMu.Unlock()
	enc, err := video.Start(video.Settings{
		Codec: s.codec, Width: s.width, Height: s.height, QP: qp, FPS: s.fps,
		NodeID:       s.capture.NodeID,
		CaptureWidth: s.captureW, CaptureHeight: s.captureH, CaptureFPS: s.captureFPS,
	}, s.onFrame)
	if err != nil {
		return err
	}
	s.encMu.Lock()
	s.enc = enc
	s.encMu.Unlock()
	go func() {
		select {
		case err := <-enc.Errors():
			s.cancel(err)
		case <-enc.Done():
			select {
			case err := <-enc.Errors():
				s.cancel(err)
			default:
			}
		case <-ctx.Done():
		}
	}()
	return nil
}

// stopPipeline stops the running pipeline, if any, outside encMu.
func (s *session) stopPipeline() {
	s.encMu.Lock()
	enc := s.enc
	s.enc = nil
	s.encMu.Unlock()
	if enc != nil {
		enc.Stop()
	}
}

func (s *session) onFrame(f video.Frame) {
	if f.Keyframe {
		slog.Log(s.ctx, dlnet.LevelTrace, "keyframe", "bytes", len(f.Data), "since_capture", time.Since(f.Captured))
	}
	if err := s.media.SendFrame(f.Data, f.Keyframe, f.Captured); err != nil {
		if errors.Is(err, dlnet.ErrSequenceExhausted) {
			s.cancel(err)
			return
		}
		slog.Debug("send frame", "err", err)
	}
}

func (s *session) requestKeyframe() {
	now := time.Now().UnixNano()
	last := s.lastKeyframe.Load()
	if now-last < int64(keyframeMinGap) || !s.lastKeyframe.CompareAndSwap(last, now) {
		return
	}
	s.encMu.Lock()
	defer s.encMu.Unlock()
	if s.enc != nil && !s.enc.ForceKeyframe() {
		slog.Debug("encoder ignored the keyframe request")
	}
}

func (s *session) setQP(qp int) {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	if s.enc != nil {
		s.enc.SetQP(qp)
	}
}

// updatePointerMap recomputes how absolute input maps onto the desktop,
// from the captured monitor's place and the current monitor layout.
func (s *session) updatePointerMap(ctx context.Context) {
	m := identityMap
	if st, err := s.d.bus.DisplayState(ctx); err != nil {
		slog.Debug("read monitor layout for pen mapping", "err", err)
	} else {
		// The stream reports the place at capture start; a resolution change
		// can move the monitor, so look it up by connector.
		monitor := desktop.Rect{X: s.capture.X, Y: s.capture.Y, W: s.capture.Width, H: s.capture.Height}
		if r, ok := st.MonitorRect(s.captured); ok {
			monitor = r
		} else if rects := st.LogicalRects(); monitor.W == 0 && len(rects) > 0 {
			monitor = rects[0]
		}
		m = newPointerMap(s.width, s.height, monitor, st.Stage())
	}
	s.pointerMu.Lock()
	s.pointer = m
	s.pointerMu.Unlock()
}

// HandleInput implements net.MediaHandler.
func (s *session) HandleInput(seq uint32, events []dlnet.InputEvent) {
	events = confine(&s.cursor, events, s.screenW, s.screenH)
	var held *input.Held
	for _, e := range events {
		if e.Kind == dlnet.InputHeld {
			if held == nil {
				held = &input.Held{}
			}
			addHeld(held, e)
			continue
		}
		if e.Kind == dlnet.InputKey && s.copyDetector.Observe(e.Code, e.Flags == 1) {
			time.AfterFunc(copyReadDelay, func() { s.sendClipboard(s.ctx, false) })
		}
		if e.Kind == dlnet.InputPenTilt {
			s.penTilt.known, s.penTilt.x, s.penTilt.y = true, e.X, e.Y
			continue
		}
		if e.Kind == dlnet.InputAbsMove || e.Kind == dlnet.InputAbsPointer {
			s.pointerMu.Lock()
			e.X, e.Y = s.pointer.apply(e.X, e.Y)
			s.pointerMu.Unlock()
		}
		ev := input.Event{Kind: uint8(e.Kind), Flags: e.Flags, Code: e.Code, X: e.X, Y: e.Y}
		if e.Kind == dlnet.InputAbsMove {
			ev.Tilt, ev.TiltX, ev.TiltY = s.penTilt.known, s.penTilt.x, s.penTilt.y
		}
		if err := s.d.injector.HandleAt(seq, ev); err != nil {
			slog.Debug("inject input", "err", err)
		}
		if e.Kind == dlnet.InputKey {
			slog.Log(s.ctx, dlnet.LevelTrace, "key", "code", input.KeyName(e.Code), "down", e.Flags == 1)
		} else {
			slog.Log(s.ctx, dlnet.LevelTrace, "input", "kind", e.Kind, "flags", e.Flags, "code", e.Code, "x", e.X, "y", e.Y)
		}
	}
	if held != nil {
		if err := s.d.injector.Reconcile(seq, *held); err != nil {
			slog.Debug("reconcile held input", "err", err)
		}
	}
}

// Flags of held-input records.
const (
	heldKey    = 1
	heldButton = 2
	heldPen    = 3

	penHeldTouch   = 1
	penHeldInRange = 2
)

func addHeld(h *input.Held, e dlnet.InputEvent) {
	switch e.Flags {
	case heldKey:
		h.Keys = append(h.Keys, e.Code)
	case heldButton:
		h.Buttons = append(h.Buttons, e.Code)
	case heldPen:
		h.PenTouch = e.Code&penHeldTouch != 0
		h.PenInRange = e.Code&penHeldInRange != 0
	}
}

// HandleTelemetry implements net.MediaHandler.
func (s *session) HandleTelemetry(t dlnet.Telemetry) {
	slog.Log(s.ctx, dlnet.LevelTrace, "telemetry", "loss_pct", float64(t.LossPctX100)/100,
		"need_idr", t.NeedIDR, "last_frame", t.LastContiguousFrameID)
	if t.NeedIDR {
		s.requestKeyframe()
	}
	// The controller gets an RTT only with the report after a new echo.
	var rtt time.Duration
	if s.rttFresh.Swap(false) {
		rtt = time.Duration(s.rttNanos.Load())
	}
	s.qMu.Lock()
	d := s.quality.Observe(time.Now(), float64(t.LossPctX100)/100, rtt)
	s.qMu.Unlock()
	if d.LevelChanged {
		// applyLevel sets the new quantizer with the new level; setting it on
		// the old, larger pipeline first would raise its bitrate.
		slog.Debug("changing quality level", "level", d.Level)
		select {
		case <-s.levelChanges:
		default:
		}
		s.levelChanges <- d.Level
		return
	}
	if d.QPChanged {
		slog.Debug("adapting quantizer", "qp", d.QP, "loss_pct", float64(t.LossPctX100)/100, "rtt", rtt)
		s.setQP(d.QP)
	}
}

// HandleRTT implements net.MediaHandler.
func (s *session) HandleRTT(rtt time.Duration) {
	s.rttNanos.Store(int64(rtt))
	s.rttFresh.Store(true)
}

func (s *session) handleMessage(ctx context.Context, msg dlnet.Message) {
	switch msg.Type {
	case dlnet.MsgLaunch:
		var l dlnet.Launch
		if err := json.Unmarshal(msg.Payload, &l); err != nil {
			s.reportError(ctx, fmt.Errorf("decode launch: %w", err))
			return
		}
		app, err := desktop.Find(s.apps, l.ID, l.Exec)
		if err == nil {
			s.pointAtClientScreen()
			err = s.d.launcher.Launch(app)
		}
		if err != nil {
			s.reportError(ctx, err)
		}
	case dlnet.MsgClipboard:
		var c dlnet.Clipboard
		if err := json.Unmarshal(msg.Payload, &c); err != nil {
			s.reportError(ctx, fmt.Errorf("decode clipboard: %w", err))
			return
		}
		if err := s.d.clipboard.Write(ctx, c.Text); err != nil {
			s.reportError(ctx, err)
		}
	case dlnet.MsgClipboardRequest:
		s.sendClipboard(ctx, true)
	case dlnet.MsgQuality:
		var q dlnet.Quality
		if err := json.Unmarshal(msg.Payload, &q); err != nil {
			s.reportError(ctx, fmt.Errorf("decode quality: %w", err))
			return
		}
		s.applyQuality(ctx, q)
	case dlnet.MsgScale:
		var c dlnet.ScaleChange
		if err := json.Unmarshal(msg.Payload, &c); err != nil {
			s.reportError(ctx, fmt.Errorf("decode scale: %w", err))
			return
		}
		s.setScale(ctx, c.Scale)
	case dlnet.MsgReqIDR:
		s.requestKeyframe()
	default:
		slog.Debug("ignoring control message", "type", msg.Type)
	}
}

func (s *session) applyQuality(ctx context.Context, q dlnet.Quality) {
	cfg := s.d.opts.Config
	if q.QP > 0 {
		s.qMu.Lock()
		s.quality.Reset(q.QP, cfg.MinQP, cfg.MaxQP)
		qp := s.quality.QP()
		s.qMu.Unlock()
		s.setQP(qp)
	}
	w, h := clampDim(q.Width, s.width), clampDim(q.Height, s.height)
	if w == s.width && h == s.height {
		return
	}
	s.resize(ctx, w, h, s.fps)
}

// applyLevel moves to a ladder level: a frame-rate change only adjusts the
// running pipeline, a size change switches the monitor and restarts it.
func (s *session) applyLevel(ctx context.Context, level int) {
	s.qMu.Lock()
	l := s.quality.Levels()[level]
	qp := s.quality.QP()
	s.qMu.Unlock()
	if l.Width == s.width && l.Height == s.height {
		s.fps = l.FPS
		s.encMu.Lock()
		if s.enc != nil {
			s.enc.SetMaxFPS(l.FPS)
			s.enc.SetQP(qp)
		}
		s.encMu.Unlock()
		s.sendSessionInfo(ctx)
		return
	}
	s.resize(ctx, l.Width, l.Height, l.FPS)
}

// resize stops the pipeline before switching the monitor mode, so the old
// pipeline never sees the capture change size, then starts a new one.
func (s *session) resize(ctx context.Context, w, h, fps int) {
	s.stopPipeline()
	s.width, s.height, s.fps = w, h, fps
	// Monitors keep their mode; vapostproc scales the stream.
	s.updatePointerMap(ctx)
	if err := s.startPipeline(ctx); err != nil {
		s.cancel(fmt.Errorf("restart pipeline at %dx%d: %w", w, h, err))
		return
	}
	// The new pipeline asks for the same capture size, so Mutter keeps the
	// virtual monitor. Should it have made a new one anyway, arrange that.
	if s.capture.Virtual && !s.virtualScreenShown(ctx) {
		slog.Info("the client's screen left the layout on a resize; arranging it again")
		s.arrangeVirtualScreen(ctx)
	}
	s.sendSessionInfo(ctx)
}

// virtualScreenShown reports whether the client's screen is still in the
// layout. It answers true when the layout cannot be read.
func (s *session) virtualScreenShown(ctx context.Context) bool {
	if s.captured == "" {
		return true // it never appeared; arranging again would wait in vain
	}
	st, err := s.d.bus.DisplayState(ctx)
	if err != nil {
		return true
	}
	_, ok := st.MonitorRect(s.captured)
	return ok
}

// sessionInfo describes the stream as it is now.
func (s *session) sessionInfo() dlnet.SessionInfo {
	s.qMu.Lock()
	defer s.qMu.Unlock()
	levels := make([]dlnet.Level, 0, len(s.quality.Levels()))
	for _, l := range s.quality.Levels() {
		levels = append(levels, dlnet.Level{Width: l.Width, Height: l.Height, FPS: l.FPS})
	}
	return dlnet.SessionInfo{
		SessionID: s.media.ID(), UDPPort: s.d.media.Port(),
		Codec: string(s.codec), Width: s.width, Height: s.height, FPS: s.fps, QP: s.quality.QP(),
		Display: s.display, Auto: s.quality.Auto(), Level: s.quality.Level(), Levels: levels,
		Audio: s.audio != nil, Screen: s.screenMode(), Scale: s.ownScale(),
	}
}

// errMirrorChosen marks a session that mirrors by choice, not as a fallback.
var errMirrorChosen = errors.New("mirroring a monitor")

// pointAtClientScreen moves the pointer to the middle of the screen the
// client sees. GNOME opens a new window on the monitor under the pointer, so
// an app launched from the client would otherwise open wherever the pointer
// was last, often on a desk monitor the client does not show.
func (s *session) pointAtClientScreen() {
	s.pointerMu.Lock()
	x, y := s.pointer.apply(absMax/2, absMax/2)
	s.pointerMu.Unlock()
	if err := s.d.injector.Handle(input.Event{Kind: uint8(dlnet.InputAbsPointer), X: x, Y: y}); err != nil {
		slog.Debug("move the pointer onto the client's screen", "err", err)
	}
}

// ownScale is the own screen's applied scale, zero when mirroring.
func (s *session) ownScale() float64 {
	if s.capture != nil && s.capture.Virtual {
		return s.appliedScale
	}
	return 0
}

// screenMode names what the client sees, for its HUD and settings.
func (s *session) screenMode() string {
	if s.capture != nil && s.capture.Virtual {
		return dlnet.ScreenOwn
	}
	return dlnet.ScreenMirror
}

// startAudio moves the desktop's sound to a virtual output and streams it.
// Failures are logged and the session goes on without sound; if the virtual
// output cannot be made, the default output's sound is streamed while it
// also plays at the desk.
func (s *session) startAudio(ctx context.Context) {
	device := desktop.DefaultMonitor
	if route, err := desktop.RedirectAudio(ctx); err != nil {
		slog.Warn("move the sound to the DeskLink output; streaming the default output instead", "err", err)
	} else {
		s.audioRoute = &route
		device = route.Monitor()
		if err := s.d.state.SetAudioDefaultSink(route.PreviousSink); err != nil {
			slog.Debug("save the output to restore", "err", err)
		}
	}
	enc, err := video.StartAudio(device, func(f video.Frame) {
		if err := s.media.SendAudio(f.Data, f.Captured); err != nil {
			slog.Log(s.ctx, dlnet.LevelTrace, "send audio", "err", err)
		}
	})
	if err != nil {
		slog.Warn("start audio", "err", err)
		s.stopAudio(ctx)
		return
	}
	s.audio = enc
	go func() {
		select {
		case err := <-enc.Errors():
			slog.Warn("audio stopped", "err", err)
		case <-enc.Done():
		case <-ctx.Done():
		}
	}()
}

// stopAudio stops the audio pipeline and gives the sound back to the output
// that was the default.
func (s *session) stopAudio(ctx context.Context) {
	if s.audio != nil {
		s.audio.Stop()
		s.audio = nil
	}
	if s.audioRoute == nil {
		return
	}
	if err := s.audioRoute.Restore(ctx); err != nil {
		slog.Warn("restore the sound output", "err", err)
		return
	}
	s.audioRoute = nil
	if err := s.d.state.SetAudioDefaultSink(""); err != nil {
		slog.Debug("clear saved audio output", "err", err)
	}
}

func (s *session) sendSessionInfo(ctx context.Context) {
	if err := s.conn.Send(ctx, dlnet.MsgSession, s.sessionInfo()); err != nil {
		slog.Debug("send updated session info", "err", err)
	}
}

func (s *session) sendClipboard(ctx context.Context, always bool) {
	if ctx.Err() != nil {
		return
	}
	text, changed, err := s.d.clipboard.Read(ctx)
	if err != nil {
		slog.Debug("read clipboard", "err", err)
		return
	}
	if (!changed && !always) || text == "" {
		return
	}
	if err := s.conn.Send(ctx, dlnet.MsgClipboard, dlnet.Clipboard{Text: text}); err != nil {
		slog.Debug("send clipboard", "err", err)
	}
}

func (s *session) watchClipboard(ctx context.Context) {
	err := s.d.clipboard.Watch(ctx, func(text string) {
		if err := s.conn.Send(ctx, dlnet.MsgClipboard, dlnet.Clipboard{Text: text}); err != nil {
			slog.Debug("send clipboard", "err", err)
		}
	})
	if errors.Is(err, desktop.ErrWatchUnsupported) {
		slog.Debug("clipboard watch unavailable; syncing after copy shortcuts and on request", "err", err)
		return
	}
	if err != nil {
		slog.Warn("clipboard watch", "err", err)
	}
}

func (s *session) reportError(ctx context.Context, err error) {
	slog.Warn("session request failed", "err", err)
	if serr := s.conn.Send(ctx, dlnet.MsgError, dlnet.ErrorInfo{Message: err.Error()}); serr != nil {
		slog.Debug("send error", "err", serr)
	}
}

// cleanup undoes everything the session changed, in reverse order. It runs
// after the session context is cancelled, so it uses its own timeout.
func (s *session) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
	defer cancel()
	s.stopPipeline()
	s.stopAudio(ctx)
	if s.capture != nil {
		if err := s.capture.Close(); err != nil {
			slog.Debug("close screencast", "err", err)
		}
		if s.capture.Virtual {
			s.waitVirtualGone(ctx)
			s.restoreDesk(ctx)
		}
	}
	if s.media != nil {
		s.media.Close()
		st := s.media.Stats()
		slog.Info("media totals", "frames", st.FramesSent, "packets", st.PacketsSent, "bytes", st.BytesSent)
	}
	if err := s.d.injector.ReleaseAll(); err != nil {
		slog.Warn("release held keys", "err", err)
	}
	if s.uninhibit != nil {
		if err := s.uninhibit(); err != nil {
			slog.Debug("release idle inhibitor", "err", err)
		}
	}
	if s.wasLocked {
		if s.active != nil && s.active.replaced.Load() {
			s.d.passRelock()
			return
		}
		if err := s.d.bus.LockSession(ctx); err != nil {
			slog.Warn("lock the session again", "err", err)
		}
	}
}

var _ dlnet.MediaHandler = (*session)(nil)
