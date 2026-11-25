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
	// unlockTimeout bounds waking the display, unlocking the desk, and
	// taking the idle inhibitor, as a session starts.
	unlockTimeout  = 5 * time.Second
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
	// mediaTimeout ends a session when no authenticated datagram has come
	// from the client for this long. Clients send telemetry and held input
	// every 250 ms and keepalives every 2 s, so a working path never goes
	// this quiet; a lost one would otherwise keep the desk unlocked.
	mediaTimeout = 10 * time.Second
	// mediaCheckInterval is how often the session checks for that.
	mediaCheckInterval = time.Second
	// setupStallLimit is how long a session's setup may run before the
	// watchdog restarts desklinkd (stall.go). Every step that waits on
	// another process has a deadline of its own, and a slow but working
	// setup ends far sooner; one still running then waits on something
	// nothing can cancel, such as a GStreamer state change.
	setupStallLimit = 2 * time.Minute
	// audioRouteTimeout bounds moving the desktop's sound to the DeskLink
	// output.
	audioRouteTimeout = 10 * time.Second
)

// errMediaLost ends a session whose UDP path stopped working while the
// WebSocket stayed up.
var errMediaLost = fmt.Errorf("the media connection was lost: nothing arrived from the client over UDP for %v", mediaTimeout)

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
	enc   videoPipeline

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
	audio      pipeline
	audioRoute *desktop.AudioRoute
	display    string
	// scale is the virtual screen's scale; captureW, captureH and captureFPS
	// its mode, zero when mirroring a monitor. deskMonitors are the
	// connectors present before the cast, to tell the virtual monitor apart.
	scale        float64
	appliedScale float64
	// ownPrimary is set while the virtual screen is GNOME's primary monitor
	// as the session last arranged it: the session found the desk locked, or
	// the client asked for it (setPrimary).
	ownPrimary bool
	// keepAwake is how long the computer stays awake after this session, as
	// the client set it (dlnet.KeepAwake values).
	keepAwake                      int
	captureW, captureH, captureFPS int
	deskMonitors                   []string
	// deskState is the monitor state before the cast, which the virtual
	// screen's layout keeps and the end of the session returns to.
	deskState desktop.DisplayState
	// deskArranged is the layout the session set when it placed the virtual
	// screen, nil before. The end of the session restores deskState only if
	// the desk's monitors are still laid out like this.
	deskArranged *desktop.DisplayLayout
	// turnedDeskOff is set once the session asked Mutter to turn the desk's
	// monitors off, before the reply, which may be lost after the change was
	// made. The end of the session then makes sure they are on again.
	turnedDeskOff bool

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

// readControl reads the client's control messages from conn until the
// connection fails or ctx ends, then calls lost. coder/websocket answers the
// client's pings only inside Read, so the reader runs from the hello on:
// waiting for a replaced session's cleanup and starting the cast can each
// take longer than the client waits for a pong. It reads with ctx, the
// connection's context, not live: cancelling a pending read closes the
// connection, and ServeSession may still have to send an error over it.
// Messages wait in the returned queue until the session reads them, and are
// dropped once live ends.
func readControl(ctx, live context.Context, lost context.CancelFunc, conn *dlnet.ControlConn) <-chan dlnet.Message {
	msgs := make(chan dlnet.Message, controlQueue)
	go func() {
		for {
			m, err := conn.Receive(ctx)
			if err != nil {
				lost()
				if ctx.Err() == nil && !dlnet.IsNormalClose(err) {
					slog.Warn("control connection failed", "peer", conn.Peer.Name, "err", err)
				}
				return
			}
			select {
			case msgs <- m:
			case <-live.Done():
				return
			}
		}
	}()
	return msgs
}

// runSession runs the session until the client leaves (parent ends), it is
// replaced or it fails. msgs are the client's control messages (readControl).
func (d *Daemon) runSession(parent context.Context, conn *dlnet.ControlConn, active *activeSession, hello dlnet.Hello, msgs <-chan dlnet.Message) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
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
	// The session notices that it has ended only in its control loop (serve),
	// so the watchdog tracks the setup until then: a step that hangs there
	// would otherwise keep the desk unlocked, and the next connection
	// waiting, for good.
	endSetup := d.stalls.beginWithin("starting the session", setupStallLimit)
	defer endSetup()

	s.prepareDesktop(ctx)
	// A running session keeps the computer awake; a hold from the last one ends.
	d.awake.stop()
	if validKeepAwake(hello.KeepAwake) {
		s.keepAwake = hello.KeepAwake
	}
	s.scale = helloScale(hello.Scale)
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
	go s.watchMedia(ctx, time.Now())
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
	endSetup()
	return s.serve(ctx, msgs)
}

// serve handles the client's control messages and the quality controller's
// level changes until the session ends. select picks among ready cases at
// random, so each case checks first: once the session has ended, a message
// still queued, such as a launch, is dropped.
//
// The session notices its end only between steps, so a step that hangs would
// keep the desk unlocked and the cast running for good. The watchdog tracks
// each step and restarts desklinkd if one runs past stallLimit (stall.go).
func (s *session) serve(ctx context.Context, msgs <-chan dlnet.Message) error {
	for {
		select {
		case msg := <-msgs:
			if ctx.Err() != nil {
				return endCause(ctx)
			}
			end := s.d.stalls.begin("handling the client's " + msg.Type + " message")
			s.handleMessage(ctx, msg)
			end()
		case level := <-s.levelChanges:
			if ctx.Err() != nil {
				return endCause(ctx)
			}
			end := s.d.stalls.begin("changing the stream's quality level")
			s.applyLevel(ctx, level)
			end()
		case <-ctx.Done():
			return endCause(ctx)
		}
	}
}

// endCause is why the session's context ended, or nil when it was simply
// cancelled: the client left or was replaced.
func endCause(ctx context.Context) error {
	if cause := context.Cause(ctx); !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}

// helloScale is the own screen's scale a hello asks for, or defaultScale
// when it asks for none or for one outside minScale..maxScale.
func helloScale(v float64) float64 {
	if !(v >= minScale && v <= maxScale) {
		if v != 0 {
			slog.Debug("ignoring the requested scale", "scale", v, "min", minScale, "max", maxScale)
		}
		return defaultScale
	}
	return v
}

// watchMedia ends the session once mediaLost says the client's datagrams
// stopped; cleanup then releases held input and locks the desk again.
// described is when the host sent the session description, which the client
// needs before it can send anything.
func (s *session) watchMedia(ctx context.Context, described time.Time) {
	t := time.NewTicker(mediaCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if mediaLost(now, described, s.media.LastReceive()) {
				s.cancel(errMediaLost)
				return
			}
		}
	}
}

// mediaLost reports whether mediaTimeout has passed since the client's
// newest authenticated datagram or, when none has come since the session
// description was sent, since then.
func mediaLost(now, described, lastReceive time.Time) bool {
	since := described
	if lastReceive.After(since) {
		since = lastReceive
	}
	return now.Sub(since) >= mediaTimeout
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
//
// Unlocking and the inhibitor run to the end, within unlockTimeout, even
// when the client leaves meanwhile: a reply lost to cancellation would leave
// the desk unlocked without the duty to lock it again, or an inhibitor that
// nobody releases. UnlockSession reports a locked desk as locked even when
// the unlock call failed, since it may have worked all the same.
func (s *session) prepareDesktop(ctx context.Context) {
	bus := s.d.bus
	wakeCtx, cancel := context.WithTimeout(ctx, unlockTimeout)
	defer cancel()
	if err := bus.WakeDisplay(wakeCtx); err != nil {
		slog.Warn("wake display", "err", err)
	}
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
	defer cancel()
	locked, err := bus.UnlockSession(unlockCtx)
	if err != nil {
		slog.Warn("unlock session", "err", err, "relock_after", locked)
	}
	// A session this one replaced may have unlocked the desk already.
	s.wasLocked = s.d.ctl.takeRelock() || locked
	if s.wasLocked {
		s.d.noteRelockOwed(unlockCtx)
	}
	inhibitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
	defer cancel()
	release, err := bus.InhibitIdle(inhibitCtx, inhibitApp, inhibitReason)
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
	st, err := s.d.display.DisplayState(ctx)
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
// undoes Mutter's layout of the unknown monitor set (placeVirtualScreen:
// the desk's monitors back where they were, or off when the desk was locked)
// and maps input onto the virtual screen's place in the new layout.
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

// placeVirtualScreen gives the virtual screen the session's scale, or the
// supported scale nearest it, and records the scale applied. When the session
// was locked as the client came, nobody uses the desk: the virtual screen
// becomes the only monitor and the desk's monitors go off, so GNOME moves
// every window onto the screen the client sees. Otherwise, or if Mutter
// refuses that, the desk keeps its layout from before the session with the
// virtual screen to its right, primary (top bar, overview) only when the
// session was locked.
func (s *session) placeVirtualScreen(ctx context.Context) error {
	s.d.displayMu.Lock()
	defer s.d.displayMu.Unlock()
	if s.wasLocked {
		err := s.arrangeOwnScreen(ctx, true)
		if err == nil || ctx.Err() != nil {
			return err
		}
		slog.Warn("turn the desk's monitors off for the session; keeping them on", "err", err)
	}
	return s.arrangeOwnScreen(ctx, false)
}

// arrangeOwnScreen applies the layout placeVirtualScreen describes, with the
// desk's monitors off when deskOff. The caller holds displayMu.
func (s *session) arrangeOwnScreen(ctx context.Context, deskOff bool) error {
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		return fmt.Errorf("read monitor state: %w", err)
	}
	layout, err := desktop.PlanVirtual(s.deskState, st, s.captured, s.scale, s.wasLocked || s.ownPrimary)
	if err == nil && deskOff {
		layout, err = layout.Only(s.captured)
	}
	if err != nil {
		return err
	}
	s.turnedDeskOff = s.turnedDeskOff || deskOff
	if err := s.applyLayout(ctx, st.Serial, layout); err != nil {
		return err
	}
	s.deskArranged = &layout
	s.noteOwnScreen(layout)
	return nil
}

// rescaleVirtualScreen gives the virtual screen the supported scale nearest
// the session's, leaving the desk's monitors as they are now, off when the
// session turned them off: someone at the desk may have changed them since
// the session placed the screen. It reports whether the scale changed; a
// request that rounds to the current scale changes nothing.
func (s *session) rescaleVirtualScreen(ctx context.Context) (bool, error) {
	s.d.displayMu.Lock()
	defer s.d.displayMu.Unlock()
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		return false, fmt.Errorf("read monitor state: %w", err)
	}
	layout, changed, err := desktop.RescaleVirtual(st, s.captured, s.scale)
	if err != nil {
		return false, err
	}
	if changed {
		if err := s.applyLayout(ctx, st.Serial, layout); err != nil {
			return false, err
		}
	}
	s.noteOwnScreen(layout)
	return changed, nil
}

// applyLayout applies a layout planned from the state with this serial. It
// runs to the end even when the session is ending meanwhile.
func (s *session) applyLayout(ctx context.Context, serial uint32, layout desktop.DisplayLayout) error {
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applyTimeout)
	defer cancel()
	if err := s.d.display.ApplyLayout(applyCtx, serial, layout); err != nil {
		return fmt.Errorf("apply the layout: %w", err)
	}
	return nil
}

// noteOwnScreen records the virtual screen's scale and whether it is primary
// from a layout that places it last.
func (s *session) noteOwnScreen(layout desktop.DisplayLayout) {
	own := layout.Logical[len(layout.Logical)-1]
	s.appliedScale, s.ownPrimary = own.Scale, own.Primary
	s.display = fmt.Sprintf("own %dx%d screen at %.0f%%", s.captureW, s.captureH, s.appliedScale*100)
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
		changed, err := s.rescaleVirtualScreen(ctx)
		if err != nil {
			s.reportError(ctx, fmt.Errorf("change the scale: %w", err))
			return
		}
		if changed {
			s.updatePointerMap(ctx)
			// Mutter's cast of the virtual screen stops delivering frames
			// once its scale changes; a new pipeline connects to it again.
			s.stopPipeline()
			if err := s.startPipeline(ctx); err != nil {
				s.cancel(fmt.Errorf("restart the pipeline after a scale change: %w", err))
				return
			}
		}
	}
	s.sendSessionInfo(ctx)
}

// setPrimary makes the client's own screen GNOME's primary monitor, with the
// top bar and the overview, or gives the primary back to the desk monitor
// that had it before the session. Only the primary moves: the
// desk's monitors stay on with their windows, and the end of the session
// gives them the primary back with the rest of their layout (restoreDesk).
func (s *session) setPrimary(ctx context.Context, primary bool) {
	if s.capture == nil || !s.capture.Virtual || s.captured == "" {
		s.reportError(ctx, errors.New("only the client's own screen can become the primary monitor"))
		return
	}
	if err := s.movePrimary(ctx, primary); err != nil {
		s.reportError(ctx, fmt.Errorf("move the primary monitor: %w", err))
		return
	}
	s.sendSessionInfo(ctx)
}

// movePrimary applies setPrimary's layout. The move is the session's own
// change, so if the desk was still laid out as the session arranged it, the
// arrangement now includes the move, and the end of the session restores the
// desk's layout from before the session as it would have. If someone at the
// desk changed it, the session leaves the desk to GNOME as before.
func (s *session) movePrimary(ctx context.Context, primary bool) error {
	s.d.displayMu.Lock()
	defer s.d.displayMu.Unlock()
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		return fmt.Errorf("read monitor state: %w", err)
	}
	layout, changed, err := desktop.MovePrimary(st, s.captured, primary, s.deskState.PrimaryConnectors())
	if err != nil {
		return err
	}
	if changed {
		untouched := s.deskArranged != nil && desktop.DeskUnchanged(*s.deskArranged, st, s.captured)
		if err := s.applyLayout(ctx, st.Serial, layout); err != nil {
			return err
		}
		if untouched {
			s.deskArranged = &layout
		}
	}
	s.noteOwnScreen(layout)
	return nil
}

// waitVirtualMonitor polls the layout for a monitor that was not there
// before the cast and has a place in it.
func (s *session) waitVirtualMonitor(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, virtualMonitorWait)
	defer cancel()
	t := time.NewTicker(virtualMonitorPoll)
	defer t.Stop()
	for {
		if st, err := s.d.display.DisplayState(ctx); err == nil {
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

// deskUntouched reports, while the virtual screen still exists, whether the
// desk's monitors are laid out as the session arranged them, so that
// restoring the layout from before the session undoes only the session's own
// change. A session that never arranged them restores as before.
func (s *session) deskUntouched(ctx context.Context) bool {
	if s.deskArranged == nil {
		return true
	}
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		slog.Debug("read monitor layout before the virtual screen goes", "err", err)
		return false
	}
	return desktop.DeskUnchanged(*s.deskArranged, st, s.captured)
}

// pipelineStopWait is how long cleanup waits for a GStreamer pipeline to
// stop before it goes on without it; a variable so tests can shorten it.
var pipelineStopWait = 5 * time.Second

// pipeline is a running GStreamer pipeline: *video.AudioEncoder, or a fake
// in tests.
type pipeline interface{ Stop() }

// videoPipeline is the running video encoder: *video.Encoder, or a fake in
// tests.
type videoPipeline interface {
	pipeline
	ForceKeyframe() bool
	SetQP(qp int)
	SetMaxFPS(fps int)
}

// giveDeskBack turns the desk's monitors on again while the virtual screen
// still exists, if the session turned them off and nobody changed that, so
// Mutter is never left without a monitor. It reports whether the desk was
// left as the session arranged it, so that closeOwnScreen restores the
// layout from before the session.
func (s *session) giveDeskBack(ctx context.Context) (restore bool) {
	restore = s.deskUntouched(ctx)
	if restore && s.turnedDeskOff {
		s.turnDeskOn(ctx)
	}
	return restore
}

// closeOwnScreen stops the cast of the client's own screen, after
// giveDeskBack. Once the virtual screen has gone, it restores the layout
// from before the session if restore says the desk was left as the session
// arranged it, and otherwise leaves the layout to GNOME, unless the desk's
// monitors are still off.
func (s *session) closeOwnScreen(ctx context.Context, restore bool) {
	if err := s.d.stopCast(ctx, s.capture); err != nil {
		slog.Debug("close screencast", "err", err)
	}
	s.waitVirtualGone(ctx)
	switch {
	case restore:
		s.restoreDesk(ctx)
	case s.turnedDeskOff && s.deskStillOff(ctx):
		slog.Warn("the desk's monitors are still off after the session; turning them on")
		s.restoreDesk(ctx)
	default:
		slog.Info("the desk's monitors changed during the session; leaving their layout to GNOME")
	}
}

// turnDeskOn turns the desk's monitors on while the virtual screen still
// exists: the layout from before the session, its primary included, with the
// virtual screen to its right. If this fails, Mutter turns them on itself
// once the virtual screen has gone.
//
// A cast that Mutter has already closed is left to Mutter. Mutter frees the
// virtual monitor as soon as the cast closes but updates its monitor list
// only later, when idle; until then it still reports the virtual monitor,
// and applying a configuration crashes gnome-shell (Mutter 51,
// meta_monitor_get_gamma_lut_size on the freed monitor).
func (s *session) turnDeskOn(ctx context.Context) {
	s.d.displayMu.Lock()
	defer s.d.displayMu.Unlock()
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		slog.Warn("read monitor state to turn the desk's monitors on", "err", err)
		return
	}
	layout, err := desktop.PlanVirtual(s.deskState, st, s.captured, s.scale, false)
	if err == nil && !s.d.castOpen(ctx, s.capture) {
		slog.Info("the screen cast closed before the session ended; Mutter turns the desk's monitors on")
		return
	}
	if err == nil {
		err = s.d.display.RestoreLayout(ctx, layout)
	}
	if err != nil {
		slog.Warn("turn the desk's monitors on before the virtual screen goes", "err", err)
	}
}

// deskStillOff reports whether only the virtual screen is on, or the layout
// cannot be read.
func (s *session) deskStillOff(ctx context.Context) bool {
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		slog.Debug("read monitor layout after the virtual screen went", "err", err)
		return true
	}
	return st.DeskOff(s.captured)
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
	if err := s.d.display.RestoreLayout(ctx, layout); err != nil {
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
		st, err := s.d.display.DisplayState(ctx)
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
	if st, err := s.d.display.DisplayState(ctx); err != nil {
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
	st, err := s.d.display.DisplayState(ctx)
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
	// GStreamer changes the pipeline's state synchronously, where cancelling
	// the session cannot reach, so the watchdog tracks the start.
	end := s.d.stalls.begin("starting the video pipeline")
	enc, err := video.Start(video.Settings{
		Codec: s.codec, Width: s.width, Height: s.height, QP: qp, FPS: s.fps,
		NodeID:       s.capture.NodeID,
		CaptureWidth: s.captureW, CaptureHeight: s.captureH, CaptureFPS: s.captureFPS,
	}, s.onFrame)
	end()
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
		s.d.stalls.finishWithin("the video pipeline's teardown", pipelineStopWait, enc.Stop)
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
	if st, err := s.d.display.DisplayState(ctx); err != nil {
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

// HandleInput implements net.MediaHandler. Input that arrives once the
// session has ended is dropped; cleanup closes the media session, which
// waits for a call under way, before it lifts every held key.
func (s *session) HandleInput(seq uint32, events []dlnet.InputEvent) {
	if s.ctx.Err() != nil {
		return
	}
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
	case dlnet.MsgPrimary:
		var p dlnet.PrimaryChange
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			s.reportError(ctx, fmt.Errorf("decode primary: %w", err))
			return
		}
		s.setPrimary(ctx, p.Primary)
	case dlnet.MsgKeepAwake:
		var k dlnet.KeepAwakeChange
		if err := json.Unmarshal(msg.Payload, &k); err != nil || !validKeepAwake(k.Minutes) {
			s.reportError(ctx, fmt.Errorf("keep awake: %d minutes is not allowed", k.Minutes))
			return
		}
		s.keepAwake = k.Minutes
		s.sendSessionInfo(ctx)
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
	st, err := s.d.display.DisplayState(ctx)
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
		Audio: s.audio != nil, Screen: s.screenMode(), Scale: s.ownScale(), KeepAwake: s.keepAwake,
		Primary: s.ownPrimary,
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
	routeCtx, cancel := context.WithTimeout(ctx, audioRouteTimeout)
	route, err := desktop.RedirectAudio(routeCtx)
	cancel()
	if err != nil {
		slog.Warn("move the sound to the DeskLink output; streaming the default output instead", "err", err)
	} else {
		s.audioRoute = &route
		device = route.Monitor()
		if err := s.d.state.SetAudioDefaultSink(route.PreviousSink); err != nil {
			slog.Debug("save the output to restore", "err", err)
		}
	}
	end := s.d.stalls.begin("starting the audio pipeline")
	enc, err := video.StartAudio(device, func(f video.Frame) {
		if err := s.media.SendAudio(f.Data, f.Captured); err != nil {
			slog.Log(s.ctx, dlnet.LevelTrace, "send audio", "err", err)
		}
	})
	end()
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
		s.d.stalls.finishWithin("the sound pipeline's teardown", pipelineStopWait, s.audio.Stop)
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

// cleanup undoes everything the session changed. The client loses control
// first: the session's context ends, so its input and control messages are
// dropped; its media session closes, waiting for input being injected, and
// sends nothing more; then every held key, button and pen contact is lifted.
// The relock comes before the display work, so a desk whose monitors the
// session turned off comes back showing the lock screen, never the unlocked
// desktop. Cleanup uses its own timeout.
func (s *session) cleanup() {
	// The watchdog restarts desklinkd if this never ends (stall.go).
	defer s.d.stalls.begin("the session's cleanup")()
	s.cancel(nil)
	if s.media != nil {
		s.media.Close()
	}
	if err := s.d.injector.ReleaseAll(); err != nil {
		slog.Warn("release held keys", "err", err)
	}
	// Each step gets its own time, so a slow one cannot leave the next
	// without any.
	step := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), restoreTimeout)
	}
	// The desk gets its lock and its monitors back first, while the pipeline
	// still runs: a GStreamer or PipeWire teardown can hang, and a desk left
	// dark with every window on the client's screen looks frozen. A session
	// that took over keeps the desk unlocked and owes the relock; it waits
	// for this cleanup to finish before it touches the desk.
	ctx, cancel := step()
	s.d.ctl.leave(ctx, s.active, s.wasLocked)
	restore := false
	if s.capture != nil && s.capture.Virtual {
		restore = s.giveDeskBack(ctx)
	}
	cancel()
	s.stopPipeline()
	ctx, cancel = step()
	s.stopAudio(ctx)
	cancel()
	ctx, cancel = step()
	defer cancel()
	if s.capture != nil {
		if s.capture.Virtual {
			s.closeOwnScreen(ctx, restore)
		} else if err := s.d.stopCast(ctx, s.capture); err != nil {
			slog.Debug("close screencast", "err", err)
		}
	}
	if s.media != nil {
		st := s.media.Stats()
		slog.Info("media totals", "frames", st.FramesSent, "packets", st.PacketsSent, "bytes", st.BytesSent)
	}
	if s.uninhibit != nil {
		if err := s.uninhibit(); err != nil {
			slog.Debug("release idle inhibitor", "err", err)
		}
	}
	// The last session keeps the computer awake for a job left running, if
	// the client asked; a session that took over does that when it ends, and
	// a device unpaired meanwhile gets nothing it asked for.
	if s.d.ctl.endsInControl(s.active) {
		s.d.awake.hold(ctx, s.keepAwake, s.active.peerID, func() bool { return s.d.ctl.stillPaired(s.active) })
	}
}

var _ dlnet.MediaHandler = (*session)(nil)
