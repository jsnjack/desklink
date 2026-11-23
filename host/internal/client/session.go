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
	// OnAudio, when set, asks for the desktop's sound and receives each Opus
	// packet on the network goroutine.
	OnAudio func(dlnet.AudioPacket)
}

// Events receives what the host sends. Methods run on the session's reader
// goroutine; GUI code must hop to its own thread.
type Events interface {
	SessionInfo(info dlnet.SessionInfo)
	Apps(apps []App)
	Clipboard(text string)
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

	mu        sync.Mutex
	transport *transport
	info      dlnet.SessionInfo
	finished  bool
	closeOnce sync.Once
}

// Connect checks /health, opens the authenticated WebSocket and sends hello.
// Frames are passed to sink in decode order on the UDP goroutine.
func Connect(ctx context.Context, p Profile, req Request, sink func(EncodedFrame), ev Events) (*Session, error) {
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
	s := &Session{profile: p, ws: ws, events: ev, sink: sink, ctx: sctx, cancel: cancel}
	s.onAudio = req.OnAudio
	hello := dlnet.Hello{
		Codec: req.Codec, Width: req.Width, Height: req.Height, FPS: req.FPS, QP: req.QP, Auto: req.Auto, Start: req.Start,
		Audio: req.OnAudio != nil,
	}
	if err := s.send(dlnet.MsgHello, hello); err != nil {
		s.Close()
		return nil, err
	}
	go s.read()
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
		if err := json.Unmarshal(m.Payload, &c); err == nil {
			s.events.Clipboard(c.Text)
		}
	case dlnet.MsgError:
		var e dlnet.ErrorInfo
		if err := json.Unmarshal(m.Payload, &e); err != nil {
			return fmt.Errorf("decode error: %w", err)
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
	t, err := newTransport(s.profile.IP, info.UDPPort, info.SessionID, keys, s.sink, s.onAudio)
	if err != nil {
		return err
	}
	s.transport = t
	go t.run(s.ctx)
	return nil
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

// RequestClipboard asks the host for its clipboard.
func (s *Session) RequestClipboard() error {
	return s.send(dlnet.MsgClipboardRequest, struct{}{})
}

// Close disconnects without waiting; the host restores the desktop. The
// close handshake runs in the background, because on a dead link it waits
// for the host's reply until it times out.
func (s *Session) Close() {
	go func() {
		if err := s.ws.Close(websocket.StatusNormalClosure, "client disconnect"); err != nil {
			slog.Debug("close control connection", "err", err)
		}
	}()
	s.finish(nil)
}

func (s *Session) finish(err error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.finished = true
		t := s.transport
		s.mu.Unlock()
		s.cancel()
		if t != nil {
			t.close()
		}
		s.events.Closed(err)
	})
}
