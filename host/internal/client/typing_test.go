package client

import (
	"slices"
	"testing"
)

func TestSplitText(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{"short text stays whole", "hé", 3, []string{"hé"}},
		{"text of exactly the limit stays whole", "héj", 3, []string{"héj"}},
		{"longer text is cut by characters, not bytes", "ééééé", 2, []string{"éé", "éé", "é"}},
		{"an emoji is never cut", "a😀b😀", 3, []string{"a😀b", "😀"}},
		{"no text sends nothing", "", 3, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := splitText(c.text, c.limit); !slices.Equal(got, c.want) {
				t.Errorf("splitText(%q, %d) = %q, want %q", c.text, c.limit, got, c.want)
			}
		})
	}
}
