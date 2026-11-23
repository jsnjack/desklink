package viewer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"

	"desklink/internal/client"
	"desklink/internal/video"
)

// encodeTestFrames produces 10-bit AV1 temporal units from a test pattern
// on the GPU.
func encodeTestFrames(t *testing.T) [][]byte {
	t.Helper()
	// 10-bit like the host's stream, so the decoder's format conversion is covered.
	el, err := gst.ParseLaunch("videotestsrc num-buffers=20 pattern=smpte ! video/x-raw,width=640,height=400 ! " +
		"vapostproc ! video/x-raw(memory:VAMemory),format=P010_10LE ! vaav1enc ! av1parse ! " +
		"video/x-av1,stream-format=obu-stream,alignment=tu ! appsink name=sink sync=false")
	if err != nil {
		t.Fatal(err)
	}
	pipeline := el.(gst.Pipeline)
	sink := pipeline.GetByName("sink").(gstapp.AppSink)
	pipeline.SetState(gst.StatePlaying)
	defer pipeline.SetState(gst.StateNull)
	var frames [][]byte
	for {
		sample := sink.PullSample()
		if sample == nil {
			break
		}
		info, ok := sample.GetBuffer().Map(gst.MapRead)
		if !ok {
			t.Fatal("map encoded buffer")
		}
		frames = append(frames, append([]byte(nil), info.Data()...))
		info.Unmap()
	}
	return frames
}

// TestWindowSmoke builds the window and decodes real AV1 frames into the
// stream picture. Run it through scripts/headless-gui.sh (make smoke).
func TestWindowSmoke(t *testing.T) {
	if os.Getenv("DESKLINK_UI_SMOKE") == "" {
		t.Skip("set DESKLINK_UI_SMOKE=1 and run through scripts/headless-gui.sh")
	}
	video.Init()
	store, err := client.OpenStore(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("Second", "10.0.0.2", 9443); err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	if video.Available(video.CodecAV1) {
		frames = encodeTestFrames(t)
	}

	var rows, width int
	var decodeErr error
	app := adw.NewApplication(AppID+".Smoke", gio.ApplicationNonUnique)
	app.ConnectActivate(func() {
		w := newWindow(app, Options{Store: store, DeviceName: "smoke"})
		w.win.Present()
		rows = len(w.rows)
		if len(frames) == 0 {
			app.Quit()
			return
		}
		w.stack.SetVisibleChildName(pageStream)
		d, err := NewDecoder("av1", func() {}, func(err error) { decodeErr = err })
		if err != nil {
			decodeErr = err
			app.Quit()
			return
		}
		w.stream.picture.SetPaintable(d.Paintable())
		go func() {
			for _, f := range frames {
				d.Push(client.EncodedFrame{Data: f})
			}
		}()
		deadline := time.Now().Add(5 * time.Second)
		glib.TimeoutAdd(100, func() bool {
			width = d.Paintable().IntrinsicWidth()
			if width > 0 || time.Now().After(deadline) {
				d.Close()
				app.Quit()
				return false
			}
			return true
		})
	})
	if code := app.Run(nil); code != 0 {
		t.Fatalf("application exited with %d", code)
	}
	if rows != 2 {
		t.Errorf("%d host rows, want 2", rows)
	}
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(frames) > 0 && width != 640 {
		t.Errorf("decoded picture is %d px wide, want 640", width)
	}
}
