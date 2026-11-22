package daemon

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	helloTimeout      = 10 * time.Second
	captureTimeout    = 45 * time.Second
	hostNonceSize     = 16
	keyframeMinGap    = 150 * time.Millisecond
	copyReadDelay     = 250 * time.Millisecond
	minStreamDim      = 320
	maxStreamDim      = 4096
	defaultFPS        = 60
	maxFPS            = 120
	inhibitApp        = "DeskLink"
	inhibitReason     = "Active remote session"
	captureNeedsGrant = "screen sharing needs approval at the desk: run `desklinkd pair` there once"
)

// session is one connected client.
type session struct {
	d      *Daemon
	conn   *dlnet.ControlConn
	ctx    context.Context
	cancel context.CancelCauseFunc

	codec         video.Codec
	width, height int
	fps           int

	media   *dlnet.MediaSession
	capture *desktop.Capture
	apps    []desktop.App

	encMu  sync.Mutex
	enc    *video.Encoder
	pwFile *os.File

	qMu     sync.Mutex
	quality *video.QualityController

	rttNanos     atomic.Int64
	lastKeyframe atomic.Int64
	copyDetector input.CopyDetector

	uninhibit      func() error
	displayChanged bool
}

func (d *Daemon) runSession(parent context.Context, conn *dlnet.ControlConn) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	hello, err := receiveHello(ctx, conn)
	if err != nil {
		return err
	}
	cfg := d.opts.Config
	s := &session{d: d, conn: conn, ctx: ctx, cancel: cancel}
	s.codec = chooseCodec(video.Codec(hello.Codec))
	s.width, s.height = clampDim(hello.Width, 2560), clampDim(hello.Height, 1600)
	s.fps = hello.FPS
	if s.fps <= 0 || s.fps > maxFPS {
		s.fps = defaultFPS
	}
	s.quality = video.NewQualityController(hello.QP, cfg.MinQP, cfg.MaxQP)
	defer s.cleanup()

	s.prepareDesktop(ctx)
	display := s.matchResolution(ctx, s.width, s.height)

	capCtx, capCancel := context.WithTimeout(ctx, captureTimeout)
	s.capture, err = d.cast.Start(capCtx)
	capCancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, desktop.ErrCaptureDenied) {
			return fmt.Errorf("%s (%w)", captureNeedsGrant, err)
		}
		return err
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
	info := dlnet.SessionInfo{
		SessionID: sessionID, UDPPort: d.media.Port(), HostNonce: hostNonce,
		Codec: string(s.codec), Width: s.width, Height: s.height, FPS: s.fps, QP: s.quality.QP(),
		Display: display,
	}
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
		msg, err := conn.Receive(ctx)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
				return cause
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.handleMessage(ctx, msg)
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
	if err := bus.UnlockSession(ctx); err != nil {
		slog.Warn("unlock session", "err", err)
	}
	release, err := bus.InhibitIdle(ctx, inhibitApp, inhibitReason)
	if err != nil {
		slog.Warn("inhibit idle", "err", err)
		return
	}
	s.uninhibit = release
}

// matchResolution switches the primary monitor to the client size if it has
// such a mode, saving the original layout first so it can be restored, and
// describes the outcome for the HUD.
func (s *session) matchResolution(ctx context.Context, width, height int) string {
	d := s.d
	d.displayMu.Lock()
	defer d.displayMu.Unlock()
	st, err := d.bus.DisplayState(ctx)
	if err != nil {
		slog.Warn("read monitor state", "err", err)
		return "monitor state unavailable; scaled to fit"
	}
	plan, err := desktop.PlanResolution(st, width, height, s.fps)
	if errors.Is(err, desktop.ErrNoMatchingMode) {
		return fmt.Sprintf("desktop has no %dx%d mode; scaled with borders", width, height)
	}
	if err != nil {
		slog.Warn("plan resolution", "err", err)
		return "scaled to fit"
	}
	if !plan.Changed {
		return fmt.Sprintf("%s already at %dx%d@%.0fHz", plan.Connector, width, height, plan.Mode.Refresh)
	}
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
	}
	if err := d.bus.ApplyLayout(ctx, st.Serial, plan.Layout); err != nil {
		slog.Warn("switch resolution", "err", err)
		return "resolution switch failed; scaled to fit"
	}
	s.displayChanged = true
	return fmt.Sprintf("switched %s to %dx%d@%.0fHz", plan.Connector, width, height, plan.Mode.Refresh)
}

func (s *session) startPipeline(ctx context.Context) error {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	s.stopPipelineLocked()
	f, err := s.capture.OpenPipeWireRemote(ctx)
	if err != nil {
		return err
	}
	s.qMu.Lock()
	qp := s.quality.QP()
	s.qMu.Unlock()
	enc, err := video.Start(video.Settings{
		Codec: s.codec, Width: s.width, Height: s.height, QP: qp,
		PipeWireFD: int(f.Fd()), NodeID: s.capture.NodeID,
	}, s.onFrame)
	if err != nil {
		return errors.Join(err, f.Close())
	}
	s.enc, s.pwFile = enc, f
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

func (s *session) stopPipelineLocked() {
	if s.enc != nil {
		s.enc.Stop()
		s.enc = nil
	}
	if s.pwFile != nil {
		if err := s.pwFile.Close(); err != nil {
			slog.Debug("close PipeWire fd", "err", err)
		}
		s.pwFile = nil
	}
}

func (s *session) onFrame(f video.Frame) {
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

// HandleInput implements net.MediaHandler.
func (s *session) HandleInput(events []dlnet.InputEvent) {
	for _, e := range events {
		if e.Kind == dlnet.InputKey && s.copyDetector.Observe(e.Code, e.Flags == 1) {
			time.AfterFunc(copyReadDelay, func() { s.sendClipboard(s.ctx, false) })
		}
		ev := input.Event{Kind: uint8(e.Kind), Flags: e.Flags, Code: e.Code, X: e.X, Y: e.Y}
		if err := s.d.injector.Handle(ev); err != nil {
			slog.Debug("inject input", "err", err)
		}
		if e.Kind == dlnet.InputKey {
			slog.Log(s.ctx, dlnet.LevelTrace, "key", "code", input.KeyName(e.Code), "down", e.Flags == 1)
		}
	}
}

// HandleTelemetry implements net.MediaHandler.
func (s *session) HandleTelemetry(t dlnet.Telemetry) {
	if t.NeedIDR {
		s.requestKeyframe()
	}
	rtt := time.Duration(s.rttNanos.Load())
	s.qMu.Lock()
	qp, changed := s.quality.Observe(time.Now(), float64(t.LossPctX100)/100, rtt)
	s.qMu.Unlock()
	if changed {
		slog.Debug("adapting quantizer", "qp", qp, "loss_pct", float64(t.LossPctX100)/100, "rtt", rtt)
		s.setQP(qp)
	}
}

// HandleRTT implements net.MediaHandler.
func (s *session) HandleRTT(rtt time.Duration) { s.rttNanos.Store(int64(rtt)) }

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
	s.width, s.height = w, h
	display := s.matchResolution(ctx, w, h)
	if err := s.startPipeline(ctx); err != nil {
		s.cancel(fmt.Errorf("restart pipeline at %dx%d: %w", w, h, err))
		return
	}
	s.qMu.Lock()
	qp := s.quality.QP()
	s.qMu.Unlock()
	info := dlnet.SessionInfo{
		SessionID: s.media.ID(), UDPPort: s.d.media.Port(),
		Codec: string(s.codec), Width: w, Height: h, FPS: s.fps, QP: qp, Display: display,
	}
	if err := s.conn.Send(ctx, dlnet.MsgSession, info); err != nil {
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
	s.encMu.Lock()
	s.stopPipelineLocked()
	s.encMu.Unlock()
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
	if s.displayChanged {
		s.d.displayMu.Lock()
		if raw := s.d.state.DisplaySnapshot(); raw != nil {
			if err := s.d.restoreDisplayLocked(ctx, raw); err != nil {
				slog.Warn("restore monitor layout", "err", err)
			}
		}
		s.d.displayMu.Unlock()
	}
	if s.uninhibit != nil {
		if err := s.uninhibit(); err != nil {
			slog.Debug("release idle inhibitor", "err", err)
		}
	}
}

var _ dlnet.MediaHandler = (*session)(nil)
