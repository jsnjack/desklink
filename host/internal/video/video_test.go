package video

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQualityControllerQP(t *testing.T) {
	t0 := time.Unix(0, 0)
	ms := time.Millisecond
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
		{"loss raises the quantizer by two", 18, []sample{{0, 3, 10 * ms, 20, true}}},
		{"queueing above the baseline raises it", 18, []sample{{0, 0, 30 * ms, 18, false}, {time.Second, 0, 75 * ms, 20, true}}},
		{"a steady long path is not congestion", 18, []sample{{0, 0, 90 * ms, 18, false}, {time.Second, 0, 95 * ms, 18, false}}},
		{"a cooldown limits raises to one a second", 18, []sample{
			{0, 5, 0, 20, true}, {500 * ms, 5, 0, 20, false}, {time.Second, 5, 0, 22, true},
		}},
		{"never above the ceiling", 25, []sample{{0, 5, 0, 26, true}, {2 * time.Second, 5, 0, 26, false}}},
		{"recovers one step after five clean seconds", 18, []sample{
			{0, 5, 0, 20, true}, {time.Second, 0, 10 * ms, 20, false},
			{5900 * ms, 0, 10 * ms, 20, false}, {6 * time.Second, 0, 10 * ms, 19, true},
		}},
		{"a middling sample restarts the clean period", 18, []sample{
			{0, 5, 0, 20, true}, {time.Second, 0, 0, 20, false}, {4 * time.Second, 1, 0, 20, false}, {6 * time.Second, 0, 0, 20, false},
		}},
		{"never below the requested value", 18, []sample{{0, 0, 0, 18, false}, {10 * time.Second, 0, 0, 18, false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := NewQualityController(c.requested, 16, 26, nil, 0, false)
			for i, s := range c.samples {
				d := q.Observe(t0.Add(s.at), s.loss, s.rtt)
				if d.QP != s.wantQP || d.QPChanged != s.changed || d.LevelChanged {
					t.Errorf("sample %d: %+v, want qp %d changed %v", i, d, s.wantQP, s.changed)
				}
			}
		})
	}
	t.Run("requested value is clamped to the range", func(t *testing.T) {
		if qp := NewQualityController(40, 16, 26, nil, 0, false).QP(); qp != 26 {
			t.Errorf("qp %d", qp)
		}
	})
}

func TestLevels(t *testing.T) {
	cases := []struct {
		name      string
		w, h, fps int
		want      []Level
	}{
		{"tablet at 120 fps", 2560, 1600, 120, []Level{{2560, 1600, 120}, {2560, 1600, 60}, {1920, 1200, 60}, {1280, 800, 60}}},
		{"60 fps maximum skips the frame-rate step", 1920, 1200, 60, []Level{{1920, 1200, 60}, {1280, 800, 60}}},
		{"16:9 keeps its aspect", 2560, 1440, 60, []Level{{2560, 1440, 60}, {1920, 1080, 60}, {1280, 720, 60}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Levels(c.w, c.h, c.fps); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// run feeds one sample every 250 ms from start for dur and returns the last
// decision with a level change, if any.
func run(q *QualityController, start time.Time, dur time.Duration, loss float64) (time.Time, []Decision) {
	var changes []Decision
	t := start
	for ; t.Sub(start) < dur; t = t.Add(250 * time.Millisecond) {
		if d := q.Observe(t, loss, 0); d.LevelChanged {
			changes = append(changes, d)
		}
	}
	return t, changes
}

func TestQualityControllerLadder(t *testing.T) {
	levels := Levels(2560, 1600, 120)
	t0 := time.Unix(0, 0)
	t.Run("sustained loss walks down the ladder once the quantizer is maxed", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 0, true)
		_, changes := run(q, t0, 30*time.Second, 5)
		if len(changes) < 2 || changes[0].Level != 1 || changes[1].Level != 2 {
			t.Fatalf("level changes %+v", changes)
		}
		if changes[0].QP != 22 {
			t.Errorf("after stepping down the quantizer restarts mid-range, got %d", changes[0].QP)
		}
	})
	t.Run("manual quality never changes level", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 0, false)
		if _, changes := run(q, t0, 60*time.Second, 5); len(changes) != 0 {
			t.Errorf("manual controller changed level: %+v", changes)
		}
	})
	t.Run("a clean connection probes one level up", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 2, true)
		_, changes := run(q, t0, 60*time.Second, 0)
		if len(changes) == 0 || changes[0].Level != 1 {
			t.Fatalf("no probe up: %+v", changes)
		}
	})
	t.Run("a failed probe steps back and locks probing out", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 2, true)
		// The probe happens at 20 s; its window lasts until 35 s.
		now, changes := run(q, t0, 25*time.Second, 0)
		if len(changes) == 0 || changes[len(changes)-1].Level != 1 {
			t.Fatalf("expected a probe to level 1, got %+v", changes)
		}
		now, changes = run(q, now, 2*time.Second, 5)
		if len(changes) != 1 || changes[0].Level != 2 {
			t.Fatalf("expected a step back to level 2, got %+v", changes)
		}
		if _, changes = run(q, now, 90*time.Second, 0); len(changes) != 0 {
			t.Errorf("probed again during the lockout: %+v", changes)
		}
	})
	t.Run("the start level is clamped", func(t *testing.T) {
		if l := NewQualityController(18, 16, 26, levels, 9, true).Level(); l != len(levels)-1 {
			t.Errorf("level %d", l)
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
