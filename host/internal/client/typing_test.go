package client

import (
	"context"
	"slices"
	"testing"
	"time"

	dlnet "desklink/internal/net"
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

// TestInputAfterComposedTextFollowsIt: a key sent right after a composed
// text reaches the host after the text, at least textLead later, because
// the host holds keys back only behind text it has received; and neither
// call blocks the GTK thread.
func TestInputAfterComposedTextFollowsIt(t *testing.T) {
	h := startFakeHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ev := newRecordedEvents()
	s, err := Connect(ctx, pairedProfile(t, ctx, h), Request{Codec: "av1"}, func(EncodedFrame) {}, ev)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wait(t, ev.ready)
	start := time.Now()
	if err := s.TypeText("é"); err != nil {
		t.Fatal(err)
	}
	s.SendInput(dlnet.InputEvent{Kind: dlnet.InputKey, Flags: 1, Code: 57})
	if took := time.Since(start); took >= textLead {
		t.Errorf("queueing the text and the key took %v", took)
	}
	text, key := wait(t, h.arrivals), wait(t, h.arrivals)
	if text.what != "text" || key.what != "input" {
		t.Fatalf("the host got %s then %s, want the text first", text.what, key.what)
	}
	if gap := key.at.Sub(text.at); gap < textLead/2 {
		t.Errorf("the key came %v after the text, want about %v", gap, textLead)
	}
	// Once the text is out, input goes at once again.
	wait(t, h.input)
	time.Sleep(2 * textLead)
	sent := time.Now()
	s.SendInput(dlnet.InputEvent{Kind: dlnet.InputKey, Code: 57})
	if a := wait(t, h.arrivals); a.what != "input" || a.at.Sub(sent) >= textLead {
		t.Errorf("input after the lead arrived %v after it was sent, want at once", a.at.Sub(sent))
	}
}
