package daemon

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	dlnet "desklink/internal/net"
	"desklink/internal/tiles"
	"desklink/internal/video"
)

// tileWire collects the binary messages a session sends.
type tileWire struct {
	mu     sync.Mutex
	frames []dlnet.TileFrame
	tiles  []dlnet.DecodedTile
}

func (w *tileWire) send(_ context.Context, msg []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch msg[0] {
	case dlnet.TileKindFrame:
		f, err := dlnet.ParseTileFrame(msg)
		if err != nil {
			return err
		}
		w.frames = append(w.frames, f)
	case dlnet.TileKindData:
		_, ts, err := dlnet.ParseTileData(msg)
		if err != nil {
			return err
		}
		w.tiles = append(w.tiles, ts...)
	}
	return nil
}

func (w *tileWire) counts() (frames, tiles int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.frames), len(w.tiles)
}

// TestASessionSendsTheExactPixelsOfASettledScreen drives the session's
// tiles as the pipeline does, with a scan and an encoded frame for each
// capture: the client gets a tile_frame for every frame and, once the screen
// stood still for the settle delay, every tile's exact pixels.
func TestASessionSendsTheExactPixelsOfASettledScreen(t *testing.T) {
	const w, h = 128, 64
	wire := &tileWire{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := newSessionTiles(ctx, wire.send, w, h, nil)
	pix := make([]byte, w*h*4)
	// A dark background with a light stroke in every fifth column, as text
	// gives; a photograph would not be sent at all.
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2] = 49, 31, 36
		if i/4%5 == 0 {
			pix[i], pix[i+1], pix[i+2] = 222, byte(i/4%w), 200
		}
	}
	pts := time.Second
	frame := func() {
		pts += 100 * time.Millisecond
		st.scan(video.ScanFrame{PTS: pts, Width: w, Height: h, Stride: w * 4, Pix: pix})
		st.encoded(video.Frame{PTS: pts, Captured: time.UnixMicro(int64(pts / time.Microsecond))})
	}
	frame()
	time.Sleep(tiles.SettleDelay)
	frame()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, n := wire.counts(); n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if len(wire.frames) != 2 || !wire.frames[0].All || wire.frames[1].CaptureMicros != uint64(pts/time.Microsecond) {
		t.Fatalf("tile_frame messages: %+v", wire.frames)
	}
	if len(wire.tiles) != 2 {
		t.Fatalf("%d tiles arrived, want 2", len(wire.tiles))
	}
	for _, tile := range wire.tiles {
		if tile.Seq != 2 {
			t.Errorf("tile %d is for frame %d, want 2", tile.Slot, tile.Seq)
		}
		x0 := int(tile.Slot) * 64
		for y := range h {
			for x := range 64 {
				i := (y*w + x0 + x) * 4
				j := (y*64 + x) * 3
				if tile.RGB[j] != pix[i+2] || tile.RGB[j+1] != pix[i+1] || tile.RGB[j+2] != pix[i] {
					t.Fatalf("tile %d pixel (%d,%d) differs from the capture", tile.Slot, x, y)
				}
			}
		}
	}
}

func TestASessionWithTilesOffSendsNothing(t *testing.T) {
	wire := &tileWire{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := newSessionTiles(ctx, wire.send, 64, 64, nil)
	st.on.Store(false)
	pix := make([]byte, 64*64*4)
	st.scan(video.ScanFrame{PTS: time.Second, Width: 64, Height: 64, Stride: 256, Pix: pix})
	st.encoded(video.Frame{PTS: time.Second, Captured: time.Now()})
	time.Sleep(20 * time.Millisecond)
	if frames, n := wire.counts(); frames != 0 || n != 0 {
		t.Errorf("tiles turned off sent %d tile_frame and %d tiles", frames, n)
	}
}

// udpWire collects the frames a session sends with their tile packets.
type udpWire struct {
	mu      sync.Mutex
	frames  []time.Duration
	packets [][]dlnet.TileFrame
	// refuse makes sendFrame report the frame as not sent.
	refuse bool
}

func (u *udpWire) sendFrame(f video.Frame, packet []byte) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	var records []dlnet.TileFrame
	if packet != nil {
		var err error
		if records, err = dlnet.ParseTilePacket(packet); err != nil {
			panic(err)
		}
	}
	u.frames = append(u.frames, f.PTS)
	u.packets = append(u.packets, records)
	return !u.refuse
}

func (u *udpWire) sent() ([]time.Duration, [][]dlnet.TileFrame) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.frames), slices.Clone(u.packets)
}

func (u *udpWire) setRefuse(refuse bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.refuse = refuse
}

// udpSession is a session's tiles whose invalidations go over UDP, fed as
// the pipeline feeds them: each frame encoded, then scanned.
type udpSession struct {
	st  *sessionTiles
	udp *udpWire
	pix []byte
	pts time.Duration
}

const udpW, udpH = 128, 64

func newUDPSession(t *testing.T) *udpSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	u := &udpSession{udp: &udpWire{}, pix: make([]byte, udpW*udpH*4), pts: time.Second}
	for i := 0; i < len(u.pix); i += 4 {
		u.pix[i], u.pix[i+1], u.pix[i+2] = 49, 31, 36
		if i/4%5 == 0 {
			u.pix[i], u.pix[i+1], u.pix[i+2] = 222, byte(i/4%udpW), 200
		}
	}
	u.st = newSessionTiles(ctx, (&tileWire{}).send, udpW, udpH, u.udp.sendFrame)
	return u
}

func (u *udpSession) encode() {
	u.pts += 10 * time.Millisecond
	u.st.encoded(video.Frame{PTS: u.pts, Captured: time.UnixMicro(int64(u.pts / time.Microsecond))})
}

func (u *udpSession) scan() {
	u.st.scan(video.ScanFrame{PTS: u.pts, Width: udpW, Height: udpH, Stride: udpW * 4, Pix: u.pix})
}

// settle encodes and scans one frame, waits out the settle delay and does it
// again, so the client may have every tile.
func (u *udpSession) settle() {
	u.encode()
	u.scan()
	time.Sleep(tiles.SettleDelay)
	u.encode()
	u.scan()
}

func TestAFrameGoesOverUDPOnlyOnceItsScanShowsWhatItChanged(t *testing.T) {
	u := newUDPSession(t)
	u.settle()
	if frames, _ := u.udp.sent(); len(frames) != 2 {
		t.Fatalf("%d frames sent before the client could have a tile, want both at once", len(frames))
	}
	u.pix[0] = 1 // a change in tile 0
	u.encode()
	if frames, _ := u.udp.sent(); len(frames) != 2 {
		t.Fatal("a frame went out before its scan, while the client may have tiles")
	}
	u.scan()
	frames, packets := u.udp.sent()
	if len(frames) != 3 || frames[2] != u.pts {
		t.Fatalf("frames sent: %v", frames)
	}
	got := packets[2]
	if len(got) != 3 || got[0].Seq != 3 || got[0].All || !slices.Equal(got[0].Changed, []uint16{0}) {
		t.Fatalf("the frame's packet: %+v", got)
	}
	if got[1].Seq != 2 || got[2].Seq != 1 || !got[2].All {
		t.Errorf("the packet does not repeat the invalidations before it: %+v", got[1:])
	}
}

func TestFramesKeepTheirOrderWhenTilesAreTurnedOffWhileOneWaits(t *testing.T) {
	u := newUDPSession(t)
	u.settle()
	u.encode() // waits for a scan that will not come
	waiting := u.pts
	u.st.setOn(false, udpW, udpH)
	u.encode()
	frames, packets := u.udp.sent()
	if len(frames) != 4 || frames[2] != waiting || frames[3] != u.pts {
		t.Fatalf("frames sent: %v", frames)
	}
	if len(packets[2]) == 0 || !packets[2][0].All {
		t.Errorf("the frame judged as the tiles stopped: %+v", packets[2])
	}
	if packets[3] != nil {
		t.Errorf("a frame after the tiles stopped carried %+v", packets[3])
	}
}

func TestAFrameThatCouldNotBeSentMakesTheNextInvalidateEveryTile(t *testing.T) {
	u := newUDPSession(t)
	u.encode()
	u.scan()
	u.udp.setRefuse(true) // the client's first datagram has not arrived
	u.encode()
	u.scan()
	u.udp.setRefuse(false)
	u.encode()
	u.scan()
	_, packets := u.udp.sent()
	if len(packets) != 3 || packets[1][0].All || !packets[2][0].All {
		t.Fatalf("invalidations: %+v", packets)
	}
}
