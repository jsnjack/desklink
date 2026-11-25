package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	dlnet "desklink/internal/net"
	"desklink/internal/tiles"
	"desklink/internal/video"
)

// sessionTiles sends one session's exact tiles (docs/desktop.md, "Exact
// tiles"): the tracker judges the scan branch's frames against the encoded
// ones, the pacer sends settled tiles within its budget and the writer puts
// both on the WebSocket in order.
type sessionTiles struct {
	tracker *tiles.Tracker
	pacer   *tiles.Pacer
	writer  *tiles.Writer
	// on is set while the client wants tiles; the pipeline's callbacks read
	// it.
	on atomic.Bool
}

// newSessionTiles starts the writer, which sends with send (the WebSocket's
// SendBinary), and the pacer for a stream of width × height; they stop with
// ctx. A write that fails ends only the tiles: the session notices a broken
// WebSocket on its own.
func newSessionTiles(ctx context.Context, send func(context.Context, []byte) error, width, height int) *sessionTiles {
	st := &sessionTiles{}
	st.writer = tiles.NewWriter(send, func() { st.tracker.RequestReset() })
	st.tracker = tiles.NewTracker(dlnet.TileGrid{Width: width, Height: height}, st)
	st.pacer = tiles.NewPacer(st.tracker, st.writer)
	st.on.Store(true)
	go st.pacer.Run(ctx)
	go func() {
		if err := st.writer.Run(ctx); err != nil {
			slog.Debug("stop sending tiles", "err", err)
		}
	}()
	return st
}

// Send implements tiles.Output.
func (st *sessionTiles) Send(msg []byte) { st.writer.Send(msg) }

// Captured implements tiles.Output.
func (st *sessionTiles) Captured(c tiles.Capture) { st.pacer.Add(c) }

// restart starts the tiles over for a stream of width × height: the next
// tile_frame tells the client that every tile it holds is invalid.
func (st *sessionTiles) restart(width, height int) {
	st.pacer.Clear()
	st.tracker.Restart(dlnet.TileGrid{Width: width, Height: height})
}

// scan takes a frame of the pipeline's scan branch.
func (st *sessionTiles) scan(f video.ScanFrame) {
	if st.on.Load() {
		st.tracker.Scan(tiles.Frame{PTS: f.PTS, Pix: f.Pix, Stride: f.Stride}, time.Now())
	}
}

// encoded takes a frame the encoder produced and the session sent.
func (st *sessionTiles) encoded(f video.Frame) {
	if st.on.Load() {
		st.tracker.Encoded(f.PTS, uint64(f.Captured.UnixMicro()))
	}
}

// logTotals logs the counters at the end of the session.
func (st *sessionTiles) logTotals() {
	t, p := st.tracker.Stats(), st.pacer.Stats()
	slog.Info("tile totals", "frames", t.Frames, "unscanned", t.Skipped, "captured", t.Captured,
		"sent", p.Tiles, "messages", p.Messages, "bytes", p.Bytes, "stale", p.Stale, "photos", p.Photos,
		"dropped", st.writer.Dropped())
}

// setTiles turns exact tiles on or off at the client's request (MsgTiles)
// and answers with the session description. Turning them on again starts
// over, for a client that lost its tiles. A session whose client did not ask
// for tiles in its hello has no scan branch and keeps them off.
func (s *session) setTiles(ctx context.Context, payload json.RawMessage) {
	var c dlnet.TilesChange
	if err := json.Unmarshal(payload, &c); err != nil {
		s.reportError(ctx, fmt.Errorf("decode tiles: %w", err))
		return
	}
	if s.tiles != nil {
		s.tiles.on.Store(c.On)
		s.tiles.restart(s.width, s.height)
		s.encMu.Lock()
		if s.enc != nil {
			s.enc.SetScanning(c.On)
		}
		s.encMu.Unlock()
	}
	s.sendSessionInfo(ctx)
}

// tilesOn is SessionInfo.Tiles: whether the session sends exact tiles.
func (s *session) tilesOn() bool {
	return s.tiles != nil && s.tiles.on.Load()
}
