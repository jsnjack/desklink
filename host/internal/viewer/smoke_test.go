package viewer

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	// A paired host whose name is Pango markup, on an address nothing
	// answers at.
	paired, err := store.Add("<b>Desk</b> & co", "192.0.2.1", 9443)
	if err != nil {
		t.Fatal(err)
	}
	paired.PeerID, paired.PeerKey, paired.Fingerprint = "peer", []byte{1}, strings.Repeat("ab", 32)
	if err := store.Update(paired); err != nil {
		t.Fatal(err)
	}
	stranger := client.Nearby{Name: "DeskLink on <i>stranger</i>", IP: "192.0.2.50", Port: 9443, Fingerprint: strings.Repeat("cd", 32)}
	var frames [][]byte
	if video.Available(video.CodecAV1) {
		frames = encodeTestFrames(t)
	}

	var rows, markupRows, nearbyRows, hostsAfterPairNearby, width int
	var decodeErr error
	shown := make(chan uint64, 64)
	app := adw.NewApplication(AppID+".Smoke", gio.ApplicationNonUnique)
	app.ConnectActivate(func() {
		w := newWindow(app, Options{Store: store, DeviceName: "smoke"})
		w.win.Present()
		rows = len(w.rows)
		// An unknown host is listed; announcing a paired host's
		// certificate at a new address only starts a pinned check there.
		w.onDiscovered(stranger)
		w.onDiscovered(client.Nearby{Name: "DeskLink on desk", IP: "192.0.2.99", Port: 9443, Fingerprint: paired.Fingerprint})
		nearbyRows = len(w.nearbyRows)
		for _, r := range append(slices.Clone(w.rows), w.nearbyRows...) {
			if r.(*adw.ActionRow).UseMarkup() {
				markupRows++
			}
		}
		// Pair on a found host only opens the code dialog.
		w.pairNearby(stranger)
		hostsAfterPairNearby = len(store.Hosts())
		if len(frames) == 0 {
			app.Quit()
			return
		}
		w.stack.SetVisibleChildName(pageStream)
		d, err := NewDecoder("av1", func() {}, func(err error) { decodeErr = err }, func(c uint64) {
			select {
			case shown <- c:
			default: // enough to check; never block the streaming thread
			}
		})
		if err != nil {
			decodeErr = err
			app.Quit()
			return
		}
		w.stream.picture.SetPaintable(d.Paintable())
		go func() {
			for i, f := range frames {
				d.Push(client.EncodedFrame{Data: f, CaptureMicros: uint64(i + 1)})
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
	if rows != 3 {
		t.Errorf("%d host rows, want 3", rows)
	}
	if nearbyRows != 1 {
		t.Errorf("%d rows under Found on this network, want 1", nearbyRows)
	}
	if markupRows != 0 {
		t.Errorf("%d rows parse Pango markup in names from the network", markupRows)
	}
	if hostsAfterPairNearby != 3 {
		t.Errorf("%d hosts saved after Pair on a found host, want 3: nothing is saved before the code", hostsAfterPairNearby)
	}
	if got, _ := store.Host(paired.ID); slices.Contains(got.Candidates(), "192.0.2.99") {
		t.Error("saved an announced address the paired host never answered at")
	}
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(frames) > 0 && width != 640 {
		t.Errorf("decoded picture is %d px wide, want 640", width)
	}
	// Decoded frames reach the sink with the capture times they were pushed
	// with, which the delay in the stats needs.
	if len(frames) > 0 {
		close(shown)
		n := 0
		for c := range shown {
			if c < 1 || c > uint64(len(frames)) {
				t.Errorf("frame shown with capture time %d, pushed 1..%d", c, len(frames))
			}
			n++
		}
		if n == 0 {
			t.Error("no decoded frame was matched to its capture time")
		}
	}
}
