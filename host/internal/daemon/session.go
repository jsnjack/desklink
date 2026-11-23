package daemon

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	display   string
	// displayChanged is set once this session switched a monitor mode, and
	// switched names the connector it switched.
	displayChanged bool
	switched       string
	// captured is the connector the portal captures, once known.
	captured string
	// levelChanges carries the latest level the controller chose from the
	// UDP goroutine to the session loop, which owns the pipeline.
	levelChanges chan int

	pointerMu sync.Mutex
	pointer   pointerMap
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
	s := &session{d: d, conn: conn, active: active, ctx: ctx, cancel: cancel}
	s.codec = chooseCodec(video.Codec(hello.Codec))
	s.width, s.height = clampDim(hello.Width, 2560), clampDim(hello.Height, 1600)
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
	s.display = s.matchResolution(ctx, s.width, s.height)
	s.resolveMonitor(ctx)

	capCtx, capCancel := context.WithTimeout(ctx, captureTimeout)
	s.capture, err = d.cast.Start(capCtx, s.captured)
	capCancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil // the client left while the cast was starting
		}
		return err
	}
	s.learnCapturedMonitor(ctx)
	s.updatePointerMap(ctx)

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

// matchResolution switches the captured monitor (the primary one until the
// capture is known) to the client size if it has such a mode, saving the
// original layout first so it can be restored, and describes the outcome for
// the HUD. The mode's refresh is planned for the fastest ladder level of
// this size, so frame-rate steps never need a mode change.
func (s *session) matchResolution(ctx context.Context, width, height int) string {
	d := s.d
	d.displayMu.Lock()
	defer d.displayMu.Unlock()
	st, err := d.bus.DisplayState(ctx)
	if err != nil {
		slog.Warn("read monitor state", "err", err)
		return "monitor state unavailable; scaled to fit"
	}
	s.qMu.Lock()
	fps := max(s.fps, video.MaxFPSAt(s.quality.Levels(), width, height))
	s.qMu.Unlock()
	plan, err := desktop.PlanResolution(st, s.captured, width, height, fps)
	if errors.Is(err, desktop.ErrNoMatchingMode) {
		// An earlier level may have switched the monitor; scaling a smaller
		// mode up would only blur the desktop.
		s.restoreOriginalLocked(ctx)
		return fmt.Sprintf("desktop has no %dx%d mode; scaled with borders", width, height)
	}
	if err != nil {
		slog.Warn("plan resolution", "err", err)
		return "scaled to fit"
	}
	if !plan.Changed {
		return fmt.Sprintf("%s already at %dx%d@%.0fHz", plan.Connector, width, height, plan.Mode.Refresh)
	}
	savedHere := false
	if d.state.DisplaySnapshot() == nil {
		current, err := st.CurrentLayout()
		if err != nil {
			slog.Warn("snapshot monitor layout", "err", err)
			return "scaled to fit"
		}
		raw, err := json.Marshal(current)
		if err != nil {
			slog.Warn("encode monitor layout", "err", err)
			return "scaled to fit"
		}
		if err := d.state.SetDisplaySnapshot(raw); err != nil {
			slog.Warn("save monitor layout", "err", err)
			return "scaled to fit"
		}
		savedHere = true
	}
	// Mutter applies the call even if the client leaves meanwhile, so the
	// call must not be cut short by the session ending.
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applyTimeout)
	defer cancel()
	if err := d.bus.ApplyLayout(applyCtx, st.Serial, plan.Layout); err != nil {
		slog.Warn("switch resolution", "err", err)
		if errors.Is(err, context.DeadlineExceeded) {
			// Mutter may still apply it: keep the snapshot so cleanup restores.
			s.displayChanged, s.switched = true, plan.Connector
			return "resolution switch timed out; scaled to fit"
		}
		if savedHere {
			if err := d.state.SetDisplaySnapshot(nil); err != nil {
				slog.Warn("clear unused monitor layout", "err", err)
			}
		}
		return "resolution switch failed; scaled to fit"
	}
	s.displayChanged, s.switched = true, plan.Connector
	return fmt.Sprintf("switched %s to %dx%d@%.0fHz", plan.Connector, width, height, plan.Mode.Refresh)
}

// restoreOriginalLocked puts back the layout this session changed. The
// caller holds displayMu.
func (s *session) restoreOriginalLocked(ctx context.Context) {
	if !s.displayChanged {
		return
	}
	if raw := s.d.state.DisplaySnapshot(); raw != nil {
		if err := s.d.restoreDisplayLocked(ctx, raw); err != nil {
			slog.Warn("restore monitor layout", "err", err)
			return
		}
	}
	s.displayChanged, s.switched = false, ""
}

// resolveMonitor names the monitor to record: the configured one when it is
// connected, otherwise the primary monitor, which matchResolution switched.
func (s *session) resolveMonitor(ctx context.Context) {
	st, err := s.d.bus.DisplayState(ctx)
	if err != nil {
		slog.Debug("read monitor layout for the recorded monitor", "err", err)
		return
	}
	if c := st.MonitorFor(s.captured); c != "" {
		s.captured = c
	}
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
		NodeID: s.capture.NodeID,
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
		slog.Log(s.ctx, dlnet.LevelTrace, "keyframe", "bytes", len(f.Data))
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
		if e.Kind == dlnet.InputAbsMove || e.Kind == dlnet.InputAbsPointer {
			s.pointerMu.Lock()
			e.X, e.Y = s.pointer.apply(e.X, e.Y)
			s.pointerMu.Unlock()
		}
		ev := input.Event{Kind: uint8(e.Kind), Flags: e.Flags, Code: e.Code, X: e.X, Y: e.Y}
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
	s.display = s.matchResolution(ctx, w, h)
	s.updatePointerMap(ctx)
	if err := s.startPipeline(ctx); err != nil {
		s.cancel(fmt.Errorf("restart pipeline at %dx%d: %w", w, h, err))
		return
	}
	s.sendSessionInfo(ctx)
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
	if s.capture != nil {
		if err := s.capture.Close(); err != nil {
			slog.Debug("close screencast", "err", err)
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
	s.d.displayMu.Lock()
	s.restoreOriginalLocked(ctx)
	s.d.displayMu.Unlock()
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
