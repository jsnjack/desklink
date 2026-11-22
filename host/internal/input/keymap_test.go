package input

import "testing"

func TestValidKeyAndName(t *testing.T) {
	cases := []struct {
		code  uint16
		valid bool
		name  string
	}{
		{0, false, "KEY_0"},
		{1, true, "KEY_ESC"},
		{29, true, "KEY_LEFTCTRL"},
		{125, true, "KEY_LEFTMETA"},
		{240, true, "KEY_240"},
		{248, true, "KEY_248"},
		{249, false, "KEY_249"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if ValidKey(c.code) != c.valid {
				t.Errorf("ValidKey(%d) = %v", c.code, !c.valid)
			}
			if got := KeyName(c.code); got != c.name {
				t.Errorf("KeyName(%d) = %s", c.code, got)
			}
		})
	}
}

func TestCopyDetector(t *testing.T) {
	type step struct {
		code uint16
		down bool
		want bool
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"ctrl+c", []step{{KeyLeftCtrl, true, false}, {KeyC, true, true}}},
		{"plain c", []step{{KeyC, true, false}}},
		{"right ctrl+x", []step{{KeyRightCtrl, true, false}, {KeyX, true, true}}},
		{"ctrl+shift+c in a terminal", []step{{KeyLeftCtrl, true, false}, {42, true, false}, {KeyC, true, true}}},
		{"ctrl+insert", []step{{KeyLeftCtrl, true, false}, {KeyInsert, true, true}}},
		{"c after ctrl was released", []step{{KeyLeftCtrl, true, false}, {KeyLeftCtrl, false, false}, {KeyC, true, false}}},
		{"key release does not count", []step{{KeyLeftCtrl, true, false}, {KeyC, false, false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var d CopyDetector
			for i, s := range c.steps {
				if got := d.Observe(s.code, s.down); got != s.want {
					t.Errorf("step %d: got %v, want %v", i, got, s.want)
				}
			}
		})
	}
}
