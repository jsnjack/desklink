package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	dlnet "desklink/internal/net"
	"desklink/internal/tiles"
	"desklink/internal/video"
)

// sessionTiles sends one session's exact tiles (docs/desktop.md, "Exact
// tiles"): the tracker judges the scan branch's frames against the encoded
// ones, the pacer sends settled tiles within its budget and the writer puts
// them on the WebSocket in order. Each frame's invalidation goes through
// the writer as a tile_frame message after the frame, or, to a client that
// takes it over UDP, in PacketTiles datagrams around the frame itself; the
// frame then waits until it is judged, which is when its scan is done
// unless the client may have no tile.
type sessionTiles struct {
	tracker *tiles.Tracker
	pacer   *tiles.Pacer
	writer  *tiles.Writer
	// on is set while the client wants tiles; the pipeline's callbacks read
	// it.
	on atomic.Bool

	// sendFrame sends a frame with the PacketTiles plaintext of its
	// invalidation (nil for none) and reports whether it went out. It is
	// nil when the client takes tile_frame messages; the session then sends
	// its frames itself.
	sendFrame func(f video.Frame, packet []byte) bool
	// feed keeps the tiles from being turned on or off between a frame
	// joining the outbox and the tracker getting it, which would leave the
	// frame waiting for a judgement that never comes.
	feed sync.Mutex
	// mu guards outbox and recent; the tracker's lock may be held when it
	// is taken, never the other way round.
	mu sync.Mutex
	// outbox holds the encoded frames not yet sent, oldest first.
	outbox []*heldFrame
	// recent are the latest invalidations, newest first; each packet
	// repeats the ones before its frame's.
	recent []dlnet.TileFrame
	// sending keeps frames in order when two goroutines flush at once.
	sending sync.Mutex
}

// heldFrame is an encoded frame in the outbox.
type heldFrame struct {
	f video.Frame
	// packet is the PacketTiles plaintext, nil for a frame sent while tiles
	// are off.
	packet []byte
	ready  bool
}

// newSessionTiles starts the writer, which sends with send (the WebSocket's
// SendBinary), and the pacer for a stream of width × height; they stop with
// ctx. A write that fails ends only the tiles: the session notices a broken
// WebSocket on its own. sendFrame, when not nil, sends each frame with its
// invalidation over UDP (sessionTiles.sendFrame).
func newSessionTiles(ctx context.Context, send func(context.Context, []byte) error, width, height int, sendFrame func(video.Frame, []byte) bool) *sessionTiles {
	st := &sessionTiles{sendFrame: sendFrame}
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

// udp reports whether frames go out with their invalidation over UDP, so
// the session hands every frame to encoded instead of sending it.
func (st *sessionTiles) udp() bool { return st.sendFrame != nil }

// Judged implements tiles.Output: the invalidation goes to the writer as a
// tile_frame, or into the packet of the frame it belongs to, ref.
func (st *sessionTiles) Judged(f dlnet.TileFrame, ref any) {
	if !st.udp() {
		st.writer.Send(f.Marshal())
		return
	}
	if f.All {
		st.writer.AllInvalidated()
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.recent = append([]dlnet.TileFrame{f}, st.recent[:min(len(st.recent), dlnet.TilePacketRecords-1)]...)
	packet, fits := dlnet.AppendTilePacket(nil, st.recent)
	if !fits {
		st.tracker.RequestReset()
	}
	if h, ok := ref.(*heldFrame); ok {
		h.packet, h.ready = packet, true
	}
}

// Send implements tiles.Output.
func (st *sessionTiles) Send(msg []byte) { st.writer.Send(msg) }

// Captured implements tiles.Output.
func (st *sessionTiles) Captured(c tiles.Capture) { st.pacer.Add(c) }

// restart starts the tiles over for a stream of width × height: the next
// judged frame tells the client that every tile it holds is invalid. Frames
// waiting for their scan go out first, judged without one.
func (st *sessionTiles) restart(width, height int) {
	st.feed.Lock()
	st.pacer.Clear()
	st.tracker.Restart(dlnet.TileGrid{Width: width, Height: height})
	st.feed.Unlock()
	st.flush()
}

// setOn turns the tiles on or off and starts them over.
func (st *sessionTiles) setOn(on bool, width, height int) {
	st.feed.Lock()
	st.on.Store(on)
	st.pacer.Clear()
	st.tracker.Restart(dlnet.TileGrid{Width: width, Height: height})
	st.feed.Unlock()
	st.flush()
}

// scan takes a frame of the pipeline's scan branch.
func (st *sessionTiles) scan(f video.ScanFrame) {
	if !st.on.Load() {
		return
	}
	st.tracker.Scan(tiles.Frame{PTS: f.PTS, Pix: f.Pix, Stride: f.Stride}, time.Now())
	st.flush()
}

// encoded takes a frame the encoder produced: one the session sent, or,
// with invalidations over UDP, one to send once it is judged.
func (st *sessionTiles) encoded(f video.Frame) {
	micros := uint64(f.Captured.UnixMicro())
	if !st.udp() {
		if st.on.Load() {
			st.tracker.Encoded(f.PTS, micros, nil)
		}
		return
	}
	st.feed.Lock()
	h := &heldFrame{f: f, ready: !st.on.Load()}
	st.mu.Lock()
	st.outbox = append(st.outbox, h)
	st.mu.Unlock()
	if !h.ready {
		st.tracker.Encoded(f.PTS, micros, h)
	}
	st.feed.Unlock()
	st.flush()
}

// flush sends the frames at the head of the outbox that are ready, in
// order. A frame that could not go out (before the client's first datagram)
// took news the client never got, so the next invalidation covers every
// tile; the client starts drawing tiles at such an invalidation.
func (st *sessionTiles) flush() {
	if !st.udp() {
		return
	}
	st.sending.Lock()
	defer st.sending.Unlock()
	for {
		st.mu.Lock()
		if len(st.outbox) == 0 || !st.outbox[0].ready {
			st.mu.Unlock()
			return
		}
		h := st.outbox[0]
		st.outbox[0] = nil
		st.outbox = st.outbox[1:]
		st.mu.Unlock()
		if !st.sendFrame(h.f, h.packet) && h.packet != nil {
			st.tracker.RequestReset()
		}
	}
}

// logTotals logs the counters at the end of the session.
func (st *sessionTiles) logTotals() {
	t, p := st.tracker.Stats(), st.pacer.Stats()
	slog.Info("tile totals", "frames", t.Frames, "unscanned", t.Skipped, "captured", t.Captured, "slowed", t.Slowed,
		"sent", p.Tiles, "messages", p.Messages, "bytes", p.Bytes, "stale", p.Stale, "photos", p.Photos,
		"dropped", st.writer.Dropped(), "udp", st.udp())
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
		s.tiles.setOn(c.On, s.width, s.height)
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

// tilesUDP is SessionInfo.TilesUDP: whether the invalidations go over UDP.
func (s *session) tilesUDP() bool {
	return s.tilesOn() && s.tiles.udp()
}
