package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

const (
	// writeTimeout bounds a control write on a stalled link.
	writeTimeout = 5 * time.Second
	// MaxClipboardBytes is the largest clipboard text synced, like the host's.
	MaxClipboardBytes = 256 << 10
)

// ErrClipboardTooLarge means the text exceeds MaxClipboardBytes.
var ErrClipboardTooLarge = errors.New("clipboard text exceeds the 256 KB sync limit")

// ErrClipboardImageTooLarge means an image exceeds the host's limit.
var ErrClipboardImageTooLarge = errors.New("the clipboard image is larger than the host takes")

// ErrClipboardImagesUnsupported means the host takes clipboard text only:
// it predates clipboard images, or they are turned off there.
var ErrClipboardImagesUnsupported = errors.New("the host takes clipboard text only")

// ErrMediaLost ends a session whose host sent no authenticated UDP datagram
// for 10 s: without them there is no video, and input may not arrive.
var ErrMediaLost = errors.New("the UDP media path failed")

// ErrReplaced ends a session that another device took over.
var ErrReplaced = errors.New("another device took over the session")

// App is an entry of the host's app drawer.
type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Exec string `json:"exec"`
}

// Request is what the client asks the host for in hello. With Auto the size
// and rate are the ladder's top and Start, if set, the level to start at.
type Request struct {
	Codec  string
	Width  int
	Height int
	FPS    int
	QP     int
	Auto   bool
	Start  *dlnet.Level
	// Fixed asks the host to keep the quantizer, size and rate for the
	// whole session (the Sharp preset).
	Fixed bool
	// Screen is what the session shows: dlnet.ScreenOwn or ScreenMirror, or
	// empty for the host's default, the own screen only when the desk is
	// locked.
	Screen string
	// Scale is the own screen's scale; zero leaves the host's default, 2.
	Scale float64
	// KeepAwake is how long the computer stays awake after the session, as
	// in dlnet.Hello.
	KeepAwake int
	// LockAfter locks the desk when the session ends, also when it was
	// unlocked as the client came.
	LockAfter bool
	// ClipboardImages is the largest clipboard image the client takes, in
	// bytes; zero takes text only. Events.ClipboardImage receives them.
	ClipboardImages int
	// Monitor is the connector a mirroring session shows, the host's
	// Profile.Monitor; empty leaves the choice to the host.
	Monitor string
	// OnAudio, when set, asks for the desktop's sound and receives each Opus
	// packet on the network goroutine.
	OnAudio func(dlnet.AudioPacket)
}

// Events receives what the host sends. Methods run on the session's network
// goroutines; GUI code must hop to its own thread.
type Events interface {
	// SessionInfo describes the stream. It does not mean video can arrive
	// yet; MediaReady does.
	SessionInfo(info dlnet.SessionInfo)
	// MediaReady reports that authenticated UDP datagrams from the host
	// arrive: at the first one, and again when they resume after MediaQuiet.
	MediaReady()
	// MediaQuiet reports that none has arrived for a few seconds, with a
	// message for the user. If the silence lasts 10 s, the session closes
	// with ErrMediaLost.
	MediaQuiet(message string)
	Apps(apps []App)
	Clipboard(text string)
	// ClipboardImage receives a PNG the host copied, once all its parts
	// arrived; only with Request.ClipboardImages set.
	ClipboardImage(png []byte)
	Notice(message string)
	Closed(err error)
}

// Session is one connection to a host.
type Session struct {
	profile Profile
	ws      *websocket.Conn
	events  Events
	sink    func(EncodedFrame)
	onAudio func(dlnet.AudioPacket)
	ctx     context.Context
	cancel  context.CancelFunc
	timing  mediaTiming
	// images joins the parts of the host's clipboard images; only the
	// reading goroutine uses it. clipMu keeps the parts of two images this
	// client sends from interleaving.
	images dlnet.ImageAssembler
	clipMu sync.Mutex

	// texts holds text waiting to be sent for the host to type (TypeText).
	texts chan string

	mu        sync.Mutex
	transport *transport
	info      dlnet.SessionInfo
	finished  bool
	closeOnce sync.Once
}

// Connect checks /health, opens the authenticated WebSocket and sends hello.
// Frames are passed to sink in decode order on the UDP goroutine.
func Connect(ctx context.Context, p Profile, req Request, sink func(EncodedFrame), ev Events) (*Session, error) {
	return connect(ctx, p, req, sink, ev, defaultMediaTiming)
}

// connect is Connect with the media path's time limits, which tests shorten.
func connect(ctx context.Context, p Profile, req Request, sink func(EncodedFrame), ev Events, timing mediaTiming) (*Session, error) {
	if !p.Paired() {
		return nil, errors.New("host is not paired")
	}
	httpc, _ := pinnedHTTP(p.Fingerprint)
	base := baseURL(p.IP, p.Port)
	h, err := health(ctx, httpc, base)
	if err != nil {
		return nil, err
	}
	// Stamped with the host's clock so a laptop with a wrong clock still connects.
	token, err := crypto.AuthToken(p.PeerID, p.PeerKey, time.UnixMilli(h.TimeMs))
	if err != nil {
		return nil, err
	}
	tlsCfg, _ := pinnedTLS(p.Fingerprint)
	ws, resp, err := websocket.Dial(ctx, "wss"+base[len("https"):]+"/ws", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, errors.New("the host rejected this pairing: pair again")
		}
		return nil, fmt.Errorf("open control connection: %w", err)
	}
	ws.SetReadLimit(dlnet.MaxControlMessage)
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{profile: p, ws: ws, events: ev, sink: sink, ctx: sctx, cancel: cancel, timing: timing,
		texts: make(chan string, textQueueSize)}
	s.onAudio = req.OnAudio
	s.images.Limit = req.ClipboardImages
	hello := dlnet.Hello{
		Codec: req.Codec, Width: req.Width, Height: req.Height, FPS: req.FPS, QP: req.QP, Auto: req.Auto, Start: req.Start, Fixed: req.Fixed,
		Audio: req.OnAudio != nil, Screen: req.Screen, Scale: req.Scale, KeepAwake: req.KeepAwake, Monitor: req.Monitor,
		LockAfter:       req.LockAfter,
		ClipboardImages: req.ClipboardImages,
	}
	if err := s.send(dlnet.MsgHello, hello); err != nil {
		s.Close()
		return nil, err
	}
	go s.read()
	go s.sendTexts()
	return s, nil
}

func (s *Session) send(msgType string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s: %w", msgType, err)
	}
	data, err := json.Marshal(dlnet.Message{Type: msgType, Payload: raw})
	if err != nil {
		return fmt.Errorf("encode %s: %w", msgType, err)
	}
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	if err := s.ws.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("send %s: %w", msgType, err)
	}
	return nil
}

func (s *Session) read() {
	var err error
	defer func() { s.finish(err) }()
	for {
		var data []byte
		_, data, err = s.ws.Read(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				err = nil
			}
			return
		}
		var m dlnet.Message
		if jerr := json.Unmarshal(data, &m); jerr != nil {
			slog.Debug("decode control message", "err", jerr)
			continue
		}
		if err = s.handle(m); err != nil {
			return
		}
	}
}

func (s *Session) handle(m dlnet.Message) error {
	switch m.Type {
	case dlnet.MsgSession:
		var info dlnet.SessionInfo
		if err := json.Unmarshal(m.Payload, &info); err != nil {
			return fmt.Errorf("decode session: %w", err)
		}
		if err := s.startMedia(info); err != nil {
			return err
		}
		s.events.SessionInfo(info)
	case dlnet.MsgApps:
		var apps []App
		if err := json.Unmarshal(m.Payload, &apps); err != nil {
			return fmt.Errorf("decode apps: %w", err)
		}
		s.events.Apps(apps)
	case dlnet.MsgClipboard:
		var c dlnet.Clipboard
		if err := json.Unmarshal(m.Payload, &c); err != nil {
			slog.Debug("decode clipboard", "err", err)
			return nil
		}
		if !c.IsImage() {
			s.events.Clipboard(c.Text)
			return nil
		}
		if s.images.Limit == 0 {
			return nil
		}
		png, err := s.images.Add(c)
		if err != nil {
			slog.Debug("clipboard image from the host", "err", err)
		} else if png != nil {
			s.events.ClipboardImage(png)
		}
	case dlnet.MsgError:
		var e dlnet.ErrorInfo
		if err := json.Unmarshal(m.Payload, &e); err != nil {
			return fmt.Errorf("decode error: %w", err)
		}
		// Another device took the desk over: the session ends with that,
		// rather than a dropped connection.
		if e.Reason == dlnet.ReasonReplaced {
			return fmt.Errorf("%w: %s", ErrReplaced, e.Message)
		}
		s.mu.Lock()
		started := s.transport != nil
		s.mu.Unlock()
		if !started {
			return errors.New(e.Message)
		}
		s.events.Notice(e.Message)
	}
	return nil
}

// startMedia opens the UDP transport on the first session message; later ones
// only report a new size.
func (s *Session) startMedia(info dlnet.SessionInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info = info
	if s.transport != nil || s.finished {
		return nil
	}
	keys, err := crypto.DeriveSessionKeys(s.profile.PeerKey, info.SessionID, info.HostNonce)
	if err != nil {
		return err
	}
	port := info.UDPPort
	var t *transport
	t, err = newTransport(s.profile.IP, port, info.SessionID, keys, s.timing, mediaHandlers{
		frame: s.sink,
		audio: s.onAudio,
		state: func(e mediaEvent) { s.media(e, port, t.watch.everUp()) },
	})
	if err != nil {
		return err
	}
	s.transport = t
	go t.run(s.ctx)
	return nil
}

// media turns a change of the UDP path into an event, or ends the session
// when the path is lost. up says whether any host datagram has arrived.
func (s *Session) media(e mediaEvent, port int, up bool) {
	if s.ctx.Err() != nil {
		return
	}
	name := s.profile.Name
	if name == "" {
		name = s.profile.IP
	}
	timeout := int(s.timing.timeout / time.Second)
	switch e {
	case mediaReady:
		noteMediaWorked(s.profile.IP, s.profile.Port)
		s.events.MediaReady()
	case mediaQuiet:
		if up {
			s.events.MediaQuiet(fmt.Sprintf("No packets from %s for a few seconds. Waiting for the link to recover…", name))
		} else {
			s.events.MediaQuiet(fmt.Sprintf("No video from %s yet: nothing has arrived on UDP port %d. A firewall or the VPN may block it. Still trying…", name, port))
		}
	case mediaLost:
		if up {
			s.closeWith(fmt.Errorf("%w: no packets from %s for %d s", ErrMediaLost, name, timeout))
		} else {
			// The next Connect tries the host's other addresses first.
			noteMediaFailure(s.profile.IP, s.profile.Port, time.Now())
			s.closeWith(fmt.Errorf("%w: nothing arrived from %s on UDP port %d within %d s; a firewall or the VPN may block it", ErrMediaLost, name, port, timeout))
		}
	}
}

func (s *Session) withTransport(fn func(*transport)) {
	s.mu.Lock()
	t := s.transport
	s.mu.Unlock()
	if t != nil {
		fn(t)
	}
}

// SendInput sends input events over UDP.
func (s *Session) SendInput(events ...dlnet.InputEvent) {
	s.withTransport(func(t *transport) { t.sendInput(events) })
}

// RequestKeyframe asks for a keyframe after a decoder error.
func (s *Session) RequestKeyframe() { s.withTransport((*transport).requestKeyframe) }

// FrameShown reports that the decoder is showing the frame the host
// captured at captureMicros, for the delay in Stats.
func (s *Session) FrameShown(captureMicros uint64) {
	s.withTransport(func(t *transport) { t.frameShown(captureMicros) })
}

// Stats returns the live link numbers.
func (s *Session) Stats() Stats {
	var st Stats
	s.withTransport(func(t *transport) { st = t.currentStats() })
	return st
}

// Info returns the latest session description.
func (s *Session) Info() dlnet.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Launch starts an app from the host's list.
func (s *Session) Launch(a App) error {
	return s.send(dlnet.MsgLaunch, dlnet.Launch{ID: a.ID, Exec: a.Exec})
}

// PushClipboard sends text to the host clipboard.
func (s *Session) PushClipboard(text string) error {
	if len(text) > MaxClipboardBytes {
		return ErrClipboardTooLarge
	}
	return s.send(dlnet.MsgClipboard, dlnet.Clipboard{Text: text})
}

// PushClipboardImage sends a PNG to the host clipboard, as parts in order.
// It returns ErrClipboardImagesUnsupported when the host takes text only
// and ErrClipboardImageTooLarge above the host's limit.
func (s *Session) PushClipboardImage(png []byte) error {
	limit := s.Info().ClipboardImages
	switch {
	case limit == 0:
		return ErrClipboardImagesUnsupported
	case len(png) > limit:
		return fmt.Errorf("%w (%d MiB)", ErrClipboardImageTooLarge, limit>>20)
	case !dlnet.IsPNG(png):
		return errors.New("the clipboard image is not a PNG")
	}
	s.clipMu.Lock()
	defer s.clipMu.Unlock()
	for _, part := range dlnet.ClipboardImageParts(png) {
		if err := s.send(dlnet.MsgClipboard, part); err != nil {
			return err
		}
	}
	return nil
}

// RequestClipboard asks the host for its clipboard.
func (s *Session) RequestClipboard() error {
	return s.send(dlnet.MsgClipboardRequest, struct{}{})
}

// SetScale asks for another scale of the session's own screen. The host
// answers with a new SessionInfo, or ignores it while mirroring.
func (s *Session) SetScale(scale float64) error {
	return s.send(dlnet.MsgScale, dlnet.ScaleChange{Scale: scale})
}

// SetKeepAwake changes how long the computer stays awake after the session,
// a dlnet.Hello.KeepAwake value. The host answers with a new SessionInfo.
func (s *Session) SetKeepAwake(minutes int) error {
	return s.send(dlnet.MsgKeepAwake, dlnet.KeepAwakeChange{Minutes: minutes})
}

// SetLockAfter sets whether the desk locks when the session ends. The host
// answers with a new SessionInfo.
func (s *Session) SetLockAfter(lock bool) error {
	return s.send(dlnet.MsgLockAfter, dlnet.LockAfterChange{Lock: lock})
}

// SetPrimary makes the own screen GNOME's primary monitor, or gives the
// primary back to the desk, until the session ends. The host answers with a
// new SessionInfo, or with an error that Events.Notice reports.
func (s *Session) SetPrimary(primary bool) error {
	return s.send(dlnet.MsgPrimary, dlnet.PrimaryChange{Primary: primary})
}

// SetDeskOff turns the desk's monitors off, which moves every window to the
// own screen, or on again. The host answers with a new SessionInfo, or with
// an error that Events.Notice reports.
func (s *Session) SetDeskOff(off bool) error {
	return s.send(dlnet.MsgDesk, dlnet.DeskChange{Off: off})
}

// Mirror switches a mirroring session to the host's monitor on connector, one
// of SessionInfo.Monitors. The host answers with a new SessionInfo, or with
// an error that Events.Notice reports.
func (s *Session) Mirror(connector string) error {
	return s.send(dlnet.MsgMirror, dlnet.MirrorChange{Connector: connector})
}

// Resize asks for the own screen at width x height device pixels. The host
// starts the screen again at that size and answers with a new SessionInfo,
// or with an error that Events.Notice reports.
func (s *Session) Resize(width, height int) error {
	return s.send(dlnet.MsgResize, dlnet.Resize{Width: width, Height: height})
}

// Close disconnects without waiting; the host restores the desktop.
func (s *Session) Close() { s.closeWith(nil) }

// closeWith tells the host the session is over and reports err as the
// reason. The close handshake runs in the background, because on a dead
// link it waits for the host's reply until it times out.
func (s *Session) closeWith(err error) {
	go func() {
		if cerr := s.ws.Close(websocket.StatusNormalClosure, "client disconnect"); cerr != nil {
			slog.Debug("close control connection", "err", cerr)
		}
	}()
	s.finish(err)
}

// finish ends the session once: it releases held input over UDP, stops the
// transport and reports Closed.
func (s *Session) finish(err error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.finished = true
		t := s.transport
		s.mu.Unlock()
		if t != nil {
			t.releaseHeld()
		}
		s.cancel()
		if t != nil {
			t.close()
		}
		s.events.Closed(err)
	})
}
