package net

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Exact tiles (docs/protocol.md): once part of the screen stops changing,
// the host sends its exact pixels as binary WebSocket messages on the control
// connection, and the client draws them over the video until a video frame
// changes that part. Which tiles each frame changed goes with the frame in
// UDP packets (PacketTiles) to clients that take them, and as tile_frame
// messages on the WebSocket to the others. TileCodec.kt mirrors this file.

// Tile message names. tile_frame and tile_data are binary messages whose
// first byte is TileKindFrame or TileKindData; tiles is a JSON control
// message.
const (
	// MsgTiles turns exact tiles on or off during a session (TilesChange).
	MsgTiles = "tiles"
	// MsgTileFrame names the binary message that says which tiles a video
	// frame changed (TileFrame).
	MsgTileFrame = "tile_frame"
	// MsgTileData names the binary message that carries tiles' pixels
	// (TileData).
	MsgTileData = "tile_data"
)

// TilesChange turns exact tiles on or off. Turning them on while they are on
// starts over: every tile the client holds becomes invalid and settled parts
// of the screen are sent again, for a client that lost its tiles.
type TilesChange struct {
	On bool `json:"on"`
}

const (
	// TileSize is the side of a tile in stream pixels; the tiles of the last
	// column and row are cut to the stream's edge.
	TileSize = 64
	// TileKindFrame is byte 0 of a tile_frame message.
	TileKindFrame byte = 1
	// TileKindData is byte 0 of a tile_data message.
	TileKindData byte = 2
	// TileFrameHeaderSize is a tile_frame message without its tile list.
	TileFrameHeaderSize = 20
	// TileDataHeaderSize is a tile_data message before its tile records.
	TileDataHeaderSize = 8
	// TileRecordSize is one tile's record in a tile_data message.
	TileRecordSize = 8
	// MaxTilesPerMessage bounds the tiles of one tile_data message.
	MaxTilesPerMessage = 64
	// tileFrameAll in a tile_frame's flags says that every tile from an
	// earlier frame is invalid.
	tileFrameAll = 0x01
	// tileFrameBitmap in a record's flags says that the changed tiles are
	// a bitmap over the grid instead of a list; only PacketTiles uses it.
	tileFrameBitmap = 0x02
	// TilePacketRecords bounds the records of one PacketTiles datagram: the
	// frame's own and up to three before it, so a lost datagram's news
	// arrives with one of the next three frames.
	TilePacketRecords = 4
)

// TileGrid cuts a Width × Height stream into TileSize tiles, numbered row
// by row from the top-left corner.
type TileGrid struct {
	Width, Height int
}

// Cols is the number of tile columns.
func (g TileGrid) Cols() int { return (g.Width + TileSize - 1) / TileSize }

// Rows is the number of tile rows.
func (g TileGrid) Rows() int { return (g.Height + TileSize - 1) / TileSize }

// Count is the number of tiles.
func (g TileGrid) Count() int { return g.Cols() * g.Rows() }

// Rect is the stream rectangle of tile slot: its top-left corner and size.
func (g TileGrid) Rect(slot int) (x, y, w, h int) {
	cols := g.Cols()
	x, y = slot%cols*TileSize, slot/cols*TileSize
	return x, y, min(TileSize, g.Width-x), min(TileSize, g.Height-y)
}

// Valid reports whether the grid has a size the messages can carry.
func (g TileGrid) Valid() bool {
	return g.Width > 0 && g.Height > 0 && g.Width <= 0xFFFF && g.Height <= 0xFFFF && g.Count() <= 0xFFFF
}

// TileFrame is a tile_frame message, sent for every video frame while tiles
// are on: Seq numbers the frame among the host's encoded frames,
// CaptureMicros is the frame's capture time as its video fragments carry it
// (and the client's decoder presents it), and Changed lists the tracked
// tiles whose pixels this frame changed. All says that every tile from a
// frame before this one is invalid.
//
// Layout: kind (1), flags (1, bit 0 All), width (2), height (2), seq (4),
// capture time in Unix microseconds (8), count (2), then count tile numbers
// (2 each); big-endian.
type TileFrame struct {
	Grid          TileGrid
	Seq           uint32
	CaptureMicros uint64
	All           bool
	Changed       []uint16
}

// Marshal encodes the message.
func (f TileFrame) Marshal() []byte {
	b := make([]byte, 0, TileFrameHeaderSize+2*len(f.Changed))
	var flags byte
	if f.All {
		flags |= tileFrameAll
	}
	b = append(b, TileKindFrame, flags)
	b = binary.BigEndian.AppendUint16(b, uint16(f.Grid.Width))
	b = binary.BigEndian.AppendUint16(b, uint16(f.Grid.Height))
	b = binary.BigEndian.AppendUint32(b, f.Seq)
	b = binary.BigEndian.AppendUint64(b, f.CaptureMicros)
	b = binary.BigEndian.AppendUint16(b, uint16(len(f.Changed)))
	for _, s := range f.Changed {
		b = binary.BigEndian.AppendUint16(b, s)
	}
	return b
}

// TileFrameAll reports whether msg is a tile_frame message with the All
// flag, without decoding the rest.
func TileFrameAll(msg []byte) bool {
	return len(msg) >= 2 && msg[0] == TileKindFrame && msg[1]&tileFrameAll != 0
}

// ParseTileFrame decodes a tile_frame message.
func ParseTileFrame(b []byte) (TileFrame, error) {
	f, n, err := parseTileRecord(b)
	if err != nil {
		return TileFrame{}, err
	}
	if n != len(b) {
		return TileFrame{}, fmt.Errorf("tile_frame of %d bytes holds a record of %d", len(b), n)
	}
	return f, nil
}

// bitmapBytes is the length of a bitmap over the grid's tiles.
func (g TileGrid) bitmapBytes() int { return (g.Count() + 7) / 8 }

// appendRecord appends f in the tile_frame layout, its changed tiles as a
// list or, when shorter, as a bitmap (bit i%8 of byte i/8 marks tile i).
func (f TileFrame) appendRecord(dst []byte) []byte {
	var flags byte
	if f.All {
		flags |= tileFrameAll
	}
	bitmap := 2*len(f.Changed) > f.Grid.bitmapBytes()
	count := len(f.Changed)
	if bitmap {
		flags |= tileFrameBitmap
		count = f.Grid.bitmapBytes()
	}
	dst = append(dst, TileKindFrame, flags)
	dst = binary.BigEndian.AppendUint16(dst, uint16(f.Grid.Width))
	dst = binary.BigEndian.AppendUint16(dst, uint16(f.Grid.Height))
	dst = binary.BigEndian.AppendUint32(dst, f.Seq)
	dst = binary.BigEndian.AppendUint64(dst, f.CaptureMicros)
	dst = binary.BigEndian.AppendUint16(dst, uint16(count))
	if !bitmap {
		for _, s := range f.Changed {
			dst = binary.BigEndian.AppendUint16(dst, s)
		}
		return dst
	}
	start := len(dst)
	dst = append(dst, make([]byte, count)...)
	for _, s := range f.Changed {
		dst[start+int(s)/8] |= 1 << (s % 8)
	}
	return dst
}

// recordSize is the length of f's record as appendRecord writes it.
func (f TileFrame) recordSize() int {
	return TileFrameHeaderSize + min(2*len(f.Changed), f.Grid.bitmapBytes())
}

// parseTileRecord decodes the tile_frame record at the start of b and
// returns its length.
func parseTileRecord(b []byte) (TileFrame, int, error) {
	if len(b) < TileFrameHeaderSize || b[0] != TileKindFrame {
		return TileFrame{}, 0, ErrShortPacket
	}
	f := TileFrame{
		Grid:          TileGrid{Width: int(binary.BigEndian.Uint16(b[2:])), Height: int(binary.BigEndian.Uint16(b[4:]))},
		All:           b[1]&tileFrameAll != 0,
		Seq:           binary.BigEndian.Uint32(b[6:]),
		CaptureMicros: binary.BigEndian.Uint64(b[10:]),
	}
	count := int(binary.BigEndian.Uint16(b[18:]))
	body := b[TileFrameHeaderSize:]
	if b[1]&tileFrameBitmap == 0 {
		if len(body) < 2*count {
			return TileFrame{}, 0, fmt.Errorf("tile record of %d bytes lists %d tiles", len(b), count)
		}
		f.Changed = make([]uint16, count)
		for i := range f.Changed {
			f.Changed[i] = binary.BigEndian.Uint16(body[2*i:])
		}
		return f, TileFrameHeaderSize + 2*count, nil
	}
	if !f.Grid.Valid() || count != f.Grid.bitmapBytes() || len(body) < count {
		return TileFrame{}, 0, fmt.Errorf("tile record's bitmap of %d bytes does not match a %dx%d stream", count, f.Grid.Width, f.Grid.Height)
	}
	f.Changed = []uint16{}
	for i := range f.Grid.Count() {
		if body[i/8]&(1<<(i%8)) != 0 {
			f.Changed = append(f.Changed, uint16(i))
		}
	}
	return f, TileFrameHeaderSize + count, nil
}

// AppendTilePacket appends the plaintext of a PacketTiles datagram:
// frames[0]'s record, then those of the frames before it (frames[1:],
// newest first) while they fit, at most TilePacketRecords in all. Each
// record has the tile_frame layout, its changed tiles as a list or, when
// shorter, as a bitmap. A frame whose record does not fit a datagram even
// alone (a grid of more than about 9800 tiles) goes as All without a list,
// and fits is false: the caller must then start the tiles over, since the
// client drops every tile the host still counts as sent.
func AppendTilePacket(dst []byte, frames []TileFrame) (out []byte, fits bool) {
	if len(frames) == 0 {
		return dst, true
	}
	first := frames[0]
	if first.recordSize() > MaxPlaintextSize {
		first = TileFrame{Grid: first.Grid, Seq: first.Seq, CaptureMicros: first.CaptureMicros, All: true}
		return first.appendRecord(dst), false
	}
	size := 0
	for i, f := range frames[:min(len(frames), TilePacketRecords)] {
		if i > 0 && size+f.recordSize() > MaxPlaintextSize {
			break
		}
		size += f.recordSize()
		dst = f.appendRecord(dst)
	}
	return dst, true
}

// ParseTilePacket decodes the records of a PacketTiles plaintext, newest
// first.
func ParseTilePacket(b []byte) ([]TileFrame, error) {
	var out []TileFrame
	for len(b) > 0 {
		f, n, err := parseTileRecord(b)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
		b = b[n:]
	}
	if len(out) == 0 {
		return nil, ErrShortPacket
	}
	return out, nil
}

// EncodedTile is one tile ready for a tile_data message: its pixels as
// QOI ops (AppendQOI) and the frame they are exact for.
type EncodedTile struct {
	Seq  uint32
	Slot uint16
	QOI  []byte
}

// MarshalTileData encodes a tile_data message: kind (1), flags (1, zero),
// width (2), height (2), count (2), then per tile its frame's seq (4), its
// number (2) and the length of its QOI ops (2), then the zlib stream (RFC
// 1950) of every tile's QOI ops in that order; big-endian. One stream
// compresses the tiles together, which makes a batch of 16 text tiles about
// 40 % smaller than tiles compressed one by one.
func MarshalTileData(g TileGrid, tiles []EncodedTile) ([]byte, error) {
	if len(tiles) == 0 || len(tiles) > MaxTilesPerMessage {
		return nil, fmt.Errorf("a tile_data message carries 1 to %d tiles, not %d", MaxTilesPerMessage, len(tiles))
	}
	var b bytes.Buffer
	b.Write([]byte{TileKindData, 0})
	hdr := binary.BigEndian.AppendUint16(nil, uint16(g.Width))
	hdr = binary.BigEndian.AppendUint16(hdr, uint16(g.Height))
	hdr = binary.BigEndian.AppendUint16(hdr, uint16(len(tiles)))
	for _, t := range tiles {
		if len(t.QOI) > 0xFFFF {
			return nil, fmt.Errorf("tile %d: %d bytes of QOI ops do not fit the record", t.Slot, len(t.QOI))
		}
		hdr = binary.BigEndian.AppendUint32(hdr, t.Seq)
		hdr = binary.BigEndian.AppendUint16(hdr, t.Slot)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(len(t.QOI)))
	}
	b.Write(hdr)
	z := zlib.NewWriter(&b)
	for _, t := range tiles {
		if _, err := z.Write(t.QOI); err != nil {
			return nil, fmt.Errorf("compress tiles: %w", err)
		}
	}
	if err := z.Close(); err != nil {
		return nil, fmt.Errorf("compress tiles: %w", err)
	}
	return b.Bytes(), nil
}

// DecodedTile is one tile of a parsed tile_data message, as RGB bytes row by
// row.
type DecodedTile struct {
	Seq  uint32
	Slot uint16
	RGB  []byte
}

// ParseTileData decodes a tile_data message and every tile's pixels.
func ParseTileData(b []byte) (TileGrid, []DecodedTile, error) {
	if len(b) < TileDataHeaderSize || b[0] != TileKindData {
		return TileGrid{}, nil, ErrShortPacket
	}
	g := TileGrid{Width: int(binary.BigEndian.Uint16(b[2:])), Height: int(binary.BigEndian.Uint16(b[4:]))}
	n := int(binary.BigEndian.Uint16(b[6:]))
	body := TileDataHeaderSize + n*TileRecordSize
	if n == 0 || n > MaxTilesPerMessage || len(b) < body || !g.Valid() {
		return TileGrid{}, nil, fmt.Errorf("bad tile_data header: %d tiles in %d bytes", n, len(b))
	}
	total := 0
	tiles := make([]DecodedTile, n)
	lengths := make([]int, n)
	for i := range tiles {
		r := b[TileDataHeaderSize+i*TileRecordSize:]
		tiles[i] = DecodedTile{Seq: binary.BigEndian.Uint32(r), Slot: binary.BigEndian.Uint16(r[4:])}
		if int(tiles[i].Slot) >= g.Count() {
			return TileGrid{}, nil, fmt.Errorf("tile %d is outside the %dx%d grid", tiles[i].Slot, g.Width, g.Height)
		}
		lengths[i] = int(binary.BigEndian.Uint16(r[6:]))
		total += lengths[i]
	}
	z, err := zlib.NewReader(bytes.NewReader(b[body:]))
	if err != nil {
		return TileGrid{}, nil, fmt.Errorf("tile_data stream: %w", err)
	}
	ops := make([]byte, total)
	if _, err := io.ReadFull(z, ops); err != nil {
		return TileGrid{}, nil, fmt.Errorf("tile_data stream: %w", err)
	}
	for i := range tiles {
		_, _, w, h := g.Rect(int(tiles[i].Slot))
		if tiles[i].RGB, err = DecodeQOI(ops[:lengths[i]], w*h); err != nil {
			return TileGrid{}, nil, fmt.Errorf("tile %d: %w", tiles[i].Slot, err)
		}
		ops = ops[lengths[i]:]
	}
	return g, tiles, nil
}

// QOI ops (https://qoiformat.org/qoi-specification.pdf). A tile is encoded
// as the spec's op stream without the header and end marker, with alpha 255
// throughout, so QOI_OP_RGBA never occurs; each tile starts afresh.
const (
	qoiOpIndex = 0x00
	qoiOpDiff  = 0x40
	qoiOpLuma  = 0x80
	qoiOpRun   = 0xc0
	qoiOpRGB   = 0xfe
	qoiOpRGBA  = 0xff
	qoiMask    = 0xc0
	qoiMaxRun  = 62
)

func qoiHash(r, g, b byte) int { return (int(r)*3 + int(g)*5 + int(b)*7 + 255*11) % 64 }

// AppendQOI appends the QOI ops of a w × h block of BGRx pixels to dst;
// stride is the distance between rows in pix. The fourth byte of each pixel
// is ignored.
func AppendQOI(dst, pix []byte, stride, w, h int) []byte {
	var index [64][3]byte
	pr, pg, pb := byte(0), byte(0), byte(0)
	run := 0
	last := w*h - 1
	for y := 0; y < h; y++ {
		row := pix[y*stride : y*stride+w*4]
		for x := 0; x < w; x++ {
			b, g, r := row[x*4], row[x*4+1], row[x*4+2]
			if r == pr && g == pg && b == pb {
				run++
				if run == qoiMaxRun || y*w+x == last {
					dst = append(dst, qoiOpRun|byte(run-1))
					run = 0
				}
				continue
			}
			if run > 0 {
				dst = append(dst, qoiOpRun|byte(run-1))
				run = 0
			}
			i := qoiHash(r, g, b)
			if index[i] == [3]byte{r, g, b} {
				dst = append(dst, qoiOpIndex|byte(i))
			} else {
				index[i] = [3]byte{r, g, b}
				dr, dg, db := int8(r-pr), int8(g-pg), int8(b-pb)
				drg, dbg := dr-dg, db-dg
				switch {
				case dr >= -2 && dr <= 1 && dg >= -2 && dg <= 1 && db >= -2 && db <= 1:
					dst = append(dst, qoiOpDiff|byte(dr+2)<<4|byte(dg+2)<<2|byte(db+2))
				case dg >= -32 && dg <= 31 && drg >= -8 && drg <= 7 && dbg >= -8 && dbg <= 7:
					dst = append(dst, qoiOpLuma|byte(dg+32), byte(drg+8)<<4|byte(dbg+8))
				default:
					dst = append(dst, qoiOpRGB, r, g, b)
				}
			}
			pr, pg, pb = r, g, b
		}
	}
	return dst
}

// errQOI is returned for QOI ops that do not decode to the expected pixels.
var errQOI = errors.New("QOI ops do not match the tile's size")

// DecodeQOI decodes the QOI ops of a tile of n pixels into RGB bytes.
func DecodeQOI(ops []byte, n int) ([]byte, error) {
	out := make([]byte, 0, n*3)
	var index [64][3]byte
	var px [3]byte
	run := 0
	p := 0
	for len(out) < n*3 {
		if run > 0 {
			run--
		} else {
			if p >= len(ops) {
				return nil, errQOI
			}
			b1 := ops[p]
			p++
			switch {
			case b1 == qoiOpRGB || b1 == qoiOpRGBA:
				size := 3
				if b1 == qoiOpRGBA {
					size = 4
				}
				if p+size > len(ops) {
					return nil, errQOI
				}
				px = [3]byte{ops[p], ops[p+1], ops[p+2]}
				p += size
			case b1&qoiMask == qoiOpIndex:
				px = index[b1]
			case b1&qoiMask == qoiOpDiff:
				px[0] += (b1>>4)&3 - 2
				px[1] += (b1>>2)&3 - 2
				px[2] += b1&3 - 2
			case b1&qoiMask == qoiOpLuma:
				if p >= len(ops) {
					return nil, errQOI
				}
				b2 := ops[p]
				p++
				dg := b1&0x3f - 32
				px[0] += dg + (b2>>4)&0x0f - 8
				px[1] += dg
				px[2] += dg + b2&0x0f - 8
			default:
				run = int(b1 & 0x3f)
			}
			index[qoiHash(px[0], px[1], px[2])] = px
		}
		out = append(out, px[0], px[1], px[2])
	}
	if run != 0 || p != len(ops) {
		return nil, errQOI
	}
	return out, nil
}
