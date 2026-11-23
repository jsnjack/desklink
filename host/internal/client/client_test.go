package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdnet "net"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"desklink/internal/config"
	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

func frag(id uint16, index, count uint16, key bool, data string) dlnet.VideoFragment {
	return dlnet.VideoFragment{FrameID: id, Index: index, Count: count, Keyframe: key, Data: []byte(data)}
}

func ids(frames []EncodedFrame) []uint16 {
	out := []uint16{}
	for _, f := range frames {
		out = append(out, f.ID)
	}
	return out
}

func feed(a *FrameAssembler, fs ...dlnet.VideoFragment) []uint16 {
	out := []uint16{}
	for _, f := range fs {
		out = append(out, ids(a.Add(f))...)
	}
	return out
}

// The same cases as FrameAssemblerTest.kt, so both clients recover alike.
func TestFrameAssembler(t *testing.T) {
	cases := []struct {
		name  string
		steps [][]dlnet.VideoFragment
		want  [][]uint16
		check func(*testing.T, *FrameAssembler)
	}{
		{
			name:  "waits for a keyframe",
			steps: [][]dlnet.VideoFragment{{frag(1, 0, 1, false, "p")}, {frag(2, 0, 1, true, "k"), frag(3, 0, 1, false, "p")}},
			want:  [][]uint16{{}, {2, 3}},
		},
		{
			name:  "holds a later frame until the earlier one completes",
			steps: [][]dlnet.VideoFragment{{frag(1, 0, 1, true, "k")}, {frag(2, 0, 2, false, "x"), frag(3, 0, 1, false, "p")}, {frag(2, 1, 2, false, "y")}},
			want:  [][]uint16{{1}, {}, {2, 3}},
		},
		{
			name: "declares loss two frames later and recovers at the next keyframe",
			steps: [][]dlnet.VideoFragment{
				{frag(1, 0, 1, true, "k"), frag(2, 0, 2, false, "half"), frag(3, 0, 1, false, "p")},
				{frag(4, 0, 1, false, "p")},
				{frag(5, 0, 1, false, "p")},
				{frag(6, 0, 1, true, "k"), frag(7, 0, 1, false, "p")},
			},
			want: [][]uint16{{1}, {}, {}, {6, 7}},
			check: func(t *testing.T, a *FrameAssembler) {
				if a.NeedKeyframe() {
					t.Error("still waiting after the keyframe")
				}
			},
		},
		{
			name:  "frame IDs wrap",
			steps: [][]dlnet.VideoFragment{{frag(65534, 0, 1, true, "k"), frag(65535, 0, 1, false, "p"), frag(0, 0, 1, false, "p"), frag(1, 0, 1, false, "p")}},
			want:  [][]uint16{{65534, 65535, 0, 1}},
		},
		{
			name:  "a forced keyframe drops until the next one",
			steps: [][]dlnet.VideoFragment{{frag(1, 0, 1, true, "k")}, nil, {frag(2, 0, 1, false, "p")}, {frag(3, 0, 1, true, "k")}},
			want:  [][]uint16{{1}, {}, {}, {3}},
		},
		{
			name:  "ignores fragments of delivered frames",
			steps: [][]dlnet.VideoFragment{{frag(1, 0, 1, true, "k"), frag(2, 0, 1, false, "p")}, {frag(1, 0, 1, true, "k")}},
			want:  [][]uint16{{1, 2}, {}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := NewFrameAssembler()
			for i, step := range c.steps {
				if step == nil {
					a.ForceKeyframe()
				}
				if got := feed(a, step...); !reflect.DeepEqual(got, c.want[i]) {
					t.Errorf("step %d: frames %v, want %v", i, got, c.want[i])
				}
			}
			if c.check != nil {
				c.check(t, a)
			}
		})
	}
	t.Run("reassembles fragments in index order", func(t *testing.T) {
		a := NewFrameAssembler()
		var out []EncodedFrame
		for _, f := range []dlnet.VideoFragment{frag(5, 2, 3, true, "c"), frag(5, 0, 3, true, "a"), frag(5, 1, 3, true, "b")} {
			out = append(out, a.Add(f)...)
		}
		if len(out) != 1 || string(out[0].Data) != "abc" {
			t.Fatalf("frames %+v", out)
		}
	})
}

func TestDistance(t *testing.T) {
	cases := []struct {
		a, b uint16
		want int
	}{{0, 65535, 1}, {65535, 0, -1}, {20, 10, 10}}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d-%d", c.a, c.b), func(t *testing.T) {
			if got := distance(c.a, c.b); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestLossMeter(t *testing.T) {
	t.Run("an empty window reports zero", func(t *testing.T) {
		var m LossMeter
		if got := m.Sample(); got != 0 {
			t.Errorf("got %d", got)
		}
	})
	var m LossMeter
	for _, s := range []uint32{1, 2, 4, 5} {
		m.Observe(s)
	}
	if got := m.Sample(); got != 2000 {
		t.Errorf("first window %d, want 2000", got)
	}
	for s := uint32(6); s <= 9; s++ {
		m.Observe(s)
	}
	if got := m.Sample(); got != 0 {
		t.Errorf("second window %d, want 0", got)
	}
}

func TestContentRectAndNormalize(t *testing.T) {
	cases := []struct {
		name         string
		widget       [2]float64
		px, py       float64
		wantX, wantY int32
	}{
		{"same aspect", [2]float64{1280, 800}, 640, 400, AbsMax / 2, AbsMax / 2},
		{"pillarboxed: left border clamps to 0", [2]float64{1600, 800}, 100, 400, 0, AbsMax / 2},
		{"letterboxed: centre of the picture", [2]float64{1280, 1000}, 640, 500, AbsMax / 2, AbsMax / 2},
		{"letterboxed: top of the picture", [2]float64{1280, 1000}, 0, 100, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ContentRect(c.widget[0], c.widget[1], 2560, 1600)
			x, y := Normalize(c.px, c.py, r)
			if diff(x, c.wantX) > 1 || diff(y, c.wantY) > 1 {
				t.Errorf("(%v,%v) in %+v → (%d,%d), want (%d,%d)", c.px, c.py, r, x, y, c.wantX, c.wantY)
			}
		})
	}
}

func diff(a, b int32) int32 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestButtonsAndKeys(t *testing.T) {
	buttons := map[uint]uint16{1: ButtonLeft, 2: ButtonMiddle, 3: ButtonRight, 8: ButtonSide, 9: ButtonExtra}
	for gdk, want := range buttons {
		t.Run(fmt.Sprintf("button %d", gdk), func(t *testing.T) {
			if got, ok := ButtonCode(gdk); !ok || got != want {
				t.Errorf("got %d %v, want %d", got, ok, want)
			}
		})
	}
	keys := []struct {
		keycode uint
		want    uint16
		ok      bool
	}{{9, 1, true}, {38, 30, true}, {133, 125, true}, {8, 0, false}, {300, 0, false}}
	for _, k := range keys {
		t.Run(fmt.Sprintf("keycode %d", k.keycode), func(t *testing.T) {
			if got, ok := EvdevFromKeycode(k.keycode); got != k.want || ok != k.ok {
				t.Errorf("got %d %v", got, ok)
			}
		})
	}
}

func TestScrollAccumulator(t *testing.T) {
	cases := []struct {
		name      string
		dx, dy    float64
		surface   bool
		wantH     int32
		wantWheel int32
	}{
		{"one wheel notch down", 0, 1, false, 0, -120},
		{"one notch right", 1, 0, false, 120, 0},
		{"20 touchpad pixels up", 0, -20, true, 0, 60},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var a ScrollAccumulator
			h, w := a.Add(c.dx, c.dy, c.surface)
			if h != c.wantH || w != c.wantWheel {
				t.Errorf("got (%d,%d), want (%d,%d)", h, w, c.wantH, c.wantWheel)
			}
		})
	}
	t.Run("fractions carry over", func(t *testing.T) {
		var a ScrollAccumulator
		_, w1 := a.Add(0, -0.1, true)
		_, w2 := a.Add(0, -0.3, true)
		if w1 != 0 || w2 != 1 {
			t.Errorf("got %d then %d, want 0 then 1", w1, w2)
		}
	})
}

func TestKeyTracker(t *testing.T) {
	var k KeyTracker
	if k.Press(29) {
		t.Error("first press reported as repeat")
	}
	if !k.Press(29) {
		t.Error("second press not reported as repeat")
	}
	k.Press(30)
	k.Release(30)
	up := k.ReleaseAll()
	if len(up) != 1 || up[0].Code != 29 || up[0].Flags != 0 {
		t.Errorf("release %+v", up)
	}
	if len(k.ReleaseAll()) != 0 {
		t.Error("released twice")
	}
}

func TestHeldInput(t *testing.T) {
	var h heldInput
	if got := h.snapshot(); len(got) != 1 || got[0].Flags != heldNone {
		t.Errorf("empty snapshot %+v", got)
	}
	h.observe(dlnet.InputEvent{Kind: dlnet.InputKey, Flags: 1, Code: 29})
	h.observe(dlnet.InputEvent{Kind: dlnet.InputMouseButton, Flags: 1, Code: ButtonLeft})
	h.observe(dlnet.InputEvent{Kind: dlnet.InputKey, Flags: 1, Code: 46})
	h.observe(dlnet.InputEvent{Kind: dlnet.InputKey, Flags: 0, Code: 46})
	want := []dlnet.InputEvent{
		{Kind: dlnet.InputHeld, Flags: heldKey, Code: 29},
		{Kind: dlnet.InputHeld, Flags: heldButton, Code: ButtonLeft},
	}
	if got := h.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot %+v, want %+v", got, want)
	}
	release := []dlnet.InputEvent{
		{Kind: dlnet.InputKey, Code: 29},
		{Kind: dlnet.InputMouseButton, Code: ButtonLeft},
	}
	if got := h.releaseAll(); !reflect.DeepEqual(got, release) {
		t.Errorf("release %+v, want %+v", got, release)
	}
	if got := h.snapshot(); len(got) != 1 || got[0].Flags != heldNone {
		t.Errorf("snapshot after release %+v, want none held", got)
	}
}

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if hosts := s.Hosts(); len(hosts) != 1 || hosts[0].IP != DefaultIP {
		t.Fatalf("default hosts %+v", hosts)
	}
	p, err := s.Add("Office", "10.0.0.5", 9443)
	if err != nil {
		t.Fatal(err)
	}
	p.PeerID, p.Fingerprint, p.PeerKey = "peer", "fp", []byte{1, 2}
	if err := s.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPreset("office"); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.FindByAddress("10.0.0.5", 9443)
	if !ok || !got.Paired() || !bytes.Equal(got.PeerKey, []byte{1, 2}) {
		t.Errorf("reloaded %+v", got)
	}
	if Presets[0].ID != "auto" || !Presets[0].Auto {
		t.Error("auto must be the first, default preset")
	}
	if reopened.Preset().ID != "office" {
		t.Errorf("preset %q", reopened.Preset().ID)
	}
	level := Level{Width: 1920, Height: 1200, FPS: 60}
	if err := reopened.SetLevel(p.ID, "wg0", level); err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.Host(p.ID); !ok || got.Levels["wg0"] != level {
		t.Errorf("level not remembered: %+v", got)
	}
	if err := reopened.SetCodec("hevc"); err != nil || reopened.Codec() != "hevc" {
		t.Errorf("codec %q, %v", reopened.Codec(), err)
	}
	if err := reopened.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Host(p.ID); ok {
		t.Error("host still saved after delete")
	}
}

func TestStoreSharedBetweenProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	gui, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := gui.SetPreset("balanced"); err != nil {
		t.Fatal(err)
	}
	cli, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Add("Paired by CLI", "10.0.0.9", 9443); err != nil {
		t.Fatal(err)
	}
	if err := gui.SetCodec("av1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := gui.FindByAddress("10.0.0.9", 9443); !ok {
		t.Error("the GUI's save dropped the host the CLI added")
	}
	if cli.Preset().ID != "balanced" || cli.Codec() != "av1" {
		t.Errorf("the CLI sees preset %q codec %q", cli.Preset().ID, cli.Codec())
	}
}

func TestPresetByID(t *testing.T) {
	if PresetByID("office").QP != 24 || PresetByID("unknown").ID != "auto" {
		t.Error("PresetByID did not find the preset or fall back to auto")
	}
}

func TestNetworkKey(t *testing.T) {
	if got := NetworkKey("127.0.0.1"); got != "lo" {
		t.Errorf("loopback network %q, want lo", got)
	}
}

func TestParsePairingCode(t *testing.T) {
	cases := []struct {
		name string
		text string
		ok   bool
	}{
		{"valid", `{"ip":"192.168.178.11","port":9443,"pin":"123456","fingerprint":"ab"}`, true},
		{"surrounding whitespace", "\n {\"ip\":\"1.2.3.4\",\"port\":1,\"pin\":\"000000\",\"fingerprint\":\"\"} \n", true},
		{"short PIN", `{"ip":"1.2.3.4","port":1,"pin":"12"}`, false},
		{"not JSON", "123456", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParsePairingCode(c.text); (err == nil) != c.ok {
				t.Errorf("err %v, want ok=%v", err, c.ok)
			}
		})
	}
}

// fakeHost runs the host's real control and media servers with a session
// handler that answers hello, streams frames and records input.
type fakeHost struct {
	// silent, when set, is the UDP socket the session description points
	// the client at instead of the media server: nothing ever answers.
	silent  *stdnet.UDPConn
	addr    netip.AddrPort
	fp      string
	pairer  *crypto.Pairer
	media   *dlnet.MediaServer
	input   chan []dlnet.InputEvent
	control chan dlnet.Message
	peers   *memPeers
}

type memPeers struct {
	mu    sync.Mutex
	peers map[string]config.Peer
}

func (m *memPeers) Get(id string) (config.Peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.peers[id]
	return p, ok
}

func (m *memPeers) Add(p config.Peer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peers[p.ID] = p
	return nil
}

func (m *memPeers) forget(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.peers, id)
	return nil
}

func (h *fakeHost) HandleInput(_ uint32, e []dlnet.InputEvent) {
	var real []dlnet.InputEvent
	for _, ev := range e {
		if ev.Kind != dlnet.InputHeld {
			real = append(real, ev)
		}
	}
	if len(real) > 0 {
		h.input <- real
	}
}
func (h *fakeHost) HandleTelemetry(dlnet.Telemetry) {}
func (h *fakeHost) HandleRTT(time.Duration)         {}

func (h *fakeHost) ServeSession(ctx context.Context, conn *dlnet.ControlConn) {
	msg, err := conn.Receive(ctx)
	if err != nil || msg.Type != dlnet.MsgHello {
		return
	}
	var hello dlnet.Hello
	if err := json.Unmarshal(msg.Payload, &hello); err != nil {
		return
	}
	keys, err := crypto.DeriveSessionKeys(conn.Peer.Key, 42, fakeNonce)
	if err != nil {
		return
	}
	port := h.media.Port()
	var sess *dlnet.MediaSession
	if h.silent != nil {
		port = h.silent.LocalAddr().(*stdnet.UDPAddr).Port
	} else {
		if sess, err = h.media.Open(42, keys, conn.RemoteIP, h); err != nil {
			return
		}
		defer sess.Close()
	}
	info := dlnet.SessionInfo{SessionID: 42, UDPPort: port, HostNonce: fakeNonce, Codec: hello.Codec, Width: hello.Width, Height: hello.Height}
	if err := conn.Send(ctx, dlnet.MsgSession, info); err != nil {
		return
	}
	if err := conn.Send(ctx, dlnet.MsgApps, []App{{ID: "bunk-gui", Name: "Bunk Terminal", Exec: "bunk-gui"}}); err != nil {
		return
	}
	if sess != nil {
		go func() {
			frame := bytes.Repeat([]byte("frame"), 1000)
			for ctx.Err() == nil {
				if err := sess.SendFrame(frame, true, time.Now()); err != nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	for {
		m, err := conn.Receive(ctx)
		if err != nil {
			return
		}
		h.control <- m
	}
}

// fakeNonce is the host nonce of every fake session.
var fakeNonce = bytes.Repeat([]byte{7}, 16)

func startFakeHost(t *testing.T) *fakeHost { return startFakeHostWith(t, nil) }

// startFakeHostWith starts a fake host whose sessions point the client at
// silent instead of the media server, when it is not nil.
func startFakeHostWith(t *testing.T, silent *stdnet.UDPConn) *fakeHost {
	t.Helper()
	dir := t.TempDir()
	tlsCfg, fp, err := dlnet.LoadOrCreateCert(filepath.Join(dir, "c"), filepath.Join(dir, "k"))
	if err != nil {
		t.Fatal(err)
	}
	allow := func(netip.Addr) bool { return true }
	media, err := dlnet.ListenMedia("127.0.0.1:0", allow)
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{silent: silent, fp: fp, pairer: crypto.NewPairer(fp, time.Now), media: media, input: make(chan []dlnet.InputEvent, 8), control: make(chan dlnet.Message, 8),
		peers: &memPeers{peers: map[string]config.Peer{}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if err := media.Close(); err != nil {
			t.Log(err)
		}
	})
	go func() {
		if err := media.Serve(ctx); err != nil {
			t.Log(err)
		}
	}()
	srv := &dlnet.ControlServer{
		Addr: "127.0.0.1:0", TLS: tlsCfg, Peers: h.peers, Pairer: h.pairer,
		Sessions: h, Allow: allow, Health: func() dlnet.Health { return dlnet.Health{Name: "desklinkd", TimeMs: time.Now().UnixMilli()} },
		Forget: h.peers.forget,
	}
	bound := make(chan stdnet.Addr, 1)
	go func() {
		if err := srv.Serve(ctx, func(a stdnet.Addr) { bound <- a }); err != nil {
			t.Log(err)
		}
	}()
	select {
	case a := <-bound:
		h.addr = netip.MustParseAddrPort(a.String())
	case <-time.After(2 * time.Second):
		t.Fatal("control server did not start")
	}
	return h
}

type recordedEvents struct {
	info   chan dlnet.SessionInfo
	ready  chan struct{}
	quiet  chan string
	apps   chan []App
	closed chan error
}

func newRecordedEvents() *recordedEvents {
	return &recordedEvents{
		info: make(chan dlnet.SessionInfo, 2), ready: make(chan struct{}, 4), quiet: make(chan string, 4),
		apps: make(chan []App, 2), closed: make(chan error, 1),
	}
}

// record hands v to ch without blocking a network goroutine when the test
// does not read ch.
func record[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
	}
}

func (r *recordedEvents) SessionInfo(i dlnet.SessionInfo) { record(r.info, i) }
func (r *recordedEvents) MediaReady()                     { record(r.ready, struct{}{}) }
func (r *recordedEvents) MediaQuiet(m string)             { record(r.quiet, m) }
func (r *recordedEvents) Apps(a []App)                    { record(r.apps, a) }
func (r *recordedEvents) Clipboard(string)                {}
func (r *recordedEvents) Notice(string)                   {}
func (r *recordedEvents) Closed(err error)                { record(r.closed, err) }

func TestPairAndStreamAgainstHostServers(t *testing.T) {
	h := startFakeHost(t)
	ip, port := h.addr.Addr().String(), int(h.addr.Port())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pin, _, err := h.pairer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a wrong fingerprint refuses to pair", func(t *testing.T) {
		if _, err := Pair(ctx, ip, port, pin, "00", "laptop"); !errors.Is(err, ErrFingerprintMismatch) {
			t.Errorf("err %v, want ErrFingerprintMismatch", err)
		}
	})
	res, err := Pair(ctx, ip, port, pin, h.fp, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if res.Fingerprint != h.fp || len(res.PeerKey) != crypto.KeySize {
		t.Fatalf("pair result %+v", res)
	}
	profile := Profile{ID: "x", IP: ip, Port: port, PeerID: res.PeerID, Fingerprint: res.Fingerprint, PeerKey: res.PeerKey}

	frames := make(chan EncodedFrame, 16)
	ev := newRecordedEvents()
	s, err := Connect(ctx, profile, Request{Codec: "av1", Width: 1920, Height: 1200, FPS: 60, QP: 20}, func(f EncodedFrame) {
		select {
		case frames <- f:
		default:
		}
	}, ev)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if info := wait(t, ev.info); info.Width != 1920 || info.Codec != "av1" {
		t.Errorf("session info %+v", info)
	}
	// The host's first datagram, not the session description, makes the
	// media path ready.
	wait(t, ev.ready)
	if apps := wait(t, ev.apps); len(apps) != 1 || apps[0].ID != "bunk-gui" {
		t.Errorf("apps %+v", apps)
	}
	if f := wait(t, frames); !f.Keyframe || len(f.Data) != 5000 {
		t.Errorf("frame %d bytes, keyframe %v", len(f.Data), f.Keyframe)
	}
	s.SendInput(dlnet.InputEvent{Kind: dlnet.InputKey, Flags: 1, Code: 30})
	if got := wait(t, h.input); len(got) != 1 || got[0].Code != 30 {
		t.Errorf("host input %+v", got)
	}
	if s.Info().Width != 1920 {
		t.Errorf("Info %+v", s.Info())
	}
	t.Run("control requests reach the host", func(t *testing.T) {
		calls := []struct {
			want string
			call func() error
		}{
			{dlnet.MsgLaunch, func() error { return s.Launch(App{ID: "bunk-gui", Exec: "bunk-gui"}) }},
			{dlnet.MsgClipboard, func() error { return s.PushClipboard("hello") }},
			{dlnet.MsgClipboardRequest, s.RequestClipboard},
		}
		for _, c := range calls {
			if err := c.call(); err != nil {
				t.Fatal(err)
			}
			if m := wait(t, h.control); m.Type != c.want {
				t.Errorf("host got %q, want %q", m.Type, c.want)
			}
		}
	})
	t.Run("an oversized clipboard is refused before sending", func(t *testing.T) {
		if err := s.PushClipboard(string(make([]byte, MaxClipboardBytes+1))); !errors.Is(err, ErrClipboardTooLarge) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("stats count received frames", func(t *testing.T) {
		time.Sleep(1200 * time.Millisecond)
		if st := s.Stats(); st.FPS == 0 || st.Mbps == 0 {
			t.Errorf("stats %+v", st)
		}
	})
	s.Close()
	if err := wait(t, ev.closed); err != nil {
		t.Errorf("close error %v", err)
	}
}

func wait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	var zero T
	return zero
}

// fecFrame is a frame of n fragments with distinct, uneven data, and its
// parity packets as the host computes them.
func fecFrame(id uint16, n int, key bool) ([]dlnet.VideoFragment, []dlnet.ParityPacket) {
	frags := make([]dlnet.VideoFragment, n)
	for i := range frags {
		data := strings.Repeat(string(rune('a'+i%26)), 10+i*7)
		frags[i] = dlnet.VideoFragment{FrameID: id, Index: uint16(i), Count: uint16(n), Keyframe: key, CaptureMicros: 4242, Data: []byte(data)}
	}
	return frags, dlnet.ParityFor(frags)
}

func joined(frags []dlnet.VideoFragment) string {
	var b strings.Builder
	for _, f := range frags {
		b.Write(f.Data)
	}
	return b.String()
}

// The same cases as the parity tests in FrameAssemblerTest.kt.
func TestFrameAssemblerParity(t *testing.T) {
	t.Run("a lost fragment is rebuilt from its group", func(t *testing.T) {
		a := NewFrameAssembler()
		frags, parity := fecFrame(1, 10, true) // two groups: even and odd indices
		var got []EncodedFrame
		for i, f := range frags {
			if i != 3 {
				got = append(got, a.Add(f)...)
			}
		}
		for _, q := range parity {
			got = append(got, a.AddParity(q)...)
		}
		if len(got) != 1 || string(got[0].Data) != joined(frags) || !got[0].Keyframe || got[0].CaptureMicros != 4242 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a burst as long as the stride is rebuilt", func(t *testing.T) {
		a := NewFrameAssembler()
		frags, parity := fecFrame(1, 10, true) // stride 2: fragments 4 and 5 are in different groups
		var got []EncodedFrame
		for _, q := range parity {
			got = append(got, a.AddParity(q)...)
		}
		for i, f := range frags {
			if i != 4 && i != 5 {
				got = append(got, a.Add(f)...)
			}
		}
		if len(got) != 1 || string(got[0].Data) != joined(frags) {
			t.Fatalf("got %d frames", len(got))
		}
	})
	t.Run("a single-fragment frame comes back from parity alone", func(t *testing.T) {
		a := NewFrameAssembler()
		_, parity := fecFrame(1, 1, true)
		got := a.AddParity(parity[0])
		if len(got) != 1 || !got[0].Keyframe || got[0].CaptureMicros != 4242 || string(got[0].Data) != "aaaaaaaaaa" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a repeated parity record is kept once", func(t *testing.T) {
		a := NewFrameAssembler()
		frags, parity := fecFrame(1, 10, true) // stride 2
		var got []EncodedFrame
		for range 3 {
			got = append(got, a.AddParity(parity[0])...)
		}
		if p := a.partial[1]; p == nil || len(p.parity) != 1 {
			t.Fatalf("partial frame %+v, want one parity record", p)
		}
		for i, f := range frags {
			if i != 4 {
				got = append(got, a.Add(f)...)
			}
		}
		if len(got) != 1 || string(got[0].Data) != joined(frags) {
			t.Fatalf("got %d frames", len(got))
		}
	})
	t.Run("two losses in one group stay lost", func(t *testing.T) {
		a := NewFrameAssembler()
		frags, parity := fecFrame(1, 10, true)
		var got []EncodedFrame
		for i, f := range frags {
			if i != 2 && i != 4 { // both even: the same group
				got = append(got, a.Add(f)...)
			}
		}
		for _, q := range parity {
			got = append(got, a.AddParity(q)...)
		}
		if len(got) != 0 {
			t.Fatalf("got %d frames from an unrecoverable loss", len(got))
		}
	})
}

func TestForgetRemovesThePairingOnTheHost(t *testing.T) {
	h := startFakeHost(t)
	ip, port := h.addr.Addr().String(), int(h.addr.Port())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pin, _, err := h.pairer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Pair(ctx, ip, port, pin, h.fp, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	p := Profile{IP: ip, Port: port, PeerID: res.PeerID, PeerKey: res.PeerKey, Fingerprint: res.Fingerprint}
	if err := Forget(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.peers.Get(res.PeerID); ok {
		t.Error("the host still has the pairing")
	}
	// The host answers 401 to a device it no longer knows, but also to a
	// wrong token, so a 401 cannot confirm the removal.
	if err := Forget(ctx, p); !errors.Is(err, ErrForgetUnconfirmed) {
		t.Errorf("forgetting twice: err %v, want ErrForgetUnconfirmed", err)
	}
	if _, err := Connect(ctx, p, Request{Codec: "av1"}, func(EncodedFrame) {}, newRecordedEvents()); err == nil {
		t.Error("connected after being forgotten")
	}
}
