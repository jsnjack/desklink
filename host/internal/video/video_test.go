package video

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQualityController(t *testing.T) {
	t0 := time.Unix(0, 0)
	type sample struct {
		at      time.Duration
		loss    float64
		rtt     time.Duration
		wantQP  int
		changed bool
	}
	cases := []struct {
		name      string
		requested int
		samples   []sample
	}{
		{"loss raises the quantizer by two", 18, []sample{{0, 3, 10 * time.Millisecond, 20, true}}},
		{"high RTT raises it too", 18, []sample{{0, 0, 90 * time.Millisecond, 20, true}}},
		{"a cooldown limits raises to one a second", 18, []sample{
			{0, 5, 0, 20, true}, {500 * time.Millisecond, 5, 0, 20, false}, {time.Second, 5, 0, 22, true},
		}},
		{"never above the ceiling", 25, []sample{{0, 5, 0, 26, true}, {2 * time.Second, 5, 0, 26, false}}},
		{"recovers one step after five clean seconds", 18, []sample{
			{0, 5, 0, 20, true}, {time.Second, 0, 10 * time.Millisecond, 20, false},
			{5900 * time.Millisecond, 0, 10 * time.Millisecond, 20, false}, {6 * time.Second, 0, 10 * time.Millisecond, 19, true},
		}},
		{"a middling sample restarts the clean period", 18, []sample{
			{0, 5, 0, 20, true}, {time.Second, 0, 0, 20, false}, {4 * time.Second, 1, 0, 20, false}, {6 * time.Second, 0, 0, 20, false},
		}},
		{"never below the requested value", 18, []sample{{0, 0, 0, 18, false}, {10 * time.Second, 0, 0, 18, false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := NewQualityController(c.requested, 16, 26)
			for i, s := range c.samples {
				qp, changed := q.Observe(t0.Add(s.at), s.loss, s.rtt)
				if qp != s.wantQP || changed != s.changed {
					t.Errorf("sample %d: qp %d changed %v, want %d %v", i, qp, changed, s.wantQP, s.changed)
				}
			}
		})
	}
	t.Run("requested value is clamped to the range", func(t *testing.T) {
		if qp := NewQualityController(40, 16, 26).QP(); qp != 26 {
			t.Errorf("qp %d", qp)
		}
	})
}

func TestAV1QIndex(t *testing.T) {
	cases := []struct{ qp, want int }{{0, 0}, {16, 80}, {26, 130}, {51, 255}, {60, 255}}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.qp), func(t *testing.T) {
			if got := AV1QIndex(c.qp); got != c.want {
				t.Errorf("AV1QIndex(%d) = %d, want %d", c.qp, got, c.want)
			}
		})
	}
}

func TestPipelineDescription(t *testing.T) {
	base := Settings{Width: 2560, Height: 1600, QP: 18, FPS: 120, PipeWireFD: 5, NodeID: 42}
	cases := []struct {
		name  string
		codec Codec
		want  []string
	}{
		{"av1", CodecAV1, []string{
			"pipewiresrc fd=5 path=42", "videorate name=rate drop-only=true max-rate=120",
			"format=P010_10LE,width=2560,height=1600", "colorimetry=(string)1:3:5:1",
			"vaav1enc name=enc rate-control=cqp qp=90", "av1parse", "stream-format=obu-stream,alignment=tu", "drop=false",
		}},
		{"hevc", CodecHEVC, []string{
			"vah265enc name=enc rate-control=cqp qpi=18 qpp=18 b-frames=0", "h265parse config-interval=-1",
			"stream-format=byte-stream,alignment=au",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := base
			s.Codec = c.codec
			desc := PipelineDescription(s)
			for _, w := range c.want {
				if !strings.Contains(desc, w) {
					t.Errorf("pipeline lacks %q:\n%s", w, desc)
				}
			}
		})
	}
}
