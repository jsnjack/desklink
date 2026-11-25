package net

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

// testPNG is a PNG signature followed by size-8 more bytes.
func testPNG(size int) []byte {
	b := make([]byte, size)
	copy(b, pngSignature)
	for i := len(pngSignature); i < size; i++ {
		b[i] = byte(i * 7)
	}
	return b
}

func TestClipboardImagePartsSplitAt512KiB(t *testing.T) {
	png := testPNG(2*ClipboardImagePart + 1)
	parts := ClipboardImageParts(png)
	wantSizes := []int{ClipboardImagePart, ClipboardImagePart, 1}
	if len(parts) != len(wantSizes) {
		t.Fatalf("%d parts, want %d", len(parts), len(wantSizes))
	}
	var joined []byte
	for i, p := range parts {
		if p.Part != i || p.Parts != len(wantSizes) || len(p.Image) != wantSizes[i] || p.Text != "" {
			t.Errorf("part %d: Part %d Parts %d %d bytes text %q", i, p.Part, p.Parts, len(p.Image), p.Text)
		}
		joined = append(joined, p.Image...)
	}
	if !bytes.Equal(joined, png) {
		t.Error("the parts do not join up to the image")
	}
}

func TestImageAssembler(t *testing.T) {
	small := testPNG(100)
	three := testPNG(2*ClipboardImagePart + 100)
	other := testPNG(ClipboardImagePart + 5)
	notPNG := bytes.Repeat([]byte{1}, 20)
	cases := []struct {
		name  string
		limit int
		feed  []Clipboard
		// want is the image the last part completes, nil for none;
		// dropped is how many parts report a dropped image.
		want    []byte
		dropped int
	}{
		{"a one-part image comes back at once", DefaultClipboardImageLimit, ClipboardImageParts(small), small, 0},
		{"a three-part image comes back after its last part", DefaultClipboardImageLimit, ClipboardImageParts(three), three, 0},
		{"a new first part starts the image over", DefaultClipboardImageLimit,
			append(ClipboardImageParts(three)[:2:2], ClipboardImageParts(other)...), other, 0},
		{"a skipped part drops the image", DefaultClipboardImageLimit,
			[]Clipboard{ClipboardImageParts(three)[0], ClipboardImageParts(three)[2]}, nil, 1},
		{"parts after a dropped image wait for the next first part", DefaultClipboardImageLimit,
			[]Clipboard{ClipboardImageParts(three)[0], ClipboardImageParts(three)[2], ClipboardImageParts(three)[1]}, nil, 2},
		{"a part without its first part is dropped", DefaultClipboardImageLimit, ClipboardImageParts(three)[1:], nil, 2},
		{"a changed part count drops the image", DefaultClipboardImageLimit,
			[]Clipboard{ClipboardImageParts(three)[0], {Image: three[ClipboardImagePart : 2*ClipboardImagePart], Part: 1, Parts: 2}}, nil, 1},
		{"more parts than the limit allows drop the image at once", ClipboardImagePart, ClipboardImageParts(other)[:1], nil, 1},
		{"more bytes than the limit drop the image", ClipboardImagePart + 1, ClipboardImageParts(other), nil, 1},
		{"an image that is not a PNG is dropped", DefaultClipboardImageLimit, ClipboardImageParts(notPNG), nil, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := ImageAssembler{Limit: c.limit}
			var got []byte
			dropped := 0
			for i, part := range c.feed {
				png, err := a.Add(part)
				switch {
				case errors.Is(err, ErrClipboardImageDropped):
					dropped++
				case err != nil:
					t.Fatalf("part %d: %v", i, err)
				case png != nil && i != len(c.feed)-1:
					t.Fatalf("an image came back at part %d of %d", i, len(c.feed))
				}
				got = png
			}
			if !bytes.Equal(got, c.want) {
				t.Errorf("got %d bytes, want %d", len(got), len(c.want))
			}
			if dropped != c.dropped {
				t.Errorf("%d parts dropped the image, want %d", dropped, c.dropped)
			}
		})
	}
}

func TestClipboardPrefersImage(t *testing.T) {
	cases := []struct {
		name  string
		types []string
		want  bool
	}{
		{"a screenshot", []string{"image/png"}, true},
		{"an image copied in a browser, with HTML that links to it", []string{"text/html", "image/png"}, true},
		{"a spreadsheet cell, which also offers a picture of itself", []string{"text/plain;charset=utf-8", "image/png", "image/bmp"}, false},
		{"text from an X11 app", []string{"UTF8_STRING", "image/png"}, false},
		{"plain text", []string{"text/plain"}, false},
		{"an image in another format only", []string{"image/jpeg"}, false},
		{"an empty clipboard", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClipboardPrefersImage(c.types); got != c.want {
				t.Errorf("ClipboardPrefersImage(%q) = %v, want %v", c.types, got, c.want)
			}
		})
	}
}

// TestClipboardMessagesMatchSharedVectors pins the JSON that ProtocolTest.kt
// decodes: text as before, and an image part with base64 bytes.
func TestClipboardMessagesMatchSharedVectors(t *testing.T) {
	cases := []struct {
		name string
		msg  Clipboard
		want string
	}{
		{"text", Clipboard{Text: "hello"}, `{"text":"hello"}`},
		{"the first part of a two-part image", Clipboard{Image: pngSignature, Part: 0, Parts: 2}, `{"text":"","image":"iVBORw0KGgo=","parts":2}`},
		{"the second part", Clipboard{Image: []byte{0, 0, 0, 0}, Part: 1, Parts: 2}, `{"text":"","image":"AAAAAA==","part":1,"parts":2}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.msg)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != c.want {
				t.Errorf("encoded %s, want %s", raw, c.want)
			}
			// Clients send defaults explicitly; the host reads both forms.
			var back Clipboard
			if err := json.Unmarshal([]byte(`{"text":"`+c.msg.Text+`","image":null,"part":0,"parts":0}`), &back); err != nil || back.IsImage() {
				t.Errorf("a text message with explicit defaults decoded as %+v, %v", back, err)
			}
			if err := json.Unmarshal(raw, &back); err != nil {
				t.Fatal(err)
			}
			if back.Text != c.msg.Text || !bytes.Equal(back.Image, c.msg.Image) || back.Part != c.msg.Part || back.Parts != c.msg.Parts {
				t.Errorf("decoded %+v, want %+v", back, c.msg)
			}
		})
	}
}
