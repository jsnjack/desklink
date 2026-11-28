package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"

	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

// recordingEncoder is a running video pipeline that records the frame-rate
// caps it is given.
type recordingEncoder struct {
	fakePipeline
	rates []int
}

func (e *recordingEncoder) SetMaxFPS(fps int) { e.rates = append(e.rates, fps) }

// readSessionInfo reads control messages until the next session
// description.
func readSessionInfo(t *testing.T, ws *websocket.Conn) dlnet.SessionInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("no session description: %v", err)
		}
		var m dlnet.Message
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		if m.Type != dlnet.MsgSession {
			continue
		}
		var info dlnet.SessionInfo
		if err := json.Unmarshal(m.Payload, &info); err != nil {
			t.Fatal(err)
		}
		return info
	}
}

// capFixture is a session streaming 2560x1600 at the top of a 120 fps
// auto ladder to a tablet on a real control connection, whose encoder
// records the rates it is given.
func capFixture(t *testing.T) (sessionFixture, *websocket.Conn, *recordingEncoder) {
	t.Helper()
	f := newSessionFixture(t)
	useFakeMutter(f, newFakeMutter())
	f.s.width, f.s.height = 2560, 1600
	ws := connectClient(t, f)
	enc := &recordingEncoder{}
	f.s.enc = enc
	return f, ws, enc
}

// connectClient gives the session a client: a control connection, whose
// other end it returns, and a media session on a local UDP port, so the
// session can send it session descriptions.
func connectClient(t *testing.T, f sessionFixture) *websocket.Conn {
	t.Helper()
	conn, ws := controlPair(t)
	f.s.conn = conn
	media, err := dlnet.ListenMedia("127.0.0.1:0", func(netip.Addr) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = media.Close() })
	f.s.d.media = media
	keys := crypto.SessionKeys{HostToClient: bytes.Repeat([]byte{1}, 32), ClientToHost: bytes.Repeat([]byte{2}, 32)}
	if f.s.media, err = media.Open(1, keys, netip.MustParseAddr("127.0.0.1"), f.s); err != nil {
		t.Fatal(err)
	}
	return ws
}

func maxFPSMessage(fps string) dlnet.Message {
	return dlnet.Message{Type: dlnet.MsgMaxFPS, Payload: json.RawMessage(`{"fps":` + fps + `}`)}
}

// TestAFrameRateCapSlowsTheEncoderAndTheClientHearsIt: a tablet whose
// panel power saving holds at 60 Hz asks for at most 60 fps; the encoder
// runs at that rate while the ladder stays at 120 fps, so the level the
// tablet remembers keeps its meaning, and lifting the cap restores the rate.
func TestAFrameRateCapSlowsTheEncoderAndTheClientHearsIt(t *testing.T) {
	f, ws, enc := capFixture(t)
	capAt := func(fps string) dlnet.SessionInfo {
		f.s.handleMessage(f.s.ctx, maxFPSMessage(fps))
		return readSessionInfo(t, ws)
	}
	info := capAt("60")
	if info.FPS != 60 || info.Level != 0 || info.Levels[0].FPS != 120 {
		t.Errorf("capped: fps %d at level %d of %v, want 60 fps at the 120 fps top level", info.FPS, info.Level, info.Levels)
	}
	info = capAt("0")
	if info.FPS != 120 || info.Level != 0 {
		t.Errorf("lifted: fps %d at level %d, want 120 at the top", info.FPS, info.Level)
	}
	if want := []int{60, 120}; !slices.Equal(enc.rates, want) {
		t.Errorf("the encoder ran at %v, want %v", enc.rates, want)
	}
}

// TestALevelQueuedBeforeTheCapDoesNotUndoIt: the UDP goroutine may queue
// the level the controller chose just before the cap moved it; the session
// loop applies the controller's level as it is by then.
func TestALevelQueuedBeforeTheCapDoesNotUndoIt(t *testing.T) {
	f, ws, enc := capFixture(t)
	f.s.handleMessage(f.s.ctx, maxFPSMessage("60"))
	readSessionInfo(t, ws)
	f.s.levelChanges <- 1 // 60 fps, chosen while the cap held the top level at 60
	f.s.handleMessage(f.s.ctx, maxFPSMessage("0"))
	readSessionInfo(t, ws)
	done := make(chan error, 1)
	go func() { done <- f.s.serve(f.s.ctx, make(chan dlnet.Message)) }()
	info := readSessionInfo(t, ws)
	f.s.cancel(nil)
	if err := <-done; err != nil {
		t.Errorf("serve ended with %v after the client left", err)
	}
	if info.Level != 0 || info.FPS != 120 {
		t.Errorf("after the queued level: level %d at %d fps, want the top level at 120", info.Level, info.FPS)
	}
	if want := []int{60, 120, 120}; !slices.Equal(enc.rates, want) {
		t.Errorf("the encoder ran at %v, want %v", enc.rates, want)
	}
}

func TestFrameRateCapMessages(t *testing.T) {
	cases := []struct {
		payload string
		want    int
		ok      bool
	}{
		{`{"fps":60}`, 60, true},
		{`{"fps":0}`, 0, true},
		{`{"fps":120}`, 120, true},
		{`{"fps":-1}`, 0, false},
		{`{"fps":121}`, 0, false},
		{`{"fps":"60"}`, 0, false},
	}
	for _, c := range cases {
		got, err := frameRateCap(json.RawMessage(c.payload))
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%s: %d, %v; want %d, accepted %v", c.payload, got, err, c.want, c.ok)
		}
	}
}

// TestANewPipelineStartsAtTheCappedRate: a pipeline started for a new size
// or screen under the cap, such as a quality step down, runs at the cap.
func TestANewPipelineStartsAtTheCappedRate(t *testing.T) {
	f, m := ownScreenFixture(t, true)
	f.s.fpsCap = 60
	if err := f.s.startPipeline(f.s.ctx); err != nil {
		t.Fatal(err)
	}
	last := m.pipelines[len(m.pipelines)-1]
	if last.FPS != 60 || last.CaptureFPS != 120 {
		t.Errorf("the pipeline runs at %d fps on a %d Hz capture, want 60 on the own screen's 120", last.FPS, last.CaptureFPS)
	}
}
