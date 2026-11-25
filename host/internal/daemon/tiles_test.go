package daemon

import (
	"context"
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
	st := newSessionTiles(ctx, wire.send, w, h)
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
	st := newSessionTiles(ctx, wire.send, 64, 64)
	st.on.Store(false)
	pix := make([]byte, 64*64*4)
	st.scan(video.ScanFrame{PTS: time.Second, Width: 64, Height: 64, Stride: 256, Pix: pix})
	st.encoded(video.Frame{PTS: time.Second, Captured: time.Now()})
	time.Sleep(20 * time.Millisecond)
	if frames, n := wire.counts(); frames != 0 || n != 0 {
		t.Errorf("tiles turned off sent %d tile_frame and %d tiles", frames, n)
	}
}
