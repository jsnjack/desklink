package desktop

import "testing"

func TestParseMouseSettings(t *testing.T) {
	cases := []struct {
		name, speed, profile string
		want                 MouseSettings
	}{
		{"the desk's slower adaptive mouse", "-0.29565217391304344", "'default'", MouseSettings{Speed: -0.29565217391304344}},
		{"a flat profile", "0.5", "'flat'", MouseSettings{Speed: 0.5, Flat: true}},
		{"a custom curve counts as adaptive", "0.0", "'custom'", MouseSettings{}},
		{"unreadable settings keep the defaults", "", "", MouseSettings{}},
		{"a speed out of range is clamped", "3", "'adaptive'", MouseSettings{Speed: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseMouseSettings(c.speed, c.profile); got != c.want {
				t.Errorf("parseMouseSettings(%q, %q) = %+v, want %+v", c.speed, c.profile, got, c.want)
			}
		})
	}
}
