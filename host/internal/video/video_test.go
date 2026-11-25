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
		{"one late RTT sample is not congestion", 18, []sample{{0, 0, 30 * ms, 18, false}, {time.Second, 0, 75 * ms, 18, false}}},
		{"two late RTT samples in a row are", 18, []sample{
			{0, 0, 30 * ms, 18, false}, {time.Second, 0, 75 * ms, 18, false}, {2 * time.Second, 0, 80 * ms, 20, true},
		}},
		{"reports between RTT samples keep the delay state", 18, []sample{
			{0, 0, 30 * ms, 18, false}, {time.Second, 0, 75 * ms, 18, false}, {2 * time.Second, 0, 80 * ms, 20, true},
			{3 * time.Second, 0, 0, 22, true},
		}},
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

func TestLevelForAndMaxFPS(t *testing.T) {
	levels := Levels(2560, 1600, 120)
	cases := []struct {
		name string
		want Level
		idx  int
	}{
		{"exact rung", Level{1920, 1200, 60}, 2},
		{"top", Level{2560, 1600, 120}, 0},
		{"remembered from a 60 Hz laptop ladder", Level{2560, 1600, 60}, 1},
		{"smaller than every rung", Level{640, 400, 30}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LevelFor(levels, c.want); got != c.idx {
				t.Errorf("LevelFor(%v) = %d, want %d", c.want, got, c.idx)
			}
		})
	}
}

// run feeds one sample every 250 ms from start for dur, with the loss given
// by lossAt for each report index, and returns the level changes.
func run(q *QualityController, start time.Time, dur time.Duration, lossAt func(i int) float64) (time.Time, []Decision) {
	var changes []Decision
	t := start
	for i := 0; t.Sub(start) < dur; i++ {
		if d := q.Observe(t, lossAt(i), 0); d.LevelChanged {
			changes = append(changes, d)
		}
		t = t.Add(250 * time.Millisecond)
	}
	return t, changes
}

func constant(loss float64) func(int) float64 { return func(int) float64 { return loss } }

func TestQualityControllerLadder(t *testing.T) {
	levels := Levels(2560, 1600, 120)
	t0 := time.Unix(0, 0)
	t.Run("sustained loss walks down the ladder once the quantizer is maxed", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 0, true)
		_, changes := run(q, t0, 30*time.Second, constant(5))
		if len(changes) < 2 || changes[0].Level != 1 || changes[1].Level != 2 {
			t.Fatalf("level changes %+v", changes)
		}
		if changes[0].QP != 22 {
			t.Errorf("after stepping down the quantizer restarts mid-range, got %d", changes[0].QP)
		}
	})
	t.Run("the quantizer gets three seconds at its ceiling first", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 0, true)
		// QP reaches 26 at 3 s, so no step before 6 s.
		if _, changes := run(q, t0, 5900*time.Millisecond, constant(5)); len(changes) != 0 {
			t.Fatalf("stepped down too early: %+v", changes)
		}
	})
	t.Run("bursty loss averaging the threshold steps down", func(t *testing.T) {
		patterns := map[string]func(int) float64{
			"3 and 1 alternating":  func(i int) float64 { return []float64{3, 1}[i%2] },
			"10 and 1 alternating": func(i int) float64 { return []float64{10, 1}[i%2] },
			"twelve 5 then one 1":  func(i int) float64 { return map[bool]float64{true: 1, false: 5}[i%13 == 12] },
		}
		for name, p := range patterns {
			q := NewQualityController(18, 16, 26, levels, 0, true)
			if _, changes := run(q, t0, 60*time.Second, p); len(changes) == 0 {
				t.Errorf("%s: never stepped down", name)
			}
		}
	})
	t.Run("manual quality never changes level", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 0, false)
		if _, changes := run(q, t0, 60*time.Second, constant(5)); len(changes) != 0 {
			t.Errorf("manual controller changed level: %+v", changes)
		}
	})
	t.Run("a clean connection probes one level up", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 2, true)
		_, changes := run(q, t0, 60*time.Second, constant(0))
		if len(changes) == 0 || changes[0].Level != 1 {
			t.Fatalf("no probe up: %+v", changes)
		}
	})
	t.Run("a failed probe steps back and locks probing out", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 2, true)
		// The probe happens at 20 s; its window lasts until 35 s.
		now, changes := run(q, t0, 25*time.Second, constant(0))
		if len(changes) == 0 || changes[len(changes)-1].Level != 1 {
			t.Fatalf("expected a probe to level 1, got %+v", changes)
		}
		now, changes = run(q, now, 2*time.Second, constant(5))
		if len(changes) != 1 || changes[0].Level != 2 {
			t.Fatalf("expected a step back to level 2, got %+v", changes)
		}
		if _, changes = run(q, now, 90*time.Second, constant(0)); len(changes) != 0 {
			t.Errorf("probed again during the lockout: %+v", changes)
		}
	})
	t.Run("one late RTT echo does not fail a probe", func(t *testing.T) {
		q := NewQualityController(18, 16, 26, levels, 2, true)
		now := t0
		for i := 0; i < 100; i++ { // 25 s clean, RTT every 2 s
			rtt := time.Duration(0)
			if i%8 == 0 {
				rtt = 10 * time.Millisecond
			}
			q.Observe(now, 0, rtt)
			now = now.Add(250 * time.Millisecond)
		}
		if q.Level() != 1 {
			t.Fatalf("no probe up, level %d", q.Level())
		}
		q.Observe(now, 0, 65*time.Millisecond)
		for i := 1; i < 8; i++ {
			now = now.Add(250 * time.Millisecond)
			q.Observe(now, 0, 0)
		}
		if q.Level() != 1 {
			t.Errorf("one late echo failed the probe: level %d", q.Level())
		}
	})
	t.Run("the start level is clamped", func(t *testing.T) {
		if l := NewQualityController(18, 16, 26, levels, 9, true).Level(); l != len(levels)-1 {
			t.Errorf("level %d", l)
		}
	})
}

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

// TestQualityControllerLadderFromTheCeiling covers controllers whose
// quantizer cannot rise because the requested value is already the maximum,
// so stepping down the ladder is the only way to shed load.
func TestQualityControllerLadderFromTheCeiling(t *testing.T) {
	levels := Levels(2560, 1600, 120)
	t0 := time.Unix(0, 0)
	cases := []struct {
		name string
		// start returns a controller whose quantizer is at its ceiling at t0.
		start func() *QualityController
	}{
		{"sustained loss steps down when the requested quantizer is the maximum", func() *QualityController {
			return NewQualityController(18, 16, 18, levels, 0, true)
		}},
		{"sustained loss steps down when the minimum equals the maximum", func() *QualityController {
			return NewQualityController(18, 18, 18, levels, 0, true)
		}},
		{"sustained loss steps down after a reset to the maximum", func() *QualityController {
			q := NewQualityController(18, 16, 26, levels, 0, true)
			run(q, t0.Add(-2*time.Second), 2*time.Second, constant(10))
			q.Reset(18, 16, 18)
			return q
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := c.start()
			// The quantizer is at its ceiling from the first lossy report at
			// t0, so the first step comes three seconds later.
			now, changes := run(q, t0, 2900*time.Millisecond, constant(10))
			if len(changes) != 0 {
				t.Fatalf("stepped down too early: %+v", changes)
			}
			_, changes = run(q, now, 60*time.Second, constant(10))
			if len(changes) < 2 || changes[0].Level != 1 || changes[1].Level != 2 {
				t.Fatalf("level changes %+v", changes)
			}
			if changes[0].QP != 18 {
				t.Errorf("the quantizer left its only value, got %d", changes[0].QP)
			}
		})
	}
	t.Run("a brief burst long after the last loss does not step down", func(t *testing.T) {
		q := NewQualityController(18, 18, 18, levels, 0, true)
		// Two lossy reports at the start, then a 750 ms burst at 10.5 s.
		// The burst alone never fills three seconds with enough loss.
		burst := func(i int) float64 {
			if i < 2 || (i >= 42 && i < 45) {
				return 10
			}
			return 0
		}
		if _, changes := run(q, t0, 20*time.Second, burst); len(changes) != 0 {
			t.Errorf("stepped down on a brief burst: %+v", changes)
		}
	})
}
