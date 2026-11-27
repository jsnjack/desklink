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
	minStreamDim   = dlnet.MinScreenSide
	maxStreamDim   = dlnet.MaxScreenSide
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
	// fps is the current level's frame rate; the encoder runs at streamFPS,
	// which the client's cap (fpsCap, MsgMaxFPS, 0 for none) may lower.
	fps    int
	fpsCap int

	media   *dlnet.MediaSession
	capture *desktop.Capture
	apps    []desktop.App
	// typer types the client's text, nil when the cast has no remote desktop
	// session; texts holds that text until it is typed, and the client's
	// keys behind it (typing.go). replacing is open while a resize or mirror
	// switch replaces the capture, typing is set while a text is typed, and
	// typingDone, when set, closes once it is. typerMu guards all but texts.
	typer textTyper
	// scroller scrolls InputScrollPx records as a touchpad does: the same
	// cast as typer, nil without a remote desktop session. typerMu guards it.
	scroller   pixelScroller
	replacing  chan struct{}
	typing     bool
	typingDone chan struct{}
	typerMu    sync.Mutex
	texts      *textQueue

	// inputQueue holds the client's input that waits for text it sent
	// before, and all input after it, which keeps its order (HandleInput);
	// inputBusy is set while a worker drains it. inputMu guards both, and
	// inputWorkers counts the worker, which cleanup waits for.
	inputMu      sync.Mutex
	inputQueue   []queuedInput
	inputBusy    bool
	inputWorkers sync.WaitGroup
	// textGivenUp is the newest text mark whose wait timed out: input behind
	// the same text does not wait again. Only the input worker uses it.
	textGivenUp uint64

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
	// unlocked is set when this session's own unlock opened the desk, which
	// the session-start notice says.
	unlocked bool

	// clientImages is the largest clipboard image the client takes, as its
	// hello said. clipMu keeps one clipboard read and send at a time, so the
	// parts of two images never interleave. images joins the parts of the
	// client's clipboard images; only the control loop uses it.
	clientImages int
	clipMu       sync.Mutex
	images       dlnet.ImageAssembler

	// tiles sends exact tiles of settled parts of the screen; nil when the
	// client did not ask for them (tiles.go).
	tiles *sessionTiles

	// audio streams the desktop's sound; audioRoute is the virtual output
	// it plays into while the session runs. Both are nil without audio.
	audio      pipeline
	audioRoute *desktop.AudioRoute
	// mic plays the client's microphone into a virtual microphone on the
	// desk while micOn is set; one whose removal failed stays, off, for the
	// next try. micMu guards both, because the UDP goroutine plays frames.
	micMu   sync.Mutex
	mic     microphone
	micOn   bool
	display string
	// scale is the virtual screen's scale; captureW, captureH and captureFPS
	// its mode, zero when mirroring a monitor. deskMonitors are the
	// connectors present before the cast, to tell the virtual monitor apart.
	scale        float64
	appliedScale float64
	// ownPrimary is set while the virtual screen is GNOME's primary monitor
	// as the session last arranged it. primaryChoice is the client's last
	// choice (setPrimary), nil before one: placing the screen again keeps it
	// (primaryWanted).
	ownPrimary    bool
	primaryChoice *bool
	// ownAlone is set while the virtual screen is the only monitor on (the
	// desk's monitors are off), so no other one can take the primary.
	ownAlone bool
	// deskOffByClient is set while the desk's monitors are off because the
	// client asked for it (setDesk), which lets it turn them on again.
	deskOffByClient bool
	// keepAwake is how long the computer stays awake after this session, as
	// the client set it (dlnet.KeepAwake values); lockAfter locks the desk
	// when the session ends, also when it was unlocked as the client came.
	keepAwake int
	lockAfter bool
	// worstLevel is the lowest quality level (highest index) the session
	// went down to, for the session history.
	worstLevel                     int
	captureW, captureH, captureFPS int
	deskMonitors                   []string
	// closedVirtual are the connectors of virtual screens the session closed
	// to resize its screen that Mutter still listed when the new one started,
	// so they are never taken for the new screen. One Mutter no longer lists
	// is forgotten: Mutter gives its connector to the next virtual monitor
	// (Meta-0 again), and taking that for an old screen left a rotated
	// screen unplaced and the session without its connector (2026-11-25).
	closedVirtual []string
	// monitors is SessionInfo.Monitors as last read, for when the layout
	// cannot be read.
	monitors []dlnet.MonitorInfo
	// pointerApart is set when the casts leave the pointer out of the frames
	// and the session sends it to the client (pointerapart.go).
	pointerApart bool
	// mirrorScale is how many pixels of the client's screen a logical pixel
	// of the mirrored monitor is, as last read with monitors; 0 before it is
	// known.
	mirrorScale float64
	// mouse is GNOME's mouse settings, read once for SessionInfo.Mouse.
	mouse     desktop.MouseSettings
	mouseOnce sync.Once
	// requestedQP is the quantizer the client asked for, the floor of the
	// quality controller; a resize starts a new controller from it.
	requestedQP int
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
	// levelChanges tells the session loop, which owns the pipeline, that the
	// controller chose a level on the UDP goroutine; it carries that level,
	// but the loop applies the controller's level as it is by then.
	levelChanges chan int

	pointerMu sync.Mutex
	pointer   pointerMap
	// screenW and screenH are the client's screen size, which relative
	// motion is measured in. A level change leaves them, so it does not
	// change the trackpad's speed; only a resize of the own screen changes
	// them. pointerMu guards them.
	screenW, screenH int
	// pixelScale is how many of the client's pixels one of GNOME's logical
	// pixels is, as last described (mouseInfo), for InputScrollPx records.
	// pointerMu guards it.
	pixelScale float64
	// cursorFrame places the cast's pixels in the frame for the pointer's
	// position and shape (pointerapart.go). pointerMu guards it.
	cursorFrame cursorFrame
	// lastPointerInput is when the client last moved the pointer, in Unix
	// nanoseconds, and deskPointer where the desk last reported it once the
	// client had left it alone (pointerIdle); the next input moves cursor
	// there first.
	lastPointerInput atomic.Int64
	deskPointer      atomic.Pointer[dlnet.CursorPosition]
	// wheelRemX and wheelRemY keep the fractions of wheel units that
	// InputScrollPx records left when the session scrolls with wheel steps,
	// and scrollRefused is set once Mutter refused a touchpad scroll, after
	// which it does. Only the goroutine injecting the client's input uses
	// them.
	wheelRemX, wheelRemY float64
	scrollRefused        bool
	// cursor is the client's pointer on its screen, and penTilt the tilt
	// the client last reported for its pen. Only the goroutine injecting the
	// client's input uses them, the UDP goroutine or the input worker, one at
	// a time (HandleInput).
	cursor  clientCursor
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
// dropped once live ends; text to type goes to texts instead, which keeps it
// in step with the client's keys (typing.go). A client that fills either
// queue is disconnected (lost): a reader waiting for room would answer no
// pings and miss the client leaving. Clients send control messages only when
// the user acts.
func readControl(ctx, live context.Context, lost context.CancelFunc, conn *dlnet.ControlConn, texts *textQueue) <-chan dlnet.Message {
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
			if m.Type == dlnet.MsgTypeText {
				if !texts.add(m) {
					lost()
					slog.Warn("closing a control connection that sent more text than its session typed",
						"peer", conn.Peer.Name, "queued", textQueueSize)
					return
				}
				continue
			}
			select {
			case msgs <- m:
			case <-live.Done():
				return
			default:
				lost()
				slog.Warn("closing a control connection that sent more messages than its session took",
					"peer", conn.Peer.Name, "queued", controlQueue)
				return
			}
		}
	}()
	return msgs
}

// runSession runs the session until the client leaves (parent ends), it is
// replaced or it fails. msgs and texts are the client's control messages and
// text to type (readControl). reconnect says the session replaced one of the
// same device, which keeps the desk as it was and so shows no notice.
func (d *Daemon) runSession(parent context.Context, conn *dlnet.ControlConn, active *activeSession, hello dlnet.Hello, msgs <-chan dlnet.Message, reconnect bool, texts *textQueue) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	cfg := d.opts.Config
	s := &session{d: d, conn: conn, active: active, ctx: ctx, cancel: cancel, cursor: newClientCursor(), texts: texts}
	s.clientImages = max(hello.ClipboardImages, 0)
	s.images.Limit = cfg.MaxClipboardImageBytes
	s.codec = chooseCodec(video.Codec(hello.Codec))
	s.width, s.height = clampDim(hello.Width, 2560), clampDim(hello.Height, 1600)
	s.screenW, s.screenH = s.width, s.height
	s.fps = hello.FPS
	if s.fps <= 0 || s.fps > maxFPS {
		s.fps = defaultFPS
	}
	s.requestedQP = hello.QP
	s.startQuality(hello, cfg.MinQP, cfg.MaxQP)
	s.levelChanges = make(chan int, 1)
	defer s.cleanup()
	// The session notices that it has ended only in its control loop (serve),
	// so the watchdog tracks the setup until then: a step that hangs there
	// would otherwise keep the desk unlocked, and the next connection
	// waiting, for good.
	endSetup := d.stalls.beginWithin("starting the session", setupStallLimit)
	defer endSetup()

	// prepareDesktop records the relock lock_after owes.
	s.lockAfter = hello.LockAfter
	s.prepareDesktop(ctx)
	// A running session keeps the computer awake; a hold from the last one ends.
	d.awake.stop()
	if validKeepAwake(hello.KeepAwake) {
		s.keepAwake = hello.KeepAwake
	}
	s.scale = helloScale(hello.Scale)
	s.pointerApart = hello.Pointer && d.pointerHook
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
		if err := s.startMonitorCapture(ctx, hello.Monitor); err != nil {
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
	s.startPointerApart(ctx)
	if hello.Tiles {
		var withFrames func(video.Frame, []byte) bool
		if hello.TilesUDP {
			withFrames = s.sendFrame
		}
		s.tiles = newSessionTiles(ctx, s.sendBinary, s.width, s.height, withFrames)
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
	// A failure is only logged: an error before the first datagram would
	// end the session on the client. The description reports it off.
	if hello.Mic {
		if err := s.startMic(ctx); err != nil {
			slog.Warn("no DeskLink microphone for the client", "err", err)
		}
	}
	info := s.sessionInfo(ctx)
	info.HostNonce = hostNonce
	if err := s.send(ctx, dlnet.MsgSession, info); err != nil {
		return err
	}
	if cfg.NotifySessionStart && !reconnect {
		d.notify(sessionNotice(conn.Peer.Name, s.unlocked))
	}
	go s.watchMedia(ctx, time.Now())
	s.apps = desktop.ScanApps(desktop.DefaultAppDirs(), cfg.PinnedApps)
	if err := s.send(ctx, dlnet.MsgApps, s.apps); err != nil {
		return err
	}

	go func() {
		if err := s.media.RunKeepalive(ctx); err != nil {
			cancel(err)
		}
	}()
	go s.watchClipboard(ctx)
	go s.typeTexts(ctx, texts)
	endSetup()
	return s.serve(ctx, msgs)
}

// startQuality sets up the session's quality controller for the client's
// maximum size and rate, the session's width, height and fps, and starts the
// stream at its level, which the session history counts as reached.
func (s *session) startQuality(hello dlnet.Hello, minQP, maxQP int) {
	s.quality = newQuality(hello, minQP, maxQP, s.width, s.height, s.fps)
	l := s.quality.Levels()[s.quality.Level()]
	s.width, s.height, s.fps = l.Width, l.Height, l.FPS
	s.worstLevel = s.quality.Level()
}

// newQuality sets up the session's quality controller for the client's
// maximum size and rate. Auto gets the whole ladder, turned to the screen's
// orientation, and starts at the level the client remembers, at most one
// below the top; fixed and manual quality keep the top level.
func newQuality(hello dlnet.Hello, minQP, maxQP, w, h, fps int) *video.QualityController {
	auto := hello.Auto && !hello.Fixed
	levels := ladder(w, h, fps)
	if !auto {
		levels = levels[:1]
	}
	start := 0
	if hello.Start != nil {
		want := video.Level{Width: hello.Start.Width, Height: hello.Start.Height, FPS: hello.Start.FPS}
		start = video.StartLevel(levels, turnedLike(want, w, h))
	}
	return video.NewQualityController(video.QualityConfig{
		Requested: hello.QP, MinQP: minQP, MaxQP: maxQP,
		Levels: levels, Start: start, Auto: auto, Fixed: hello.Fixed,
	})
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
		case <-s.levelChanges:
			if ctx.Err() != nil {
				return endCause(ctx)
			}
			// The controller's level now, not the one queued: a frame-rate
			// cap may have moved it since (setFPSCap), and applying the
			// current level again changes nothing.
			end := s.d.stalls.begin("changing the stream's quality level")
			s.applyLevel(ctx, s.controllerLevel())
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

// ladder is the quality ladder for a client screen of w x h at up to fps.
// A portrait screen gets the landscape ladder turned on its side, so the
// rungs correspond in both orientations and a rotation keeps the session on
// its rung.
func ladder(w, h, fps int) []video.Level {
	if w >= h {
		return video.Levels(w, h, fps)
	}
	levels := video.Levels(h, w, fps)
	for i := range levels {
		levels[i].Width, levels[i].Height = levels[i].Height, levels[i].Width
	}
	return levels
}

// turnedLike is level l turned to the orientation of a w x h screen. Clients
// remember the level per network, from sessions in either orientation, and
// a landscape level would match no rung of a portrait ladder but the last.
func turnedLike(l video.Level, w, h int) video.Level {
	if (l.Width >= l.Height) != (w >= h) {
		l.Width, l.Height = l.Height, l.Width
	}
	return l
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
	// A session this one replaced may have unlocked the desk already.
	owed := s.d.ctl.takeRelock()
	// The relock is recorded before the unlock, so a desklinkd that dies
	// right after it still locks the desk on its next start. A LockedHint
	// that cannot be read counts as locked, as in UnlockSession.
	if locked, err := bus.SessionLocked(unlockCtx); owed || err != nil || locked {
		s.d.relock.owe(unlockCtx)
	}
	locked, err := bus.UnlockSession(unlockCtx)
	if err != nil {
		slog.Warn("unlock session", "err", err, "relock_after", locked)
	}
	s.wasLocked = owed || locked
	s.unlocked = locked && err == nil
	if s.wasLocked || s.lockAfter {
		// Again in case the desk locked between the two reads; a relock
		// recorded already is left as it is. lock_after is recorded too, so
		// that a desklinkd that dies before the session ends locks the desk
		// on its next start, as the client asked.
		s.d.relock.owe(unlockCtx)
	} else {
		s.d.relock.forget()
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
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		return err
	}
	s.deskMonitors = st.Connectors()
	s.deskState = st
	// Until the virtual monitor shows up, the session records no monitor of
	// the desk: input and cleanup must not act on the configured one.
	s.captured = ""
	return s.startVirtualCast(ctx)
}

// startVirtualCast starts the cast of a virtual monitor the size of the top
// quality level; the pipeline's caps give Mutter that size.
func (s *session) startVirtualCast(ctx context.Context) error {
	s.qMu.Lock()
	top := s.quality.Levels()[0]
	s.qMu.Unlock()
	capCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()
	c, err := s.d.cast.StartVirtual(capCtx, s.pointerApart)
	if err != nil {
		return err
	}
	s.useCapture(c)
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
	if s.wasLocked || s.deskOffByClient {
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
	layout, err := desktop.PlanVirtual(s.deskState, st, s.captured, s.scale, s.primaryWanted())
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

// primaryWanted is whether the virtual screen is primary when the session
// places it: as the client last chose, and before any choice when the session
// found the desk locked.
func (s *session) primaryWanted() bool {
	if s.primaryChoice != nil {
		return *s.primaryChoice
	}
	return s.wasLocked
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
	s.appliedScale, s.ownPrimary, s.ownAlone = own.Scale, own.Primary, len(layout.Logical) == 1
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
			if !s.stopForRestart() {
				return
			}
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
	s.primaryChoice = &primary
	s.noteOwnScreen(layout)
	return nil
}

// setDesk turns the desk's monitors off, so GNOME moves every window onto
// the client's own screen, or on again, which puts the windows back on the
// monitors they came from. The end of the session turns them on as for a
// desk it found locked (giveDeskBack).
func (s *session) setDesk(ctx context.Context, off bool) {
	if err := s.switchDesk(ctx, off); err != nil {
		s.reportError(ctx, err)
		return
	}
	s.updatePointerMap(ctx)
	s.sendSessionInfo(ctx)
}

// switchDesk applies setDesk. Only a client that turned the desk's monitors
// off turns them on again: a session that found the desk locked keeps them
// dark while it runs, because the desk is unlocked and anyone at it would
// see the session's desktop.
func (s *session) switchDesk(ctx context.Context, off bool) error {
	switch {
	case s.capture == nil || !s.capture.Virtual || s.captured == "":
		return errors.New("only a session with its own screen can turn the desk's monitors off")
	case off == s.ownAlone:
		return nil
	case !off && !s.deskOffByClient:
		return errors.New("the desk was locked when the session started, so its monitors stay off until the session ends")
	}
	s.d.displayMu.Lock()
	defer s.d.displayMu.Unlock()
	if err := s.arrangeOwnScreen(ctx, off); err != nil {
		if off {
			return fmt.Errorf("turn the desk's monitors off: %w", err)
		}
		return fmt.Errorf("turn the desk's monitors on: %w", err)
	}
	s.deskOffByClient = off
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
					if !slices.Contains(s.deskMonitors, c) && !slices.Contains(s.closedVirtual, c) {
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
	SetScanning(on bool)
}

// videoEncoder is a started video pipeline that reports its failure and its
// end: *video.Encoder, or a fake in tests.
type videoEncoder interface {
	videoPipeline
	Errors() <-chan error
	Done() <-chan struct{}
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
// virtual screen to its right (ownConnector). If this fails, Mutter turns them on itself
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
	layout, err := desktop.PlanVirtual(s.deskState, st, s.ownConnector(st), s.scale, false)
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

// ownConnector is the connector of the session's virtual screen in st: the
// one the session found or, when it never found it (Mutter added the screen
// too late), the only monitor that is on and is neither the desk's nor a
// screen the session closed; empty when there is not exactly one.
func (s *session) ownConnector(st desktop.DisplayState) string {
	if s.captured != "" {
		return s.captured
	}
	own := ""
	for _, lm := range st.Logical {
		for _, c := range lm.Connectors {
			if slices.Contains(s.deskMonitors, c) || slices.Contains(s.closedVirtual, c) || c == own {
				continue
			}
			if own != "" {
				return ""
			}
			own = c
		}
	}
	return own
}

// waitVirtualGone waits until the stopped cast's virtual monitor has left
// Mutter's monitor list. Mutter frees the monitor with the cast but updates
// the list only when idle, and a configuration applied meanwhile crashes
// gnome-shell (Mutter 51; on 2026-11-25 in meta_logical_monitor_get_scale,
// restoring the desk at once for a session that had lost its screen's
// connector), so nothing touches the layout before this returns. A session
// that never found its connector waits until no monitor but the desk's is
// listed. It reports whether the monitor went in time, and forgets the
// closed screens Mutter no longer lists.
func (s *session) waitVirtualGone(ctx context.Context) bool {
	if slices.Contains(s.deskMonitors, s.captured) {
		return true
	}
	ours := func(c string) bool {
		if s.captured != "" {
			return c == s.captured
		}
		return !slices.Contains(s.deskMonitors, c)
	}
	ctx, cancel := context.WithTimeout(ctx, virtualMonitorWait)
	defer cancel()
	t := time.NewTicker(virtualMonitorPoll)
	defer t.Stop()
	for {
		st, err := s.d.display.DisplayState(ctx)
		if err != nil {
			return false
		}
		listed := st.Connectors()
		if !slices.ContainsFunc(listed, ours) {
			s.closedVirtual = slices.DeleteFunc(s.closedVirtual, func(c string) bool { return !slices.Contains(listed, c) })
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}

// startMonitorCapture mirrors a physical monitor without changing it: chosen,
// the client's last choice for this host, when it is on; otherwise the
// configured one, or else the one whose shape is closest to the client's.
// Anything at the desk stays as it is.
func (s *session) startMonitorCapture(ctx context.Context, chosen string) error {
	configured := s.d.opts.Config.Monitor
	if st, err := s.d.display.DisplayState(ctx); err != nil {
		slog.Debug("read monitor layout to pick the monitor to mirror", "err", err)
		s.captured = configured
	} else {
		if st.MonitorOn(chosen) {
			configured = chosen
		}
		s.captured = st.BestFit(configured, s.width, s.height)
	}
	capCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()
	c, err := s.d.cast.Start(capCtx, s.captured, s.pointerApart)
	if err != nil {
		return err
	}
	s.useCapture(c)
	s.learnCapturedMonitor(ctx)
	s.updatePointerMap(ctx)
	s.display = "mirroring " + s.captured
	return nil
}

// setMirror answers a mirror message: the session shows another monitor.
func (s *session) setMirror(ctx context.Context, connector string) {
	err := s.switchMirror(ctx, connector)
	switch {
	case ctx.Err() != nil:
		// The switch failed half way and ended the session; its cleanup
		// gives the desk back.
	case err != nil:
		s.reportError(ctx, fmt.Errorf("mirror %s: %w", connector, err))
	default:
		s.sendSessionInfo(ctx)
	}
}

// switchMirror makes a mirroring session record the monitor on connector,
// which must be on, instead of the one it records. The new cast starts
// before the old one stops, so a monitor Mutter cannot record leaves the
// session as it was; once the old pipeline has stopped, a failure ends the
// session.
func (s *session) switchMirror(ctx context.Context, connector string) error {
	if s.capture == nil || s.capture.Virtual {
		return errors.New("only a session that mirrors a monitor can switch to another")
	}
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		return fmt.Errorf("read monitor state: %w", err)
	}
	if !st.MonitorOn(connector) {
		return fmt.Errorf("no monitor %q is on", connector)
	}
	if connector == s.captured {
		return nil
	}
	capCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	c, err := s.d.cast.Start(capCtx, connector, s.pointerApart)
	cancel()
	if err != nil {
		return err
	}
	// Text being typed through the old cast finishes before it stops.
	s.holdTyping()
	defer s.releaseTyping()
	old := s.capture
	if !s.stopForRestart() {
		s.useCapture(c) // the session's cleanup stops this one
		s.stopReplacedCast(ctx, old)
		return errPipelineStuck
	}
	s.useCapture(c)
	s.captured = connector
	s.display = "mirroring " + connector
	s.stopReplacedCast(ctx, old)
	if err := ctx.Err(); err != nil {
		return err // the session ended meanwhile; its cleanup stops the new cast
	}
	s.updatePointerMap(ctx)
	if err := s.startPipeline(ctx); err != nil {
		err = fmt.Errorf("start the pipeline for %s: %w", connector, err)
		s.cancel(err)
		return err
	}
	return nil
}

// teardownContext is the context of one step that undoes a change the
// session made for a resize or a mirror switch. It runs to the end even when
// the session is ending meanwhile: godbus does not even send a call whose
// context has ended, and a cast the session forgets once it has replaced the
// capture would keep running until desklinkd restarts, with its virtual
// monitor in the layout and the desk's monitors off if the session had
// turned them off.
func teardownContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
}

// stopReplacedCast stops the cast a mirror switch replaced.
func (s *session) stopReplacedCast(ctx context.Context, old *desktop.Capture) {
	ctx, cancel := teardownContext(ctx)
	defer cancel()
	if err := s.d.stopCast(ctx, old); err != nil {
		slog.Debug("close the screencast of the monitor mirrored before", "err", err)
	}
}

// useCapture records the running cast, which types the client's text when it
// belongs to a remote desktop session; nil while a resize replaces it.
func (s *session) useCapture(c *desktop.Capture) {
	s.capture = c
	s.typerMu.Lock()
	defer s.typerMu.Unlock()
	s.typer, s.scroller = nil, nil
	if c != nil && c.CanType() {
		s.typer, s.scroller = c, c
	}
}

// currentScroller is the cast that scrolls like a touchpad, or nil.
func (s *session) currentScroller() pixelScroller {
	s.typerMu.Lock()
	defer s.typerMu.Unlock()
	return s.scroller
}

// currentTyper is the cast that types the client's text, or nil. A mirror
// switch or resize replaces it while the typist runs.
func (s *session) currentTyper() textTyper {
	s.typerMu.Lock()
	defer s.typerMu.Unlock()
	return s.typer
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
	settings := video.Settings{
		Codec: s.codec, Width: s.width, Height: s.height, QP: qp, FPS: s.streamFPS(),
		NodeID:       s.capture.NodeID,
		CaptureWidth: s.captureW, CaptureHeight: s.captureH, CaptureFPS: s.captureFPS,
	}
	if s.tiles != nil {
		// The client's tiles belong to the last pipeline's frames.
		s.tiles.restart(s.width, s.height)
		settings.Scan = s.tiles.scan
	}
	end := s.d.stalls.begin("starting the video pipeline")
	enc, err := s.d.startVideo(settings, s.onFrame)
	end()
	if err != nil {
		return err
	}
	if s.tiles != nil {
		enc.SetScanning(s.tiles.on.Load())
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

// errPipelineStuck ends a session whose video pipeline did not stop in time
// for a resize or a scale change.
var errPipelineStuck = fmt.Errorf("the video pipeline did not stop within %v, so no new one could start", pipelineStopWait)

// stopPipeline stops the running pipeline, if any, outside encMu, and
// reports whether it stopped. One that takes longer than pipelineStopWait
// goes on stopping in the background (finishWithin).
func (s *session) stopPipeline() bool {
	s.encMu.Lock()
	enc := s.enc
	s.enc = nil
	s.encMu.Unlock()
	if enc == nil {
		return true
	}
	return s.d.stalls.finishWithin("the video pipeline's teardown", pipelineStopWait, enc.Stop)
}

// stopForRestart stops the pipeline before a new one starts for a resize or
// a scale change. A pipeline still stopping in the background could go on
// sending frames beside the new one on the same capture, so instead the
// session ends (errPipelineStuck) and its cleanup gives the desk back.
func (s *session) stopForRestart() bool {
	if s.stopPipeline() {
		return true
	}
	s.cancel(errPipelineStuck)
	return false
}

func (s *session) onFrame(f video.Frame) {
	// The controller lowers a raised quantizer once the screen is still,
	// which it tells from the encoded frames' sizes.
	s.qMu.Lock()
	s.quality.NoteFrame(time.Now(), len(f.Data), f.Keyframe)
	s.qMu.Unlock()
	if f.Keyframe {
		slog.Log(s.ctx, dlnet.LevelTrace, "keyframe", "bytes", len(f.Data), "since_capture", time.Since(f.Captured))
	}
	// A frame whose invalidation goes over UDP waits for it (tiles.go).
	if s.tiles != nil && s.tiles.udp() {
		s.tiles.encoded(f)
		return
	}
	s.sendFrame(f, nil)
	if s.tiles != nil {
		s.tiles.encoded(f)
	}
}

// sendFrame sends one encoded frame and, when packet is not nil, the
// PacketTiles plaintext of its invalidation around it; it reports whether
// the frame went out.
func (s *session) sendFrame(f video.Frame, packet []byte) bool {
	sent, err := s.media.SendFrameWithTiles(f.Data, f.Keyframe, f.Captured, packet)
	if err != nil {
		if errors.Is(err, dlnet.ErrSequenceExhausted) {
			s.cancel(err)
			return false
		}
		slog.Debug("send frame", "err", err)
	}
	return sent
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
	// Until the layout is read, the cast fills the frame at the size it
	// was asked for (the own screen's mode).
	frame := cursorFrame{cw: 1, ch: 1, sw: float64(s.captureW), sh: float64(s.captureH)}
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
		k := st.StreamScale(s.captured)
		frame = cursorFrame{cx: m.cx, cy: m.cy, cw: m.cw, ch: m.ch, sw: float64(monitor.W) * k, sh: float64(monitor.H) * k}
	}
	s.pointerMu.Lock()
	s.pointer = m
	s.cursorFrame = frame
	s.pointerMu.Unlock()
}

// queuedInput is one input datagram waiting in the input worker's queue:
// its sequence number and events, and for one with keys how many texts the
// session had received when it arrived (textQueue.mark).
type queuedInput struct {
	seq      uint32
	events   []dlnet.InputEvent
	textMark uint64
}

// HandleInput implements net.MediaHandler. Input that arrives once the
// session has ended is dropped; cleanup closes the media session, which
// waits for a call under way, and waits for the input worker before it
// lifts every held key.
//
// It runs on the UDP goroutine, which every session's datagrams share, so it
// never waits: keys that arrive while text the client sent before them is
// still being typed go to a worker that injects them once the text is typed
// (typing.go), and so does all input after them until the worker has caught
// up, which keeps the client's order. Other input is injected at once.
func (s *session) HandleInput(seq uint32, events []dlnet.InputEvent) {
	if s.ctx.Err() != nil {
		return
	}
	in := queuedInput{seq: seq, events: events}
	waits := false
	if s.texts != nil && hasKey(events) {
		in.textMark, waits = s.texts.mark()
	}
	s.inputMu.Lock()
	if !s.inputBusy && !waits {
		s.inputMu.Unlock()
		s.injectInput(seq, events)
		return
	}
	s.inputQueue = append(s.inputQueue, in)
	if !s.inputBusy {
		s.inputBusy = true
		s.inputWorkers.Add(1)
		go s.drainInput()
	}
	s.inputMu.Unlock()
}

// hasKey reports whether events hold a key press or release.
func hasKey(events []dlnet.InputEvent) bool {
	return slices.ContainsFunc(events, func(e dlnet.InputEvent) bool { return e.Kind == dlnet.InputKey })
}

// drainInput injects the queued input in order, keys once the text received
// before them has been typed or keysBehindTextLimit has passed, until the
// queue is empty or the session has ended; HandleInput then injects at once
// again.
func (s *session) drainInput() {
	defer s.inputWorkers.Done()
	for {
		s.inputMu.Lock()
		if len(s.inputQueue) == 0 || s.ctx.Err() != nil {
			s.inputQueue, s.inputBusy = nil, false
			s.inputMu.Unlock()
			return
		}
		in := s.inputQueue[0]
		s.inputQueue = s.inputQueue[1:]
		s.inputMu.Unlock()
		if in.textMark > s.textGivenUp && !s.texts.waitTyped(s.ctx, in.textMark, keysBehindTextLimit) {
			if s.ctx.Err() != nil {
				continue
			}
			// Input behind the same text does not wait again.
			s.textGivenUp = in.textMark
			slog.Debug("keys go ahead of text still being typed", "waited", keysBehindTextLimit)
		}
		s.injectInput(in.seq, in.events)
	}
}

// injectInput injects one datagram's input, confined to the client's screen.
func (s *session) injectInput(seq uint32, events []dlnet.InputEvent) {
	if s.ctx.Err() != nil {
		return
	}
	s.pointerMu.Lock()
	screenW, screenH := s.screenW, s.screenH
	s.pointerMu.Unlock()
	if p := s.deskPointer.Swap(nil); p != nil {
		s.cursor.set(int32(p.X), int32(p.Y))
	}
	if movesPointer(events) {
		s.lastPointerInput.Store(time.Now().UnixNano())
	}
	events = confine(&s.cursor, events, screenW, screenH)
	var held *input.Held
	for _, e := range events {
		if e.Kind == dlnet.InputScrollPx {
			s.scrollPixels(seq, e)
			continue
		}
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

// wheelUnitsPerPx is how many 1/120 wheel units a pixel of the client's
// screen scrolls when the session has no remote desktop session to scroll
// like a touchpad: the rate the tablet used before InputScrollPx.
const wheelUnitsPerPx = 3

// pixelScroller scrolls as a touchpad's fingers do: (*desktop.Capture).Scroll,
// or a fake in tests.
type pixelScroller interface {
	Scroll(dx, dy float64, finish bool) error
}

// scrollPixels scrolls by an InputScrollPx record, which says how far the
// client's fingers moved the content in its screen's pixels. With a remote
// desktop session it scrolls like a touchpad, by the same distance in
// GNOME's logical pixels, so the content stays under the fingers; without
// one, as wheel steps.
func (s *session) scrollPixels(seq uint32, e dlnet.InputEvent) {
	moveX := float64(e.X) / dlnet.ScrollPxUnit
	moveY := float64(e.Y) / dlnet.ScrollPxUnit
	if sc := s.currentScroller(); sc != nil && !s.scrollRefused {
		s.pointerMu.Lock()
		scale := s.pixelScale
		s.pointerMu.Unlock()
		if scale <= 0 {
			scale = 1
		}
		// Content moving down is a scroll up.
		err := sc.Scroll(-moveX/scale, -moveY/scale, e.Flags&dlnet.ScrollPxFinished != 0)
		if err == nil {
			return
		}
		slog.Warn("Mutter refused a touchpad scroll; the session scrolls with wheel steps", "err", err)
		s.scrollRefused = true
	}
	// Wheel units count up and right; content moving right is a scroll left.
	s.wheelRemX -= moveX * wheelUnitsPerPx
	s.wheelRemY += moveY * wheelUnitsPerPx
	h, v := int32(s.wheelRemX), int32(s.wheelRemY)
	s.wheelRemX -= float64(h)
	s.wheelRemY -= float64(v)
	if e.Flags&dlnet.ScrollPxFinished != 0 {
		s.wheelRemX, s.wheelRemY = 0, 0
	}
	if h == 0 && v == 0 {
		return
	}
	if err := s.d.injector.HandleAt(seq, input.Event{Kind: input.KindScroll, X: h, Y: v}); err != nil {
		slog.Debug("inject input", "err", err)
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
		if c.IsImage() {
			s.receiveImagePart(ctx, c)
			return
		}
		if err := s.d.clipboard.Write(ctx, c.Text); err != nil {
			s.reportError(ctx, err)
		}
	case dlnet.MsgClipboardRequest:
		// An image's parts can take seconds on a slow link; the control
		// loop does not wait for them.
		go s.sendClipboard(ctx, true)
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
	case dlnet.MsgDesk:
		var c dlnet.DeskChange
		if err := json.Unmarshal(msg.Payload, &c); err != nil {
			s.reportError(ctx, fmt.Errorf("decode desk: %w", err))
			return
		}
		s.setDesk(ctx, c.Off)
	case dlnet.MsgMirror:
		var m dlnet.MirrorChange
		if err := json.Unmarshal(msg.Payload, &m); err != nil {
			s.reportError(ctx, fmt.Errorf("decode mirror: %w", err))
			return
		}
		s.setMirror(ctx, m.Connector)
	case dlnet.MsgResize:
		var r dlnet.Resize
		if err := json.Unmarshal(msg.Payload, &r); err != nil {
			s.reportError(ctx, fmt.Errorf("decode resize: %w", err))
			return
		}
		s.setSize(ctx, r.Width, r.Height)
	case dlnet.MsgKeepAwake:
		var k dlnet.KeepAwakeChange
		if err := json.Unmarshal(msg.Payload, &k); err != nil || !validKeepAwake(k.Minutes) {
			s.reportError(ctx, fmt.Errorf("keep awake: %d minutes is not allowed", k.Minutes))
			return
		}
		s.keepAwake = k.Minutes
		s.sendSessionInfo(ctx)
	case dlnet.MsgLockAfter:
		var l dlnet.LockAfterChange
		if err := json.Unmarshal(msg.Payload, &l); err != nil {
			s.reportError(ctx, fmt.Errorf("decode lock after: %w", err))
			return
		}
		s.setLockAfter(ctx, l.Lock)
		s.sendSessionInfo(ctx)
	case dlnet.MsgMic:
		var c dlnet.MicChange
		if err := json.Unmarshal(msg.Payload, &c); err != nil {
			s.reportError(ctx, fmt.Errorf("decode mic: %w", err))
			return
		}
		s.setMic(ctx, c.On)
	case dlnet.MsgTiles:
		s.setTiles(ctx, msg.Payload)
	case dlnet.MsgMaxFPS:
		fps, err := frameRateCap(msg.Payload)
		if err != nil {
			s.reportError(ctx, err)
			return
		}
		s.setFPSCap(ctx, fps)
	case dlnet.MsgReqIDR:
		s.requestKeyframe()
	default:
		slog.Debug("ignoring control message", "type", msg.Type)
	}
}

// setLockAfter answers a lock_after message. On a desk the session found
// unlocked it records the relock, or forgets it, as prepareDesktop does for
// the hello, so that a restart of desklinkd locks the desk as the client
// asked; a desk found locked owes its relock either way.
func (s *session) setLockAfter(ctx context.Context, lock bool) {
	s.lockAfter = lock
	if s.wasLocked {
		return
	}
	if lock {
		ctx, cancel := context.WithTimeout(ctx, unlockTimeout)
		defer cancel()
		s.d.relock.owe(ctx)
	} else {
		s.d.relock.forget()
	}
}

func (s *session) applyQuality(ctx context.Context, q dlnet.Quality) {
	cfg := s.d.opts.Config
	if q.QP > 0 {
		s.qMu.Lock()
		s.requestedQP = q.QP
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
	s.worstLevel = max(s.worstLevel, level)
	s.qMu.Lock()
	levels := s.quality.Levels()
	qp := s.quality.QP()
	s.qMu.Unlock()
	// A level the controller chose before a resize replaced the ladder.
	if level < 0 || level >= len(levels) {
		return
	}
	l := levels[level]
	if l.Width == s.width && l.Height == s.height {
		s.fps = l.FPS
		s.encMu.Lock()
		if s.enc != nil {
			s.enc.SetMaxFPS(s.streamFPS())
			s.enc.SetQP(qp)
		}
		s.encMu.Unlock()
		s.sendSessionInfo(ctx)
		return
	}
	s.resize(ctx, l.Width, l.Height, l.FPS)
}

// streamFPS is the rate the encoder runs at: the level's, at most the
// client's cap.
func (s *session) streamFPS() int { return video.CappedFPS(s.fps, s.fpsCap) }

// frameRateCap reads a max_fps message: a cap from 1 to maxFPS frames a
// second, or 0, which lifts it.
func frameRateCap(payload json.RawMessage) (int, error) {
	var m dlnet.MaxFPS
	if err := json.Unmarshal(payload, &m); err != nil {
		return 0, fmt.Errorf("decode max_fps: %w", err)
	}
	if m.FPS < 0 || m.FPS > maxFPS {
		return 0, fmt.Errorf("max_fps: %d frames a second is outside 0 to %d", m.FPS, maxFPS)
	}
	return m.FPS, nil
}

// setFPSCap answers a max_fps message: the encoder runs at most at fps, the
// rate the client's screen shows, until a cap of 0 lifts it. The frame
// limiter drops the frames above it before conversion and encoding; the own
// screen keeps its mode. The quality controller keeps its ladder and moves
// to the level that streams at the capped rate
// (QualityController.SetFrameRateCap), which supersedes a level it queued
// before. The session description tells the client the rate.
func (s *session) setFPSCap(ctx context.Context, fps int) {
	slog.Debug("the client caps the frame rate", "fps", fps)
	s.qMu.Lock()
	s.quality.SetFrameRateCap(time.Now(), fps)
	s.qMu.Unlock()
	s.fpsCap = fps
	s.applyLevel(ctx, s.controllerLevel())
}

// controllerLevel is the quality controller's current level.
func (s *session) controllerLevel() int {
	s.qMu.Lock()
	defer s.qMu.Unlock()
	return s.quality.Level()
}

// resize stops the pipeline before switching the monitor mode, so the old
// pipeline never sees the capture change size, then starts a new one.
func (s *session) resize(ctx context.Context, w, h, fps int) {
	if !s.stopForRestart() {
		return
	}
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

// setSize answers a resize message: the client's own screen gets the size of
// the client's screen, as when the tablet rotates.
func (s *session) setSize(ctx context.Context, w, h int) {
	err := s.resizeOwnScreen(ctx, w, h)
	switch {
	case ctx.Err() != nil:
		// The resize failed half way and ended the session; its cleanup
		// gives the desk back.
	case err != nil:
		s.reportError(ctx, err)
	default:
		s.sendSessionInfo(ctx)
	}
}

// resizeOwnScreen gives the client's own screen a new size of w x h. Mutter
// takes a virtual monitor's mode from the stream's caps when the cast starts,
// so the session stops the cast and starts another of the new size, then
// places the new screen as it placed the old one: the same scale, the
// desk's monitors off if they were, and the client's primary choice. The
// quality ladder starts again for the new size on the same rung, and input
// maps onto the new screen. A desk the session turned off is turned on
// before the old screen goes, as at the end of a session, so Mutter is
// never left without a monitor. Windows on the old screen move to the
// primary monitor as it goes, so beside the desk they land on the desk.
// A request the session cannot take returns an error and changes nothing;
// a failure once the old cast has stopped ends the session.
func (s *session) resizeOwnScreen(ctx context.Context, w, h int) error {
	if s.capture == nil || !s.capture.Virtual {
		return errors.New("only the client's own screen can change its size")
	}
	if w < minStreamDim || w > maxStreamDim || h < minStreamDim || h > maxStreamDim {
		return fmt.Errorf("a %dx%d screen is outside %d to %d pixels a side", w, h, minStreamDim, maxStreamDim)
	}
	w, h = w&^1, h&^1
	if w == s.captureW && h == s.captureH {
		return nil
	}
	// Text being typed through the old cast finishes before it stops; text
	// after it waits for the new one.
	s.holdTyping()
	defer s.releaseTyping()
	if !s.stopForRestart() {
		return errPipelineStuck
	}
	slog.Debug("resizing the client's screen", "from", fmt.Sprintf("%dx%d", s.captureW, s.captureH), "to", fmt.Sprintf("%dx%d", w, h))
	s.newLadder(w, h)
	s.closeVirtualScreen(ctx)
	if err := ctx.Err(); err != nil {
		// The session ended while the old screen went: no new screen starts,
		// and the desk's monitors are on, as cleanup would leave them.
		return err
	}
	fail := func(err error) error {
		s.cancel(err)
		return err
	}
	if err := s.startVirtualCast(ctx); err != nil {
		return fail(fmt.Errorf("start the client's screen at %dx%d: %w", w, h, err))
	}
	s.pointerMu.Lock()
	s.screenW, s.screenH = w, h
	s.pointerMu.Unlock()
	if err := s.startPipeline(ctx); err != nil {
		return fail(fmt.Errorf("start the pipeline for the client's %dx%d screen: %w", w, h, err))
	}
	s.arrangeVirtualScreen(ctx)
	return nil
}

// newLadder replaces the quality ladder with one for a client screen of
// w x h, at the same top frame rate and on the same rung, starting from the
// client's requested quantizer. Its rungs correspond to the old ones in
// either orientation (ladder). The new controller keeps what the old one
// learned about the connection and ignores the restart's keyframe
// (QualityController.Follow).
func (s *session) newLadder(w, h int) {
	cfg := s.d.opts.Config
	s.qMu.Lock()
	old := s.quality
	levels := ladder(w, h, old.Levels()[0].FPS)
	if !old.Auto() {
		levels = levels[:1]
	}
	s.quality = old.Follow(video.QualityConfig{
		Requested: s.requestedQP, MinQP: cfg.MinQP, MaxQP: cfg.MaxQP,
		Levels: levels, Start: old.Level(), Auto: old.Auto(), Fixed: old.Fixed(),
	}, time.Now())
	l := s.quality.Levels()[s.quality.Level()]
	s.qMu.Unlock()
	// The new ladder starts on the level the old controller had chosen.
	select {
	case <-s.levelChanges:
	default:
	}
	s.width, s.height, s.fps = l.Width, l.Height, l.FPS
}

// closeVirtualScreen stops the cast of the client's own screen for a
// resize, turning the desk's monitors on first if the session turned them
// off, and waits for the virtual monitor to leave the layout. Each step runs
// to the end even when the session is ending meanwhile (teardownContext):
// once the capture is replaced, cleanup no longer knows the old cast.
func (s *session) closeVirtualScreen(ctx context.Context) {
	if s.ownAlone {
		stepCtx, cancel := teardownContext(ctx)
		s.turnDeskOn(stepCtx)
		cancel()
	}
	stepCtx, cancel := teardownContext(ctx)
	if err := s.d.stopCast(stepCtx, s.capture); err != nil {
		slog.Debug("close screencast", "err", err)
	}
	cancel()
	stepCtx, cancel = teardownContext(ctx)
	gone := s.waitVirtualGone(stepCtx)
	cancel()
	if s.captured != "" && !gone {
		s.closedVirtual = append(s.closedVirtual, s.captured)
	}
	s.useCapture(nil)
	s.captured = ""
	s.ownAlone, s.ownPrimary = false, false
	// Until the new screen is placed, the end of the session restores the
	// desk's layout from before the session whatever it finds.
	s.deskArranged = nil
}

// mirroring is SessionInfo.Mirroring: the connector of the monitor the
// session mirrors, empty for the own screen.
func (s *session) mirroring() string {
	if s.capture == nil || s.capture.Virtual {
		return ""
	}
	return s.captured
}

// monitorList is SessionInfo.Monitors: the monitors that are on, without
// the session's own virtual screen, or the last list when the layout cannot
// be read. A session with its own screen lists only monitors that were
// there before its cast, which leaves out its virtual screen even before
// the session knows its connector.
func (s *session) monitorList(ctx context.Context) []dlnet.MonitorInfo {
	st, err := s.d.display.DisplayState(ctx)
	if err != nil {
		slog.Debug("read monitor layout for the session description", "err", err)
		return s.monitors
	}
	own := s.capture != nil && s.capture.Virtual
	var out []dlnet.MonitorInfo
	for _, m := range st.ActiveMonitors() {
		if !own && m.Connector == s.captured {
			s.mirrorScale = s.mirroredScale(st, m)
		}
		if own && (m.Connector == s.captured || !slices.Contains(s.deskMonitors, m.Connector)) {
			continue
		}
		out = append(out, dlnet.MonitorInfo{
			Connector: m.Connector, Width: int(m.Width), Height: int(m.Height), Refresh: m.Refresh, Primary: m.Primary,
		})
	}
	s.monitors = out
	return out
}

// sessionInfo describes the stream as it is now.
func (s *session) sessionInfo(ctx context.Context) dlnet.SessionInfo {
	monitors := s.monitorList(ctx)
	s.qMu.Lock()
	defer s.qMu.Unlock()
	levels := make([]dlnet.Level, 0, len(s.quality.Levels()))
	for _, l := range s.quality.Levels() {
		levels = append(levels, dlnet.Level{Width: l.Width, Height: l.Height, FPS: l.FPS})
	}
	return dlnet.SessionInfo{
		SessionID: s.media.ID(), UDPPort: s.d.media.Port(),
		Codec: string(s.codec), Width: s.width, Height: s.height, FPS: s.streamFPS(), QP: s.quality.QP(),
		Display: s.display, Auto: s.quality.Auto(), Level: s.quality.Level(), Levels: levels,
		Audio: s.audio != nil, Screen: s.screenMode(), Scale: s.ownScale(), KeepAwake: s.keepAwake, LockAfter: s.lockAfter,
		Primary: s.primaryState(), DeskOff: s.deskOffState(), ClipboardImages: s.d.opts.Config.MaxClipboardImageBytes,
		Monitors: monitors, Mirroring: s.mirroring(), Keysyms: s.currentTyper() != nil,
		Mic: s.micState(), Tiles: s.tilesOn(), TilesUDP: s.tilesUDP(), Mouse: s.mouseInfo(ctx),
		ScrollPx: s.currentScroller() != nil, Pointer: s.pointerApart,
	}
}

// mirroredScale is mirrorScale for the mirrored monitor m: its scale, times
// the client's screen width over the monitor's, since the client shows the
// monitor scaled to its screen; 0 when unknown.
func (s *session) mirroredScale(st desktop.DisplayState, m desktop.ActiveMonitor) float64 {
	scale, ok := st.ConnectorScale(m.Connector)
	if !ok || m.Width <= 0 {
		return 0
	}
	s.pointerMu.Lock()
	screenW := s.screenW
	s.pointerMu.Unlock()
	return scale * float64(screenW) / float64(m.Width)
}

// mouseInfo is SessionInfo.Mouse: GNOME's mouse settings, read once a
// session, and the scale of the screen the session shows.
func (s *session) mouseInfo(ctx context.Context) *dlnet.MouseInfo {
	s.mouseOnce.Do(func() {
		if s.d.mouseSettings != nil {
			s.mouse = s.d.mouseSettings(ctx)
		}
	})
	scale := s.ownScale()
	if scale <= 0 {
		scale = s.mirrorScale
	}
	if scale <= 0 {
		scale = 1
	}
	s.pointerMu.Lock()
	s.pixelScale = scale
	s.pointerMu.Unlock()
	return &dlnet.MouseInfo{Speed: s.mouse.Speed, Flat: s.mouse.Flat, Scale: scale}
}

// deskOffState is SessionInfo.DeskOff: whether the desk's monitors are off,
// or nil when the client cannot switch them: the session has no own screen
// in the layout, or it found the desk locked and keeps it dark.
func (s *session) deskOffState() *bool {
	if s.capture == nil || !s.capture.Virtual || s.captured == "" || (s.ownAlone && !s.deskOffByClient) {
		return nil
	}
	off := s.ownAlone
	return &off
}

// primaryState is SessionInfo.Primary: whether the own screen is primary,
// or nil when the session cannot move the primary: it has no own screen in
// the layout, or that screen is the only monitor on, so a switch to give the
// primary back could only fail.
func (s *session) primaryState() *bool {
	if s.capture == nil || !s.capture.Virtual || s.captured == "" || s.ownAlone {
		return nil
	}
	primary := s.ownPrimary
	return &primary
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
	// A route that failed and could not be undone comes back too, so that
	// cleanup (stopAudio) removes it.
	if route.Module != "" {
		s.audioRoute = &route
		if err := s.d.state.SetAudioDefaultSink(route.PreviousSink); err != nil {
			slog.Debug("save the output to restore", "err", err)
		}
	}
	if err != nil {
		slog.Warn("move the sound to the DeskLink output; streaming the default output instead", "err", err)
	} else {
		device = route.Monitor()
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
	if err := s.send(ctx, dlnet.MsgSession, s.sessionInfo(ctx)); err != nil {
		slog.Debug("send updated session info", "err", err)
	}
}

const (
	// controlSendTimeout bounds writing one control message to the client,
	// and clipboardPartTimeout one part of a clipboard image, up to 512 KiB.
	controlSendTimeout   = 10 * time.Second
	clipboardPartTimeout = 30 * time.Second
)

// send writes one control message to the client, unless the session has
// ended (sendWithin).
func (s *session) send(ctx context.Context, msgType string, payload any) error {
	return s.sendWithin(ctx, controlSendTimeout, msgType, payload)
}

// sendWithin writes one control message to the client within limit, unless
// the session has ended. The write gets a context of its own, because
// coder/websocket closes the connection when the context of a write ends
// during it: a session ended by a takeover must leave the connection open
// for the takeover notice (tellWhenTakenOver).
func (s *session) sendWithin(ctx context.Context, limit time.Duration, msgType string, payload any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	return s.conn.Send(sendCtx, msgType, payload)
}

// sendBinary writes one tile message like sendWithin, so a session ended by
// a takeover leaves the connection open for the takeover notice.
func (s *session) sendBinary(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlSendTimeout)
	defer cancel()
	return s.conn.SendBinary(sendCtx, data)
}

// imageLimit is the largest clipboard image the session sends: what the
// client takes, and no more than the host's limit; 0 sends text only.
func (s *session) imageLimit() int {
	return min(s.clientImages, s.d.opts.Config.MaxClipboardImageBytes)
}

// receiveImagePart takes one part of a clipboard image from the client and
// puts the image on the clipboard once its last part arrived.
func (s *session) receiveImagePart(ctx context.Context, c dlnet.Clipboard) {
	if s.images.Limit == 0 {
		slog.Debug("dropping a clipboard image: clipboard images are turned off")
		return
	}
	png, err := s.images.Add(c)
	if err != nil {
		s.reportError(ctx, err)
		return
	}
	if png == nil {
		return
	}
	if err := s.d.clipboard.WriteImage(ctx, png); err != nil {
		s.reportError(ctx, err)
	}
}

// sendClipboard sends the desk's clipboard to the client if it changed, or
// always.
func (s *session) sendClipboard(ctx context.Context, always bool) {
	if ctx.Err() != nil {
		return
	}
	s.clipMu.Lock()
	defer s.clipMu.Unlock()
	content, changed, err := s.d.clipboard.Read(ctx, s.imageLimit())
	if err != nil {
		slog.Debug("read clipboard", "err", err)
		return
	}
	if !changed && !always {
		return
	}
	s.sendClipboardLocked(ctx, content)
}

// sendClipboardLocked sends clipboard content, an image as its parts in
// order. A session that ends during an image sends no more parts, which can
// take seconds on a slow link. The caller holds clipMu.
func (s *session) sendClipboardLocked(ctx context.Context, content desktop.Content) {
	if content.PNG != nil {
		for _, part := range dlnet.ClipboardImageParts(content.PNG) {
			if err := s.sendWithin(ctx, clipboardPartTimeout, dlnet.MsgClipboard, part); err != nil {
				slog.Debug("send clipboard image", "err", err)
				return
			}
		}
		return
	}
	if content.Text == "" {
		return
	}
	if err := s.send(ctx, dlnet.MsgClipboard, dlnet.Clipboard{Text: content.Text}); err != nil {
		slog.Debug("send clipboard", "err", err)
	}
}

func (s *session) watchClipboard(ctx context.Context) {
	err := s.d.clipboard.Watch(ctx, s.imageLimit(), func(content desktop.Content) {
		s.clipMu.Lock()
		defer s.clipMu.Unlock()
		s.sendClipboardLocked(ctx, content)
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
	if serr := s.send(ctx, dlnet.MsgError, dlnet.ErrorInfo{Message: err.Error()}); serr != nil {
		slog.Debug("send error", "err", serr)
	}
}

// cleanup undoes everything the session changed. The client loses control
// first: the session's context ends, so its input and control messages are
// dropped; its media session closes, waiting for input being injected, and
// sends nothing more; the input worker stops; then every held key, button
// and pen contact is lifted.
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
	// A key the worker injects after the release would stay down.
	s.inputWorkers.Wait()
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
	// A session that found the desk unlocked locks it too when its client
	// asked (lock_after), but only if no session took over: a reconnecting
	// client finds the desk as it left it. The relock recorded for lock_after
	// goes with it then; a successor records its own if its client asks.
	if s.d.ctl.leave(ctx, s.active, s.wasLocked, s.lockAfter) && s.lockAfter && !s.wasLocked {
		s.d.relock.forget()
	}
	s.active.summary = sessionSummary{display: s.display, worstLevel: s.levelName(s.worstLevel)}
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
	s.removeMic(ctx)
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
	if s.tiles != nil {
		s.tiles.logTotals()
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

// levelName describes quality level i for the session history, such as
// "2560x1600@60".
func (s *session) levelName(i int) string {
	if s.quality == nil {
		return ""
	}
	s.qMu.Lock()
	defer s.qMu.Unlock()
	levels := s.quality.Levels()
	if i < 0 || i >= len(levels) {
		return ""
	}
	l := levels[i]
	return fmt.Sprintf("%dx%d@%d", l.Width, l.Height, l.FPS)
}
