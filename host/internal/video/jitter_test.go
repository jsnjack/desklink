package video

import (
	"testing"
	"time"
)

// opus20 is a 20 ms Opus packet (CELT fullband, one frame) tagged with id.
func opus20(id byte) []byte { return []byte{31 << 3, id} }

// playOut pops n slots and returns what each was: the tag of the frame
// played, -1 for concealment or -2 for idle.
func playOut(j *jitterBuffer, n int) []int {
	var got []int
	for range n {
		f, _, kind := j.pop()
		switch kind {
		case playFrame:
			got = append(got, int(f[1]))
		case playConceal:
			got = append(got, -1)
		default:
			got = append(got, -2)
		}
	}
	return got
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestJitterBuffer(t *testing.T) {
	const idle, conceal = -2, -1
	cases := []struct {
		name string
		run  func(j *jitterBuffer) []int
		want []int
		// late and trimmed are the frames the buffer must have dropped.
		late, trimmed uint64
	}{
		{
			"playout waits until 40 ms are queued",
			func(j *jitterBuffer) []int {
				j.put(10, opus20(10))
				got := playOut(j, 1)
				j.put(11, opus20(11))
				return append(got, playOut(j, 3)...)
			},
			[]int{idle, 10, 11, conceal}, 0, 0,
		},
		{
			"a missing frame is concealed and playback goes on",
			func(j *jitterBuffer) []int {
				j.put(1, opus20(1))
				j.put(3, opus20(3))
				return playOut(j, 3)
			},
			[]int{1, conceal, 3}, 0, 0,
		},
		{
			"a frame whose turn has passed is dropped",
			func(j *jitterBuffer) []int {
				j.put(1, opus20(1))
				j.put(3, opus20(3))
				got := playOut(j, 2)
				j.put(2, opus20(2))
				return append(got, playOut(j, 1)...)
			},
			[]int{1, conceal, 3}, 1, 0,
		},
		{
			"frames reordered before playout starts still play in order",
			func(j *jitterBuffer) []int {
				j.put(6, opus20(6))
				j.put(5, opus20(5))
				return playOut(j, 2)
			},
			[]int{5, 6}, 0, 0,
		},
		{
			"a burst beyond 60 ms is cut back to 40 ms instead of delaying the rest",
			func(j *jitterBuffer) []int {
				for c := uint16(1); c <= 6; c++ {
					j.put(c, opus20(byte(c)))
				}
				return playOut(j, 3)
			},
			[]int{5, 6, conceal}, 0, 4,
		},
		{
			"after 100 ms of concealment playout waits and starts at the next frame that comes",
			func(j *jitterBuffer) []int {
				j.put(1, opus20(1))
				j.put(2, opus20(2))
				got := playOut(j, 8)
				j.put(20, opus20(20))
				j.put(21, opus20(21))
				// Frame 3 belongs to the stall: it is late, not a new start.
				j.put(3, opus20(3))
				return append(got, playOut(j, 2)...)
			},
			[]int{1, 2, conceal, conceal, conceal, conceal, conceal, idle, 20, 21}, 1, 0,
		},
		{
			"counters wrap at 16 bits",
			func(j *jitterBuffer) []int {
				j.put(65535, opus20(1))
				j.put(0, opus20(2))
				j.put(1, opus20(3))
				return playOut(j, 3)
			},
			[]int{1, 2, 3}, 0, 0,
		},
		{
			"a client that starts counting again is followed, not dropped as late",
			func(j *jitterBuffer) []int {
				j.put(3000, opus20(1))
				j.put(3001, opus20(2))
				got := playOut(j, 2)
				j.put(0, opus20(3))
				j.put(1, opus20(4))
				return append(got, playOut(j, 2)...)
			},
			[]int{1, 2, 3, 4}, 0, 0,
		},
		{
			"a repeated frame plays once",
			func(j *jitterBuffer) []int {
				j.put(1, opus20(1))
				j.put(1, opus20(1))
				j.put(2, opus20(2))
				return playOut(j, 3)
			},
			[]int{1, 2, conceal}, 0, 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := newJitterBuffer()
			if got := c.run(j); !equalInts(got, c.want) {
				t.Errorf("played %v, want %v", got, c.want)
			}
			if j.stats.Late != c.late || j.stats.Trimmed != c.trimmed {
				t.Errorf("late %d, trimmed %d; want %d and %d", j.stats.Late, j.stats.Trimmed, c.late, c.trimmed)
			}
		})
	}
}

func TestJitterBufferDropsWhatIsNotOpus(t *testing.T) {
	j := newJitterBuffer()
	j.put(1, nil)
	j.put(2, []byte{31<<3 | 3}) // code 3 without its frame count
	if j.stats.Bad != 2 || len(j.frames) != 0 {
		t.Errorf("bad %d, queued %d; want both dropped", j.stats.Bad, len(j.frames))
	}
}

func TestOpusDuration(t *testing.T) {
	cases := []struct {
		name   string
		packet []byte
		want   time.Duration
		ok     bool
	}{
		{"a CELT 20 ms frame", []byte{31 << 3}, 20 * time.Millisecond, true},
		{"a CELT 10 ms frame", []byte{30 << 3}, 10 * time.Millisecond, true},
		{"a CELT 2.5 ms frame", []byte{28 << 3}, 2500 * time.Microsecond, true},
		{"a SILK wideband 20 ms frame", []byte{9 << 3}, 20 * time.Millisecond, true},
		{"a SILK 60 ms frame", []byte{3 << 3}, 60 * time.Millisecond, true},
		{"a hybrid 10 ms frame", []byte{12 << 3}, 10 * time.Millisecond, true},
		{"two 20 ms frames in one packet", []byte{31<<3 | 1}, 40 * time.Millisecond, true},
		{"three 20 ms frames counted in the second byte", []byte{31<<3 | 3, 3}, 60 * time.Millisecond, true},
		{"more than 120 ms is not Opus", []byte{3<<3 | 3, 3}, 0, false},
		{"an empty packet", nil, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := opusDuration(c.packet)
			if got != c.want || ok != c.ok {
				t.Errorf("opusDuration = %v, %v; want %v, %v", got, ok, c.want, c.ok)
			}
		})
	}
}
