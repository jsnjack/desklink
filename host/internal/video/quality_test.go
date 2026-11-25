package video

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Unix(0, 0)

const ms = time.Millisecond

func at(d time.Duration) time.Time { return t0.Add(d) }

// run feeds one report every 250 ms from start for dur, with the loss given
// by lossAt for each report index, and returns every change it caused.
func run(q *QualityController, start time.Time, dur time.Duration, lossAt func(i int) float64) (time.Time, []Decision) {
	var changes []Decision
	now := start
	for i := 0; now.Sub(start) < dur; i++ {
		if d := q.Observe(now, lossAt(i), 0); d.LevelChanged || d.QPChanged {
			changes = append(changes, d)
		}
		now = now.Add(250 * ms)
	}
	return now, changes
}

// runBusy is run with a changing screen: an encoded frame of 2 KB before
// every report.
func runBusy(q *QualityController, start time.Time, dur time.Duration, lossAt func(i int) float64) (time.Time, []Decision) {
	var changes []Decision
	now := start
	for i := 0; now.Sub(start) < dur; i++ {
		q.NoteFrame(now, 2048, false)
		if d := q.Observe(now, lossAt(i), 0); d.LevelChanged || d.QPChanged {
			changes = append(changes, d)
		}
		now = now.Add(250 * ms)
	}
	return now, changes
}

func constant(loss float64) func(int) float64 { return func(int) float64 { return loss } }

// manual is a one-level controller as manual presets get, between the
// default quantizer limits.
func manual(requested int) *QualityController {
	return NewQualityController(QualityConfig{Requested: requested, MinQP: 12, MaxQP: 38})
}

func auto(start int) *QualityController {
	return NewQualityController(QualityConfig{Requested: 12, MinQP: 12, MaxQP: 38, Levels: Levels(2560, 1600, 120), Start: start, Auto: true})
}

func TestQualityControllerQuantizer(t *testing.T) {
	type sample struct {
		at      time.Duration
		loss    float64
		rtt     time.Duration
		wantQP  int
		changed bool
	}
	lossy := func(from, to time.Duration, loss float64, qp int) []sample {
		var out []sample
		for d := from; d < to; d += 250 * ms {
			out = append(out, sample{d, loss, 0, qp, false})
		}
		return out
	}
	cases := []struct {
		name    string
		samples []sample
	}{
		{"a second of loss is not yet a quantizer step", lossy(0, 1500*ms, 5, 18)},
		{"two seconds of loss raise the quantizer to the middle of the range", append(lossy(0, 2*time.Second, 5, 18),
			sample{2 * time.Second, 5, 0, 28, true})},
		{"the next step waits for its own two seconds after the keyframe's grace", append(append(append(lossy(0, 2*time.Second, 5, 18),
			sample{2 * time.Second, 5, 0, 28, true}), lossy(2250*ms, 5*time.Second, 5, 28)...),
			sample{5 * time.Second, 5, 0, 38, true})},
		{"never above the ceiling", append(lossy(0, 2*time.Second, 5, 18), append([]sample{{2 * time.Second, 5, 0, 28, true}},
			append(lossy(2250*ms, 5*time.Second, 5, 28), append([]sample{{5 * time.Second, 5, 0, 38, true}},
				lossy(5250*ms, 20*time.Second, 5, 38)...)...)...)...)},
		{"one late RTT sample is not congestion", []sample{
			{0, 0, 30 * ms, 18, false}, {2 * time.Second, 0, 75 * ms, 18, false}, {4 * time.Second, 0, 30 * ms, 18, false},
			{6 * time.Second, 0, 0, 18, false},
		}},
		{"a steady long path is not congestion", []sample{
			{0, 0, 90 * ms, 18, false}, {2 * time.Second, 0, 95 * ms, 18, false}, {4 * time.Second, 0, 92 * ms, 18, false},
		}},
		{"never below the requested value", []sample{{0, 0, 0, 18, false}, {30 * time.Second, 0, 0, 18, false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := manual(18)
			for i, s := range c.samples {
				d := q.Observe(at(s.at), s.loss, s.rtt)
				if d.QP != s.wantQP || d.QPChanged != s.changed || d.LevelChanged {
					t.Fatalf("sample %d at %v: %+v, want qp %d changed %v", i, s.at, d, s.wantQP, s.changed)
				}
			}
		})
	}
	t.Run("two late RTT samples in a row are congestion once they last two seconds", func(t *testing.T) {
		q := manual(18)
		rtts := map[int]time.Duration{0: 30 * ms, 8: 75 * ms, 16: 80 * ms, 24: 80 * ms}
		var changes []Decision
		now := t0
		for i := 0; i <= 24; i++ {
			if d := q.Observe(now, 0, rtts[i]); d.QPChanged {
				changes = append(changes, d)
			}
			now = now.Add(250 * ms)
		}
		// Delay counts from the second late sample at 4 s; two seconds later
		// the quantizer steps.
		if len(changes) != 1 || changes[0].QP != 28 {
			t.Errorf("changes %+v, want one step to 28 at 6 s", changes)
		}
	})
	t.Run("the requested value is clamped to the range", func(t *testing.T) {
		if qp := manual(40).QP(); qp != 38 {
			t.Errorf("qp %d, want 38", qp)
		}
		if qp := manual(8).QP(); qp != 12 {
			t.Errorf("qp %d, want the configured minimum 12", qp)
		}
	})
}

// raised returns a manual controller whose quantizer went up to the middle
// step with two seconds of loss, and the time of the next report.
func raised(t *testing.T) (*QualityController, time.Time) {
	t.Helper()
	q := manual(18)
	now, changes := run(q, t0, 2250*ms, constant(5))
	if len(changes) != 1 || q.QP() != 28 {
		t.Fatalf("setup: changes %+v", changes)
	}
	return q, now
}

func TestQualityControllerSharpensWhenStill(t *testing.T) {
	t.Run("the quantizer returns to the floor in one step once clean and still", func(t *testing.T) {
		q, now := raised(t)
		_, changes := run(q, now, 5*time.Second, constant(0))
		if len(changes) != 0 {
			t.Fatalf("returned before five clean seconds: %+v", changes)
		}
		_, changes = run(q, now.Add(5*time.Second), 250*ms, constant(0))
		if len(changes) != 1 || changes[0].QP != 18 {
			t.Errorf("changes %+v, want one step back to 18", changes)
		}
	})
	t.Run("a changing screen delays the return until twenty clean seconds", func(t *testing.T) {
		q, now := raised(t)
		_, changes := runBusy(q, now, 20*time.Second, constant(0))
		if len(changes) != 0 {
			t.Fatalf("returned while the screen changed: %+v", changes)
		}
		_, changes = runBusy(q, now.Add(20*time.Second), 250*ms, constant(0))
		if len(changes) != 1 || changes[0].QP != 18 {
			t.Errorf("changes %+v, want the return after twenty clean seconds", changes)
		}
	})
	t.Run("the return waits for a second without changes", func(t *testing.T) {
		q, now := raised(t)
		end, changes := runBusy(q, now, 6*time.Second, constant(0))
		if len(changes) != 0 {
			t.Fatalf("returned while the screen changed: %+v", changes)
		}
		// The last change on screen was at end-250ms; a second later the
		// screen counts as still.
		_, changes = run(q, end, 750*ms, constant(0))
		if len(changes) != 0 {
			t.Fatalf("returned before the screen was still for a second: %+v", changes)
		}
		_, changes = run(q, end.Add(750*ms), 250*ms, constant(0))
		if len(changes) != 1 || changes[0].QP != 18 {
			t.Errorf("changes %+v, want the return once still", changes)
		}
	})
	t.Run("keyframes and unchanged frames do not count as changes on screen", func(t *testing.T) {
		q, now := raised(t)
		for i := 0; i < 21; i++ {
			q.NoteFrame(now, 300_000, true)
			q.NoteFrame(now, 35, false)
			q.Observe(now, 0, 0)
			now = now.Add(250 * ms)
		}
		if q.QP() != 18 {
			t.Errorf("qp %d, want the return after five clean seconds", q.QP())
		}
	})
	t.Run("loss right after the return undoes it and locks returns out", func(t *testing.T) {
		q, now := raised(t)
		now, _ = run(q, now, 5250*ms, constant(0)) // returns at the last report
		if q.QP() != 18 {
			t.Fatalf("setup: qp %d", q.QP())
		}
		// The keyframe's grace lasts a second; loss for a second after it
		// undoes the return.
		now, changes := run(q, now.Add(750*ms), 1250*ms, constant(5))
		if len(changes) != 1 || changes[0].QP != 28 {
			t.Fatalf("changes %+v, want the return undone", changes)
		}
		if _, changes = run(q, now, 55*time.Second, constant(0)); len(changes) != 0 {
			t.Errorf("returned again during the lockout: %+v", changes)
		}
		if _, changes = run(q, now.Add(55*time.Second), 10*time.Second, constant(0)); len(changes) != 1 || changes[0].QP != 18 {
			t.Errorf("changes %+v, want a return once the minute's lockout ended", changes)
		}
	})
	t.Run("loss during the return's own keyframe does not undo it", func(t *testing.T) {
		q, now := raised(t)
		now, _ = run(q, now, 5250*ms, constant(0))
		// Three lossy reports inside the second of grace.
		_, changes := run(q, now, 20*time.Second, func(i int) float64 {
			if i < 3 {
				return 10
			}
			return 0
		})
		if len(changes) != 0 || q.QP() != 18 {
			t.Errorf("changes %+v, qp %d: the return's own burst undid it", changes, q.QP())
		}
	})
}

func TestLevels(t *testing.T) {
	cases := []struct {
		name      string
		w, h, fps int
		want      []Level
	}{
		{"tablet at 120 fps keeps its size down to 30 fps", 2560, 1600, 120,
			[]Level{{2560, 1600, 120}, {2560, 1600, 60}, {2560, 1600, 30}, {1920, 1200, 30}, {1280, 800, 30}}},
		{"60 fps maximum skips the 120 fps step", 1920, 1200, 60, []Level{{1920, 1200, 60}, {1920, 1200, 30}, {1280, 800, 30}}},
		{"16:9 keeps its aspect", 2560, 1440, 60, []Level{{2560, 1440, 60}, {2560, 1440, 30}, {1920, 1080, 30}, {1280, 720, 30}}},
		{"a 24 fps maximum only changes size", 1920, 1200, 24, []Level{{1920, 1200, 24}, {1280, 800, 24}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Levels(c.w, c.h, c.fps); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestLevelForAndStartLevel(t *testing.T) {
	tablet := Levels(2560, 1600, 120)
	laptop := Levels(2560, 1600, 60)
	cases := []struct {
		name     string
		levels   []Level
		want     Level
		idx      int
		startsAt int
	}{
		{"exact rung", tablet, Level{1920, 1200, 30}, 3, 1},
		{"top", tablet, Level{2560, 1600, 120}, 0, 0},
		{"60 fps at full size", tablet, Level{2560, 1600, 60}, 1, 1},
		{"a rung of the old ladder", tablet, Level{1920, 1200, 60}, 3, 1},
		{"smaller than every rung", tablet, Level{640, 400, 30}, 4, 1},
		{"a laptop's lowest rung starts at full size", laptop, Level{1280, 800, 30}, 3, 1},
		{"a one-level ladder", tablet[:1], Level{1280, 800, 30}, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LevelFor(c.levels, c.want); got != c.idx {
				t.Errorf("LevelFor(%v) = %d, want %d", c.want, got, c.idx)
			}
			if got := StartLevel(c.levels, c.want); got != c.startsAt {
				t.Errorf("StartLevel(%v) = %d, want %d", c.want, got, c.startsAt)
			}
		})
	}
}

// kinds describes changes for comparisons and failure messages: "level n",
// "qp n", or both.
func kinds(changes []Decision) []string {
	out := make([]string, 0, len(changes))
	for _, d := range changes {
		switch {
		case d.LevelChanged && d.QPChanged:
			out = append(out, fmt.Sprintf("level %d qp %d", d.Level, d.QP))
		case d.LevelChanged:
			out = append(out, fmt.Sprintf("level %d", d.Level))
		default:
			out = append(out, fmt.Sprintf("qp %d", d.QP))
		}
	}
	return out
}

func TestQualityControllerLadder(t *testing.T) {
	t.Run("sustained loss lowers the frame rate, then the quantizer, then the size", func(t *testing.T) {
		q := auto(0)
		_, changes := run(q, t0, 60*time.Second, constant(5))
		want := []string{"level 1", "level 2", "qp 25", "qp 38", "level 3", "level 4"}
		if got := kinds(changes); !reflect.DeepEqual(got, want) {
			t.Fatalf("changes %v, want %v", got, want)
		}
		if l := q.Levels()[q.Level()]; l != (Level{1280, 800, 30}) || q.QP() != 38 {
			t.Errorf("ended at %v qp %d", l, q.QP())
		}
	})
	t.Run("the full size gets three seconds at the coarsest quantizer before a size step", func(t *testing.T) {
		q := auto(0)
		// The quantizer reaches 38 at 7.5 s; its keyframe's grace ends at
		// 8.5 s, so the size holds until 11.5 s.
		_, changes := run(q, t0, 11250*ms, constant(5))
		for _, d := range changes {
			if !q.Levels()[d.Level].sameSize(q.Levels()[0]) {
				t.Fatalf("size stepped down too early: %v", kinds(changes))
			}
		}
		if q.QP() != 38 {
			t.Errorf("qp %d, want the ceiling first", q.QP())
		}
	})
	t.Run("bursty loss averaging the threshold steps down", func(t *testing.T) {
		patterns := map[string]func(int) float64{
			"3 and 1 alternating":  func(i int) float64 { return []float64{3, 1}[i%2] },
			"10 and 1 alternating": func(i int) float64 { return []float64{10, 1}[i%2] },
			"twelve 5 then one 1":  func(i int) float64 { return map[bool]float64{true: 1, false: 5}[i%13 == 12] },
		}
		for name, p := range patterns {
			q := auto(0)
			if _, changes := run(q, t0, 60*time.Second, p); len(changes) < 3 {
				t.Errorf("%s: changes %v", name, kinds(changes))
			}
		}
	})
	t.Run("manual quality never changes level", func(t *testing.T) {
		q := NewQualityController(QualityConfig{Requested: 18, MinQP: 12, MaxQP: 38, Levels: Levels(2560, 1600, 120)})
		_, changes := run(q, t0, 60*time.Second, constant(5))
		for _, d := range changes {
			if d.LevelChanged {
				t.Fatalf("manual controller changed level: %v", kinds(changes))
			}
		}
		if q.QP() != 38 {
			t.Errorf("qp %d, want the ceiling", q.QP())
		}
	})
	t.Run("a clean connection climbs back in the reverse order", func(t *testing.T) {
		q := auto(4)
		// One upward move every fifteen clean seconds.
		_, changes := run(q, t0, 65*time.Second, constant(0))
		want := []string{"level 3", "level 2", "level 1", "level 0"}
		if got := kinds(changes); !reflect.DeepEqual(got, want) {
			t.Fatalf("changes %v, want %v", got, want)
		}
	})
	t.Run("after congestion the size comes back before the quantizer and the quantizer before the frame rate", func(t *testing.T) {
		q := auto(0)
		now, _ := run(q, t0, 30*time.Second, constant(5))
		_, changes := run(q, now, 120*time.Second, constant(0))
		want := []string{"level 3", "level 2", "qp 12", "level 1", "level 0"}
		if got := kinds(changes); !reflect.DeepEqual(got, want) {
			t.Fatalf("changes %v, want %v", got, want)
		}
	})
	t.Run("a failed probe steps back and locks probing out", func(t *testing.T) {
		q := auto(3)
		// The size probe comes after 15 clean seconds; its restart's grace
		// lasts until 17 s.
		now, changes := run(q, t0, 17*time.Second, constant(0))
		if got := kinds(changes); !reflect.DeepEqual(got, []string{"level 2"}) {
			t.Fatalf("changes %v, want a probe to level 2", got)
		}
		now, changes = run(q, now, 1250*ms, constant(5))
		if got := kinds(changes); !reflect.DeepEqual(got, []string{"level 3"}) {
			t.Fatalf("changes %v, want a step back to level 3", got)
		}
		if _, changes = run(q, now, 55*time.Second, constant(0)); len(changes) != 0 {
			t.Errorf("probed again during the lockout: %v", kinds(changes))
		}
	})
	t.Run("a second failure doubles the lockout", func(t *testing.T) {
		q := auto(3)
		now, _ := run(q, t0, 17*time.Second, constant(0))
		now, _ = run(q, now, 1250*ms, constant(5)) // first failure: a minute
		now, changes := run(q, now, 61*time.Second, constant(0))
		if got := kinds(changes); !reflect.DeepEqual(got, []string{"level 2"}) {
			t.Fatalf("changes %v, want the second probe after the minute", got)
		}
		now, _ = run(q, now.Add(2*time.Second), 1250*ms, constant(5)) // second failure: two minutes
		if q.Level() != 3 {
			t.Fatalf("level %d, want the second probe undone", q.Level())
		}
		if _, changes = run(q, now, 115*time.Second, constant(0)); len(changes) != 0 {
			t.Errorf("probed again within two minutes: %v", kinds(changes))
		}
	})
	t.Run("congestion during a probe's own restart does not fail it", func(t *testing.T) {
		q := auto(3)
		now, _ := run(q, t0, 15250*ms, constant(0))
		if q.Level() != 2 {
			t.Fatalf("setup: level %d", q.Level())
		}
		// Loss for the 1.5 s the new pipeline's keyframe takes, then clean.
		_, changes := run(q, now, 20*time.Second, func(i int) float64 {
			if i < 6 {
				return 10
			}
			return 0
		})
		for _, d := range changes {
			if d.Level > 2 {
				t.Fatalf("the probe failed on its own keyframe: %v", kinds(changes))
			}
		}
	})
	t.Run("one late RTT echo does not fail a probe", func(t *testing.T) {
		q := auto(1)
		now := t0
		for i := 0; i < 64; i++ { // 16 s clean, RTT every 2 s
			rtt := time.Duration(0)
			if i%8 == 0 {
				rtt = 10 * ms
			}
			q.Observe(now, 0, rtt)
			now = now.Add(250 * ms)
		}
		if q.Level() != 0 {
			t.Fatalf("no probe up, level %d", q.Level())
		}
		q.Observe(now, 0, 65*ms)
		for i := 1; i < 8; i++ {
			now = now.Add(250 * ms)
			q.Observe(now, 0, 0)
		}
		if q.Level() != 0 {
			t.Errorf("one late echo failed the probe: level %d", q.Level())
		}
	})
	t.Run("a brief burst long after the last loss does not step down", func(t *testing.T) {
		q := auto(0)
		// Two lossy reports at the start, then a 750 ms burst at 10.5 s.
		// Neither fills the shortest window with enough loss.
		burst := func(i int) float64 {
			if i < 2 || (i >= 42 && i < 45) {
				return 10
			}
			return 0
		}
		if _, changes := run(q, t0, 20*time.Second, burst); len(changes) != 0 {
			t.Errorf("stepped down on a brief burst: %v", kinds(changes))
		}
	})
	t.Run("the start level is clamped to the ladder", func(t *testing.T) {
		if l := auto(9).Level(); l != len(Levels(2560, 1600, 120))-1 {
			t.Errorf("level %d", l)
		}
	})
}

// TestQualityControllerFromTheCeiling covers controllers whose quantizer
// cannot rise because the requested value is already the maximum, so the
// frame rate and the size are the only ways to shed load.
func TestQualityControllerFromTheCeiling(t *testing.T) {
	cases := []struct {
		name  string
		start func() *QualityController
	}{
		{"sustained loss steps down when the requested quantizer is the maximum", func() *QualityController {
			return NewQualityController(QualityConfig{Requested: 18, MinQP: 12, MaxQP: 18, Levels: Levels(2560, 1600, 120), Auto: true})
		}},
		{"sustained loss steps down when the minimum equals the maximum", func() *QualityController {
			return NewQualityController(QualityConfig{Requested: 18, MinQP: 18, MaxQP: 18, Levels: Levels(2560, 1600, 120), Auto: true})
		}},
		{"sustained loss steps down after a reset to the maximum", func() *QualityController {
			q := auto(0)
			q.Reset(18, 12, 18)
			return q
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := c.start()
			_, changes := run(q, t0, 30*time.Second, constant(10))
			want := []string{"level 1", "level 2", "level 3", "level 4"}
			if got := kinds(changes); !reflect.DeepEqual(got, want) {
				t.Fatalf("changes %v, want %v", got, want)
			}
			if q.QP() != 18 {
				t.Errorf("the quantizer left its only value, got %d", q.QP())
			}
		})
	}
}

func TestQualityControllerFixed(t *testing.T) {
	fixed := func(requested int) *QualityController {
		return NewQualityController(QualityConfig{Requested: requested, MinQP: 16, MaxQP: 38, Levels: Levels(2560, 1600, 120), Auto: true, Fixed: true})
	}
	t.Run("a fixed session keeps its quantizer and level whatever the link does", func(t *testing.T) {
		q := fixed(12)
		_, changes := run(q, t0, 60*time.Second, constant(10))
		if len(changes) != 0 || q.QP() != 12 || q.Level() != 0 || q.Auto() {
			t.Errorf("changes %v, qp %d, level %d, auto %v", kinds(changes), q.QP(), q.Level(), q.Auto())
		}
	})
	t.Run("a fixed session may go below the configured minimum down to the floor", func(t *testing.T) {
		cases := []struct{ requested, want int }{{12, 12}, {8, 8}, {4, FixedMinQP}, {60, 38}}
		for _, c := range cases {
			if qp := fixed(c.requested).QP(); qp != c.want {
				t.Errorf("requested %d: qp %d, want %d", c.requested, qp, c.want)
			}
		}
	})
	t.Run("a reset keeps a fixed session fixed", func(t *testing.T) {
		q := fixed(12)
		q.Reset(10, 16, 38)
		if _, changes := run(q, t0, 30*time.Second, constant(10)); len(changes) != 0 || q.QP() != 10 {
			t.Errorf("changes %v, qp %d", kinds(changes), q.QP())
		}
	})
}
