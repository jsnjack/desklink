package net

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"

	"desklink/internal/config"
)

const (
	// ClipboardImagePart is the most PNG bytes one clipboard message
	// carries. A large image goes as several messages, so the WebSocket's
	// pings and pongs get through between them: a single 10 MB message on a
	// slow link would hold them back until the tablet gave up on the
	// connection.
	ClipboardImagePart = 512 << 10
	// DefaultClipboardImageLimit is the largest clipboard image the host
	// and the clients take unless configured otherwise.
	DefaultClipboardImageLimit = config.DefaultMaxClipboardImageBytes
)

// pngSignature starts every PNG file.
var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// IsPNG reports whether data starts with the PNG signature.
func IsPNG(data []byte) bool { return bytes.HasPrefix(data, pngSignature) }

// MIMETypePNG is the clipboard type images are read and written in.
const MIMETypePNG = "image/png"

// plainTextTypes are the clipboard types that hold plain text.
var plainTextTypes = []string{"UTF8_STRING", "STRING", "TEXT"}

// ClipboardPrefersImage reports whether a clipboard that offers these MIME
// types is synced as an image: it offers a PNG and no plain text. Copying a
// cell or a file offers both, and its text is what the other side wants;
// copying an image in a browser offers a PNG and HTML that only links to it,
// so HTML alone does not count as text.
func ClipboardPrefersImage(types []string) bool {
	if !slices.Contains(types, MIMETypePNG) {
		return false
	}
	for _, t := range types {
		if t == "text/plain" || strings.HasPrefix(t, "text/plain;") || slices.Contains(plainTextTypes, t) {
			return false
		}
	}
	return true
}

// ClipboardImageParts splits a PNG into the clipboard messages that carry
// it, in the order they are sent.
func ClipboardImageParts(png []byte) []Clipboard {
	n := (len(png) + ClipboardImagePart - 1) / ClipboardImagePart
	parts := make([]Clipboard, 0, n)
	for i := range n {
		end := min((i+1)*ClipboardImagePart, len(png))
		parts = append(parts, Clipboard{Image: png[i*ClipboardImagePart : end], Part: i, Parts: n})
	}
	return parts
}

// ErrClipboardImageDropped means the image a clipboard message belonged to
// was dropped: its parts came out of order, it was larger than the limit,
// or it was not a PNG.
var ErrClipboardImageDropped = errors.New("clipboard image dropped")

// ImageAssembler joins the parts of clipboard images into PNGs of at most
// Limit bytes. The WebSocket delivers messages in order, so the parts of an
// image arrive one after the other; anything else drops the image, and the
// assembler waits for the next part 0. It is not safe for concurrent use.
type ImageAssembler struct {
	Limit int

	buf   []byte
	next  int
	parts int
}

// Add takes one image part. It returns the PNG once its last part arrived,
// nil while parts are missing, and ErrClipboardImageDropped when it dropped
// the image this part belongs to.
func (a *ImageAssembler) Add(c Clipboard) ([]byte, error) {
	maxParts := (a.Limit + ClipboardImagePart - 1) / ClipboardImagePart
	if c.Part == 0 {
		a.reset()
		a.parts = c.Parts
	}
	switch {
	case a.parts == 0:
		return nil, fmt.Errorf("%w: part %d without its first part", ErrClipboardImageDropped, c.Part)
	case c.Parts != a.parts || c.Part != a.next:
		a.reset()
		return nil, fmt.Errorf("%w: part %d of %d out of order", ErrClipboardImageDropped, c.Part, c.Parts)
	case c.Parts > maxParts || len(a.buf)+len(c.Image) > a.Limit:
		a.reset()
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrClipboardImageDropped, a.Limit)
	}
	a.buf = append(a.buf, c.Image...)
	a.next++
	if a.next < a.parts {
		return nil, nil
	}
	png := a.buf
	a.reset()
	if !IsPNG(png) {
		return nil, fmt.Errorf("%w: not a PNG", ErrClipboardImageDropped)
	}
	return png, nil
}

func (a *ImageAssembler) reset() {
	a.buf, a.next, a.parts = nil, 0, 0
}
