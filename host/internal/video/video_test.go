package video

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFrameLimiter(t *testing.T) {
	cases := []struct {
		name     string
		fps      int
		gaps     []time.Duration
		wantKept int
	}{
		{"a 120 fps variable-rate source capped at 60", 60, repeat(time.Second/120, 120), 60},
		{"a source at exactly the cap passes", 60, repeat(time.Second/60, 60), 60},
		{"a slow static screen passes", 60, repeat(100*time.Millisecond, 10), 10},
		{"bursts are thinned", 30, append(repeat(time.Millisecond, 20), repeat(40*time.Millisecond, 10)...), 11},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newFrameLimiter(c.fps)
			pts, kept := time.Duration(0), 0
			for _, g := range c.gaps {
				pts += g
				if l.allow(pts) {
					kept++
				}
			}
			if diff := kept - c.wantKept; diff < -1 || diff > 1 {
				t.Errorf("kept %d of %d frames, want about %d", kept, len(c.gaps), c.wantKept)
			}
		})
	}
	t.Run("a restarted stream is let through", func(t *testing.T) {
		l := newFrameLimiter(60)
		l.allow(10 * time.Second)
		if !l.allow(time.Millisecond) {
			t.Error("a timestamp going backwards was dropped")
		}
	})
}

func repeat(d time.Duration, n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = d
	}
	return out
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

func TestPipelineDescriptionCaptureSize(t *testing.T) {
	mirror := PipelineDescription(Settings{Codec: CodecAV1, Width: 1920, Height: 1200, QP: 18, FPS: 60, NodeID: 7})
	if strings.Contains(mirror, "memory:DMABuf") {
		t.Errorf("a monitor capture must not fix the stream size:\n%s", mirror)
	}
	virtual := PipelineDescription(Settings{
		Codec: CodecAV1, Width: 1920, Height: 1200, QP: 18, FPS: 60, NodeID: 7,
		CaptureWidth: 2560, CaptureHeight: 1600, CaptureFPS: 120,
	})
	for _, want := range []string{
		"pipewiresrc path=7 do-timestamp=true keepalive-time=100 ! video/x-raw(memory:DMABuf),width=2560,height=1600,max-framerate=120/1 ! queue",
		"format=P010_10LE,width=1920,height=1200",
	} {
		if !strings.Contains(virtual, want) {
			t.Errorf("virtual capture lacks %q:\n%s", want, virtual)
		}
	}
}

func TestPipelineDescription(t *testing.T) {
	base := Settings{Width: 2560, Height: 1600, QP: 18, FPS: 120, NodeID: 42}
	cases := []struct {
		name  string
		codec Codec
		want  []string
	}{
		{"av1", CodecAV1, []string{
			"pipewiresrc path=42", "vapostproc name=post",
			"format=P010_10LE,width=2560,height=1600,pixel-aspect-ratio=1/1", "colorimetry=(string)2:3:7:1",
			"vaav1enc name=enc rate-control=cqp qp=90 key-int-max=1024", "palette-mode=true target-usage=2",
			"av1parse", "stream-format=obu-stream,alignment=tu", "drop=false",
		}},
		{"hevc", CodecHEVC, []string{
			"vah265enc name=enc rate-control=cqp qpi=18 qpp=18 b-frames=0 key-int-max=1024", "h265parse config-interval=-1",
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

// TestQualityControllerLadderFromTheCeiling covers controllers whose
// quantizer cannot rise because the requested value is already the maximum,
// so stepping down the ladder is the only way to shed load.
