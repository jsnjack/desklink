package net

import (
	"bytes"
	"encoding/hex"
	"math/rand/v2"
	"slices"
	"testing"
)

// vectorPixel is the pattern of the shared tile vectors, at stream pixel
// x, y: flat backgrounds, a blue stroke, a ramp and scattered colours, as
// text on a desktop gives. TileCodecTest.kt has the same function.
func vectorPixel(x, y int) (r, g, b byte) {
	switch {
	case y < 2:
		return 28, 28, 28
	case (x+y)%7 == 0:
		return 98, 160, 234
	case x%5 == 0:
		return byte(30 + x), byte(40 + y), 200
	case x%3 == 0:
		return byte(x * 9), byte(y * 13), byte(x ^ y)
	default:
		return 36, 31, 49
	}
}

// vectorTile renders the pattern of a w × h tile at x0, y0 as BGRx, with
// the padding byte varying to show that it is ignored.
func vectorTile(x0, y0, w, h int) []byte {
	pix := make([]byte, 0, w*h*4)
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			r, g, b := vectorPixel(x, y)
			pix = append(pix, b, g, r, byte(x*y))
		}
	}
	return pix
}

// rgbOf drops the padding byte of BGRx pixels and swaps them to RGB.
func rgbOf(bgrx []byte) []byte {
	out := make([]byte, 0, len(bgrx)/4*3)
	for i := 0; i+3 < len(bgrx); i += 4 {
		out = append(out, bgrx[i+2], bgrx[i+1], bgrx[i])
	}
	return out
}

// Vectors that TileCodecTest.kt decodes: two tiles of a 70x6 stream (the
// right one cut to 6x6), and two tile_frame messages.
const (
	tileDataVector = "02000046000600020000000500010042000000060000024c789c24914b6a1b4d14857ffd799007210e07eca444e49465" +
		"a7a47e2471bbac063fc017843cf0443168e24943c6f606b4022f2378055a800545469768035e8106996aec43b8d1ac69" +
		"be3edf575df7b7bfb9bbbdcf26535eba33f0c7cf3fbc71e760932b2f83d80bf0268cc0a6508fc49b28f0bc94737b96b3" +
		"fbdbc7c75fec646a3b896dd75a8f0cdc53247e77afd9cb14f661992912efdc86679529e76e0b890fceb1ced473e53e20" +
		"f1c4180f4aa61cbacf486cdc9e07a7ae87c40b5be2cc55e038532edc3e92e72453b0e58e917895a9076b77ca4e6eb276" +
		"f8cfb39b2b07e199d58457ece5ea791dde5a90311eac72e53c6c5a4d687b7015de5b4a6eb27ed80125570e4340fa77b2" +
		"51aee034f42d2837df2c1c80e35cb908dfe0b90c879c18d30a47b69f588713768ab5ac5b2807f17fab896f3c781d5f58" +
		"4a61b2bbb8095685721edfd9c9ea42c155dcb61afbf9ecc73d500ae5307e826713bb1c19338d5f6c3c711623382e948b" +
		"587b70192b4e0c68c553785e5903eb78cc4e69beb63c07bba572204fccd72b15bc969716549aef4eb6c0aa54ce65637d" +
		"ad0ff291b5612be9ac957dd905a5540ec57bb0911d8e0c98ca57785e94a69cc92138b6c9c4a51c7062404b4e6c12bc2a" +
		"1589b51cfd1d00965c1fd2"
	tileFrameVector    = "01000a000640010203040102030405060708000200030102"
	tileFrameAllVector = "0101004600060000000700000000000000090000"
	// tilePacketVector is a PacketTiles plaintext: frame 9 of a 512x128
	// stream (16 tiles) changed tiles 0, 3, 9 and 15, as a bitmap; frame 8
	// changed tile 5, as a list; frame 7 invalidated every tile.
	tilePacketVector = "0102020000800000000900000000000003e900020982" +
		"0100020000800000000800000000000003e800010005" +
		"0101020000800000000700000000000003e70000"
)

func TestTileGridRects(t *testing.T) {
	cases := []struct {
		name          string
		grid          TileGrid
		slot          int
		x, y, w, h    int
		cols, rows, n int
	}{
		{"first tile of the full stream", TileGrid{2560, 1600}, 0, 0, 0, 64, 64, 40, 25, 1000},
		{"last tile of the full stream", TileGrid{2560, 1600}, 999, 2496, 1536, 64, 64, 40, 25, 1000},
		{"bottom row cut to the stream", TileGrid{1920, 1200}, 30*18 + 3, 192, 1152, 64, 48, 30, 19, 570},
		{"right column cut to the stream", TileGrid{70, 10}, 1, 64, 0, 6, 10, 2, 1, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, y, w, h := c.grid.Rect(c.slot)
			if x != c.x || y != c.y || w != c.w || h != c.h {
				t.Errorf("Rect(%d) = %d,%d %dx%d, want %d,%d %dx%d", c.slot, x, y, w, h, c.x, c.y, c.w, c.h)
			}
			if c.grid.Cols() != c.cols || c.grid.Rows() != c.rows || c.grid.Count() != c.n {
				t.Errorf("grid is %dx%d = %d tiles, want %dx%d = %d", c.grid.Cols(), c.grid.Rows(), c.grid.Count(), c.cols, c.rows, c.n)
			}
		})
	}
}

func TestQOIRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	noise := func(w, h int) []byte {
		pix := make([]byte, w*h*4)
		for i := range pix {
			pix[i] = byte(rng.Uint32())
		}
		return pix
	}
	flat := func(w, h int) []byte {
		pix := make([]byte, w*h*4)
		for i := 0; i < len(pix); i += 4 {
			pix[i], pix[i+1], pix[i+2], pix[i+3] = 28, 28, 28, byte(i)
		}
		return pix
	}
	gradient := func(w, h int) []byte {
		pix := make([]byte, 0, w*h*4)
		for y := range h {
			for x := range w {
				pix = append(pix, byte(x), byte(x+y), byte(y*3), 0)
			}
		}
		return pix
	}
	cases := []struct {
		name string
		w, h int
		pix  []byte
	}{
		{"text-like pattern, full tile", 64, 64, vectorTile(0, 0, 64, 64)},
		{"flat colour: runs longer than one op", 64, 64, flat(64, 64)},
		{"flat colour of 62 pixels: one full run", 62, 1, flat(62, 1)},
		{"small steps: diff and luma ops", 64, 64, gradient(64, 64)},
		{"noise: literal ops", 64, 64, noise(64, 64)},
		{"edge tile", 6, 10, vectorTile(64, 0, 6, 10)},
		{"one pixel", 1, 1, []byte{1, 2, 3, 4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ops := AppendQOI(nil, c.pix, c.w*4, c.w, c.h)
			got, err := DecodeQOI(ops, c.w*c.h)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, rgbOf(c.pix)) {
				t.Error("decoded pixels differ from the encoded ones")
			}
		})
	}
}

func TestQOIReadsRowsByStride(t *testing.T) {
	// A 2x2 block in the top-left corner of a 3-pixel-wide frame.
	frame := []byte{1, 2, 3, 0, 4, 5, 6, 0, 99, 99, 99, 0, 7, 8, 9, 0, 10, 11, 12, 0, 99, 99, 99, 0}
	got, err := DecodeQOI(AppendQOI(nil, frame, 12, 2, 2), 4)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{3, 2, 1, 6, 5, 4, 9, 8, 7, 12, 11, 10}; !bytes.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestQOIRejectsOpsOfAnotherSize(t *testing.T) {
	ops := AppendQOI(nil, vectorTile(0, 0, 8, 8), 32, 8, 8)
	cases := []struct {
		name string
		ops  []byte
		n    int
	}{
		{"truncated", ops[:len(ops)-1], 64},
		{"too many pixels expected", ops, 65},
		{"too few pixels expected", ops, 63},
		{"a literal cut short", []byte{qoiOpRGB, 1}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := DecodeQOI(c.ops, c.n); err == nil {
				t.Error("decoded without an error")
			}
		})
	}
}

func TestTileFrameRoundTrip(t *testing.T) {
	cases := []TileFrame{
		{Grid: TileGrid{2560, 1600}, Seq: 1, CaptureMicros: 1732530000123456, Changed: []uint16{0, 39, 999}},
		{Grid: TileGrid{1920, 1200}, Seq: 0xFFFFFFFF, CaptureMicros: 1, All: true, Changed: []uint16{}},
	}
	for _, f := range cases {
		got, err := ParseTileFrame(f.Marshal())
		if err != nil {
			t.Fatal(err)
		}
		if got.Grid != f.Grid || got.Seq != f.Seq || got.CaptureMicros != f.CaptureMicros || got.All != f.All || !slices.Equal(got.Changed, f.Changed) {
			t.Errorf("round trip gave %+v, want %+v", got, f)
		}
		if TileFrameAll(f.Marshal()) != f.All {
			t.Errorf("TileFrameAll = %v, want %v", !f.All, f.All)
		}
	}
	if _, err := ParseTileFrame(TileFrame{Changed: []uint16{1, 2}}.Marshal()[:21]); err == nil {
		t.Error("a tile_frame cut inside its list parsed")
	}
}

func TestTilePacketCarriesTheFrameAndTheOnesBeforeIt(t *testing.T) {
	big := TileGrid{Width: 2560, Height: 1600}
	many := make([]uint16, 0, 300)
	for i := range 300 {
		many = append(many, uint16(i*3))
	}
	frames := []TileFrame{
		{Grid: big, Seq: 12, CaptureMicros: 1200, Changed: many},
		{Grid: big, Seq: 11, CaptureMicros: 1100, Changed: []uint16{7}},
		{Grid: big, Seq: 10, CaptureMicros: 1000, All: true, Changed: []uint16{}},
		{Grid: big, Seq: 9, CaptureMicros: 900, Changed: []uint16{0, 999}},
		{Grid: big, Seq: 8, CaptureMicros: 800, Changed: []uint16{1}},
	}
	b, fits := AppendTilePacket(nil, frames)
	if !fits {
		t.Fatal("a packet for a 2560x1600 stream did not fit")
	}
	// 300 tiles as a bitmap of 125 bytes instead of a list of 600.
	if want := 20 + 125 + 22 + 20 + 24; len(b) != want {
		t.Errorf("packet of %d bytes, want %d", len(b), want)
	}
	got, err := ParseTilePacket(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != TilePacketRecords {
		t.Fatalf("%d records, want the frame's and the %d before it", len(got), TilePacketRecords-1)
	}
	for i, f := range got {
		w := frames[i]
		if f.Grid != w.Grid || f.Seq != w.Seq || f.CaptureMicros != w.CaptureMicros || f.All != w.All || !slices.Equal(f.Changed, w.Changed) {
			t.Errorf("record %d is %+v, want %+v", i, f, w)
		}
	}
}

func TestTilePacketKeepsItsOwnFrameWhenTheOnesBeforeItDoNotFit(t *testing.T) {
	g := TileGrid{Width: 4096, Height: 4096} // 4096 tiles: a 512-byte bitmap
	half := make([]uint16, 0, 2048)
	for i := range 2048 {
		half = append(half, uint16(2*i))
	}
	b, fits := AppendTilePacket(nil, []TileFrame{{Grid: g, Seq: 2, Changed: half}, {Grid: g, Seq: 1, Changed: half}, {Grid: g, Seq: 0, Changed: half}})
	got, err := ParseTilePacket(b)
	if !fits || err != nil || len(got) != 2 || len(b) > MaxPlaintextSize {
		t.Fatalf("fits %v, %d records in %d bytes, err %v", fits, len(got), len(b), err)
	}
	huge := TileGrid{Width: 0xFFFF, Height: 0x2000}
	b, fits = AppendTilePacket(nil, []TileFrame{{Grid: huge, Seq: 5, Changed: half}})
	got, err = ParseTilePacket(b)
	if fits || err != nil || len(got) != 1 || !got[0].All || len(got[0].Changed) != 0 {
		t.Errorf("a record too large for a datagram: fits %v, %+v, err %v", fits, got, err)
	}
}

func TestTilePacketRejectsACutRecord(t *testing.T) {
	b, _ := hex.DecodeString(tilePacketVector)
	for _, n := range []int{0, 5, 21, len(b) - 1} {
		if _, err := ParseTilePacket(b[:n]); err == nil {
			t.Errorf("a packet cut to %d bytes parsed", n)
		}
	}
}

func TestTileDataRoundTrip(t *testing.T) {
	g := TileGrid{Width: 1920, Height: 1200}
	var tiles []EncodedTile
	var want [][]byte
	for i, slot := range []int{0, 29, 30*18 + 29} {
		x, y, w, h := g.Rect(slot)
		pix := vectorTile(x, y, w, h)
		tiles = append(tiles, EncodedTile{Seq: uint32(100 + i), Slot: uint16(slot), QOI: AppendQOI(nil, pix, w*4, w, h)})
		want = append(want, rgbOf(pix))
	}
	msg, err := MarshalTileData(g, tiles)
	if err != nil {
		t.Fatal(err)
	}
	gotGrid, got, err := ParseTileData(msg)
	if err != nil {
		t.Fatal(err)
	}
	if gotGrid != g || len(got) != len(tiles) {
		t.Fatalf("got grid %+v and %d tiles", gotGrid, len(got))
	}
	for i := range got {
		if got[i].Seq != tiles[i].Seq || got[i].Slot != tiles[i].Slot || !bytes.Equal(got[i].RGB, want[i]) {
			t.Errorf("tile %d differs after the round trip", i)
		}
	}
	if _, err := MarshalTileData(g, nil); err == nil {
		t.Error("an empty tile_data message was encoded")
	}
	bad := slices.Clone(msg)
	bad[TileDataHeaderSize+4], bad[TileDataHeaderSize+5] = 0xFF, 0xFF
	if _, _, err := ParseTileData(bad); err == nil {
		t.Error("a tile outside the grid parsed")
	}
}

// TestTileVectors pins the bytes that TileCodecTest.kt decodes, so both
// sides read the same layout.
func TestTileVectors(t *testing.T) {
	unhex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	frame := TileFrame{Grid: TileGrid{Width: 2560, Height: 1600}, Seq: 0x01020304, CaptureMicros: 0x0102030405060708, Changed: []uint16{3, 0x0102}}
	if got := hex.EncodeToString(frame.Marshal()); got != tileFrameVector {
		t.Errorf("tile_frame encodes as %s, want %s", got, tileFrameVector)
	}
	all := TileFrame{Grid: TileGrid{Width: 70, Height: 6}, Seq: 7, CaptureMicros: 9, All: true}
	if got := hex.EncodeToString(all.Marshal()); got != tileFrameAllVector {
		t.Errorf("tile_frame with All encodes as %s, want %s", got, tileFrameAllVector)
	}
	small := TileGrid{Width: 512, Height: 128}
	packet, _ := AppendTilePacket(nil, []TileFrame{
		{Grid: small, Seq: 9, CaptureMicros: 1001, Changed: []uint16{0, 3, 9, 15}},
		{Grid: small, Seq: 8, CaptureMicros: 1000, Changed: []uint16{5}},
		{Grid: small, Seq: 7, CaptureMicros: 999, All: true},
	})
	if got := hex.EncodeToString(packet); got != tilePacketVector {
		t.Errorf("the tile packet encodes as %s, want %s", got, tilePacketVector)
	}
	g, tiles, err := ParseTileData(unhex(tileDataVector))
	if err != nil {
		t.Fatal(err)
	}
	if g != (TileGrid{Width: 70, Height: 6}) || len(tiles) != 2 || tiles[0].Slot != 1 || tiles[0].Seq != 5 || tiles[1].Slot != 0 || tiles[1].Seq != 6 {
		t.Fatalf("vector decodes as grid %+v, tiles %+v", g, tiles)
	}
	for _, tile := range tiles {
		x0, y0, w, h := g.Rect(int(tile.Slot))
		if !bytes.Equal(tile.RGB, rgbOf(vectorTile(x0, y0, w, h))) {
			t.Errorf("tile %d of the vector does not hold the pattern", tile.Slot)
		}
	}
}
