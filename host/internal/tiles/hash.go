package tiles

import (
	"encoding/binary"
	"math/bits"
)

// The tile hash is xxHash64's round function over four lanes. A change
// that the hash misses would leave stale pixels on the client, so it is a
// 64-bit hash with full mixing rather than a checksum.
const (
	prime1 uint64 = 0x9E3779B185EBCA87
	prime2 uint64 = 0xC2B2AE3D27D4EB4F
	prime3 uint64 = 0x165667B19E3779F9
	// lane1 and lane4 start two lanes as xxHash64 does with seed 0:
	// prime1+prime2 and -prime1, modulo 2⁶⁴.
	lane1 uint64 = 0x60EA27EEADC0B5D6
	lane4 uint64 = 0x61C8864E7A143579
	// rgbMask keeps blue, green and red of two BGRx pixels in a
	// little-endian word: the fourth byte is padding the client never sees,
	// and a source may fill it with anything.
	rgbMask = 0x00FFFFFF00FFFFFF
)

func round(acc, in uint64) uint64 {
	return bits.RotateLeft64(acc+in*prime2, 31) * prime1
}

// hashTile hashes the red, green and blue bytes of the w × h block of BGRx
// pixels at x, y in pix, whose rows are stride bytes apart.
func hashTile(pix []byte, stride, x, y, w, h int) uint64 {
	a0, a1, a2, a3 := lane1, prime2, uint64(0), lane4
	for r := y; r < y+h; r++ {
		row := pix[r*stride+x*4 : r*stride+(x+w)*4]
		for len(row) >= 32 {
			a0 = round(a0, binary.LittleEndian.Uint64(row)&rgbMask)
			a1 = round(a1, binary.LittleEndian.Uint64(row[8:])&rgbMask)
			a2 = round(a2, binary.LittleEndian.Uint64(row[16:])&rgbMask)
			a3 = round(a3, binary.LittleEndian.Uint64(row[24:])&rgbMask)
			row = row[32:]
		}
		for len(row) >= 8 {
			a0 = round(a0, binary.LittleEndian.Uint64(row)&rgbMask)
			row = row[8:]
		}
		if len(row) >= 4 {
			a1 = round(a1, uint64(binary.LittleEndian.Uint32(row)&0x00FFFFFF))
		}
	}
	sum := bits.RotateLeft64(a0, 1) + bits.RotateLeft64(a1, 7) + bits.RotateLeft64(a2, 12) + bits.RotateLeft64(a3, 18)
	sum ^= sum >> 33
	sum *= prime2
	sum ^= sum >> 29
	sum *= prime3
	return sum ^ sum>>32
}
