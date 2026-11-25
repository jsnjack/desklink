package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

// pairedProfile pairs with the fake host and returns its profile.
func pairedProfile(t *testing.T, ctx context.Context, h *fakeHost) Profile {
	t.Helper()
	ip, port := h.addr.Addr().String(), int(h.addr.Port())
	pin, _, err := h.pairer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Pair(ctx, ip, port, pin, h.fp, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	return Profile{ID: "x", IP: ip, Port: port, PeerID: res.PeerID, Fingerprint: res.Fingerprint, PeerKey: res.PeerKey}
}

// imageOf is a PNG signature followed by size-8 more bytes.
func imageOf(size int) []byte {
	b := bytes.Repeat([]byte{3}, size)
	copy(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	return b
}

func TestClipboardImagesBetweenLaptopAndHost(t *testing.T) {
	h := startFakeHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ev := newRecordedEvents()
	s, err := Connect(ctx, pairedProfile(t, ctx, h), Request{Codec: "av1", ClipboardImages: dlnet.DefaultClipboardImageLimit}, func(EncodedFrame) {}, ev)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if hello := wait(t, h.hello); hello.ClipboardImages != dlnet.DefaultClipboardImageLimit {
		t.Errorf("hello says the laptop takes %d-byte images, want %d", hello.ClipboardImages, dlnet.DefaultClipboardImageLimit)
	}
	if info := wait(t, ev.info); info.ClipboardImages != fakeImageLimit {
		t.Fatalf("session says the host takes %d-byte images, want %d", info.ClipboardImages, fakeImageLimit)
	}

	t.Run("an image goes to the host in parts and comes back whole", func(t *testing.T) {
		png := imageOf(2*dlnet.ClipboardImagePart + 10)
		if err := s.PushClipboardImage(png); err != nil {
			t.Fatal(err)
		}
		for i := range 3 {
			m := wait(t, h.control)
			var c dlnet.Clipboard
			if m.Type != dlnet.MsgClipboard || json.Unmarshal(m.Payload, &c) != nil || c.Part != i || c.Parts != 3 {
				t.Fatalf("host got %q part %d of %d, want clipboard part %d of 3", m.Type, c.Part, c.Parts, i)
			}
		}
		if got := wait(t, ev.images); !bytes.Equal(got, png) {
			t.Errorf("the image came back as %d bytes, want %d", len(got), len(png))
		}
	})
	t.Run("an image over the host's limit is refused before sending", func(t *testing.T) {
		if err := s.PushClipboardImage(imageOf(fakeImageLimit + 1)); !errors.Is(err, ErrClipboardImageTooLarge) {
			t.Errorf("err %v, want ErrClipboardImageTooLarge", err)
		}
	})
	t.Run("only PNGs are sent", func(t *testing.T) {
		if err := s.PushClipboardImage([]byte("GIF89a")); err == nil {
			t.Error("a GIF was sent")
		}
	})
}

func TestClipboardImagesNeedTheHostsSupport(t *testing.T) {
	h := startFakeHost(t)
	h.textOnly.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ev := newRecordedEvents()
	s, err := Connect(ctx, pairedProfile(t, ctx, h), Request{Codec: "av1", ClipboardImages: dlnet.DefaultClipboardImageLimit}, func(EncodedFrame) {}, ev)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wait(t, ev.info)
	if err := s.PushClipboardImage(imageOf(100)); !errors.Is(err, ErrClipboardImagesUnsupported) {
		t.Errorf("err %v, want ErrClipboardImagesUnsupported", err)
	}
}

func TestSendFileSavesItOnTheHost(t *testing.T) {
	h := startFakeHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := pairedProfile(t, ctx, h)
	cases := []struct {
		name string
		size int64
	}{
		{"with its length", 11},
		{"without its length", -1},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sent atomic.Int64
			saved, err := SendFile(ctx, p, "notes.txt", strings.NewReader("hello world"), c.size, func(n int64) { sent.Store(n) })
			if err != nil {
				t.Fatal(err)
			}
			want := "notes.txt"
			if i > 0 {
				want = "notes (1).txt"
			}
			if saved != want {
				t.Errorf("saved as %q, want %q", saved, want)
			}
			if got, err := os.ReadFile(filepath.Join(h.uploads, saved)); err != nil || string(got) != "hello world" {
				t.Errorf("the host holds %q, %v", got, err)
			}
			if sent.Load() != 11 {
				t.Errorf("progress reached %d bytes, want 11", sent.Load())
			}
		})
	}
	t.Run("a file over the host's limit comes back with the host's reason", func(t *testing.T) {
		_, err := SendFile(ctx, p, "big.bin", bytes.NewReader(make([]byte, 1<<20+1)), 1<<20+1, nil)
		if err == nil || !strings.Contains(err.Error(), "larger than this computer accepts") {
			t.Errorf("err %v, want the host's reason", err)
		}
	})
}

func TestUploadResult(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		want     string
		wantErr  string
		sentinel error
	}{
		{"a saved file", http.StatusOK, `{"name":"a (1).txt","size":3}`, "a (1).txt", "", nil},
		{"a refused file", http.StatusConflict, `{"error":"another file from this device is still being received"}`, "", "another file from this device", nil},
		{"a rejected token", http.StatusUnauthorized, "unauthorized\n", "", "pair again", nil},
		{"an old host", http.StatusNotFound, "404 page not found\n", "", "", ErrUploadUnsupported},
		{"a reply that is not JSON", http.StatusBadGateway, "<html>", "", "HTTP 502", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := uploadResult(c.status, strings.NewReader(c.body))
			if got != c.want {
				t.Errorf("name %q, want %q", got, c.want)
			}
			switch {
			case c.sentinel != nil && !errors.Is(err, c.sentinel):
				t.Errorf("err %v, want %v", err, c.sentinel)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Errorf("err %v, want one containing %q", err, c.wantErr)
			case c.want != "" && err != nil:
				t.Errorf("err %v", err)
			}
		})
	}
}
