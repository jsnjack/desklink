package tiles

import (
	"math/rand/v2"
	"testing"

	dlnet "desklink/internal/net"
)

func TestTheTileHashSeesEveryColourByte(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"full tile", 64, 64},
		{"odd width: a pixel after the last whole word", 5, 3},
		{"width under one word", 1, 2},
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stride := c.w*4 + 8
			pix := make([]byte, stride*c.h)
			for i := range pix {
				pix[i] = byte(rng.Uint32())
			}
			base := hashTile(pix, stride, 0, 0, c.w, c.h)
			for y := range c.h {
				for x := range c.w {
					for ch := range 4 {
						i := y*stride + x*4 + ch
						pix[i] ^= 1
						changed := hashTile(pix, stride, 0, 0, c.w, c.h) != base
						pix[i] ^= 1
						if want := ch != 3; changed != want {
							t.Fatalf("flipping byte %d of pixel (%d,%d): hash changed %v, want %v", ch, x, y, changed, want)
						}
					}
				}
			}
			for i := c.w * 4; i < stride; i++ {
				pix[i] ^= 0xff
			}
			if hashTile(pix, stride, 0, 0, c.w, c.h) != base {
				t.Error("bytes beyond the tile changed its hash")
			}
		})
	}
}

func TestTheTileHashTellsMovedContentApart(t *testing.T) {
	// A glyph one pixel further right holds the same bytes in another order.
	pix := make([]byte, 64*4*64)
	pix[10*256+20*4] = 200
	a := hashTile(pix, 256, 0, 0, 64, 64)
	pix[10*256+20*4], pix[10*256+21*4] = 0, 200
	if hashTile(pix, 256, 0, 0, 64, 64) == a {
		t.Error("moving a pixel did not change the hash")
	}
}

func TestHashingAFrameInParallelGivesEveryTilesOwnHash(t *testing.T) {
	g := dlnet.TileGrid{Width: 1000, Height: 300}
	stride := g.Width*4 + 12
	pix := make([]byte, stride*g.Height)
	for i := range pix {
		pix[i] = byte(i * 13)
	}
	got := hashTiles(g, pix, stride)
	for i := range g.Count() {
		x, y, w, h := g.Rect(i)
		if want := hashTile(pix, stride, x, y, w, h); got[i] != want {
			t.Fatalf("tile %d hashed to %x, want %x", i, got[i], want)
		}
	}
}

// BenchmarkScanFullStream hashes every tile of a 2560x1600 frame, the work
// the tracker does for each scanned frame.
func BenchmarkScanFullStream(b *testing.B) {
	g := dlnet.TileGrid{Width: 2560, Height: 1600}
	pix := make([]byte, g.Width*4*g.Height)
	for i := range pix {
		pix[i] = byte(i * 7)
	}
	b.SetBytes(int64(len(pix)))
	for b.Loop() {
		hashTiles(g, pix, g.Width*4)
	}
}
