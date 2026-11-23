package video

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// TestEncoderOnGPU runs the real encode pipeline with a test source in place
// of PipeWire. It needs a VA-API GPU and skips without one.
func TestEncoderOnGPU(t *testing.T) {
	if _, err := os.Stat("/dev/dri/renderD128"); err != nil {
		t.Skip("no render node")
	}
	Init()
	for _, codec := range []Codec{CodecAV1, CodecHEVC} {
		t.Run(string(codec), func(t *testing.T) {
			if !Available(codec) {
				t.Skipf("%s encoder not available", codec)
			}
			var mu sync.Mutex
			var frames, keyframes int
			var oldest time.Duration
			keyAfterForce := make(chan struct{}, 1)
			forced := false
			src := "videotestsrc is-live=true pattern=smpte ! video/x-raw,width=640,height=400,framerate=60/1 ! "
			enc, err := start(src+encodeDescription(Settings{Codec: codec, Width: 640, Height: 400, QP: 20, FPS: 60}), codec, 60, func(f Frame) {
				mu.Lock()
				defer mu.Unlock()
				frames++
				if len(f.Data) == 0 {
					t.Error("empty frame")
				}
				// Captured comes from the source's timestamp: before the
				// frame was encoded, but not long before.
				age := time.Since(f.Captured)
				if age < 0 || age > maxFrameAge {
					t.Errorf("frame captured %v ago", age)
				}
				oldest = max(oldest, age)
				if f.Keyframe {
					keyframes++
					if forced {
						select {
						case keyAfterForce <- struct{}{}:
						default:
						}
					}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer enc.Stop()
			time.Sleep(300 * time.Millisecond)
			enc.SetQP(26)
			mu.Lock()
			forced = true
			mu.Unlock()
			if !enc.ForceKeyframe() {
				t.Error("force-key-unit event was not handled")
			}
			select {
			case <-keyAfterForce:
			case err := <-enc.Errors():
				t.Fatal(err)
			case <-time.After(2 * time.Second):
				t.Fatal("no keyframe after ForceKeyframe")
			}
			mu.Lock()
			if frames < 5 || keyframes < 2 {
				t.Errorf("%d frames, %d keyframes", frames, keyframes)
			}
			if oldest < 100*time.Microsecond {
				t.Errorf("oldest frame captured %v before delivery: the source's timestamps were not used", oldest)
			}
			t.Logf("oldest frame captured %v before delivery", oldest)
			frames = 0
			mu.Unlock()

			enc.SetMaxFPS(20)
			time.Sleep(time.Second)
			mu.Lock()
			defer mu.Unlock()
			// The 60 fps source capped at 20 fps; allow for timing slack.
			if frames < 10 || frames > 30 {
				t.Errorf("%d frames in a second with a 20 fps cap", frames)
			}
		})
	}
}

// TestConversionKeepsDesktopColours runs the stream's colour conversion on
// the GPU and checks that a flat colour keeps its brightness: the luma must
// be what the BT.709 matrix gives for the desktop's sRGB values, with no
// transfer curve applied on top. It needs a VA-API GPU and skips without one.
func TestConversionKeepsDesktopColours(t *testing.T) {
	if _, err := os.Stat("/dev/dri/renderD128"); err != nil {
		t.Skip("no render node")
	}
	Init()
	cases := []struct {
		name    string
		r, g, b uint8
	}{
		{"black", 0, 0, 0},
		{"near black", 20, 20, 20},
		{"dark theme background", 28, 28, 28},
		{"dark grey", 40, 40, 40},
		{"grey", 128, 128, 128},
		{"white", 255, 255, 255},
		{"adwaita blue", 0x35, 0x84, 0xe4},
		{"tan", 200, 120, 80},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Settings{Width: 640, Height: 400}
			desc := fmt.Sprintf("videotestsrc num-buffers=1 pattern=solid-color foreground-color=0xff%02x%02x%02x ! "+
				"video/x-raw,format=BGRx,width=640,height=400,colorimetry=sRGB ! ", c.r, c.g, c.b) +
				convertDescription(s) +
				// A second vapostproc with the same colours only copies the frame to memory.
				fmt.Sprintf("vapostproc ! video/x-raw,format=P010_10LE,width=640,height=400,colorimetry=(string)%s ! appsink name=out", streamColorimetry)
			el, err := gst.ParseLaunch(desc)
			if err != nil {
				t.Fatal(err)
			}
			pipeline := el.(gst.Pipeline)
			sink, ok := pipeline.GetByName("out").(gstapp.AppSink)
			if !ok {
				t.Fatal("no appsink")
			}
			if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
				t.Fatal("the pipeline did not start")
			}
			defer pipeline.SetState(gst.StateNull)
			f, ok, err := pullFrame(sink)
			if err != nil || !ok {
				t.Fatalf("no frame: ok=%v err=%v", ok, err)
			}
			// P010 keeps each 10-bit sample in the top bits of 16; the first
			// two bytes are the luma of the top-left pixel.
			luma := int(binary.LittleEndian.Uint16(f.data)) >> 6
			want := 64 + 876*(0.2126*float64(c.r)+0.7152*float64(c.g)+0.0722*float64(c.b))/255
			if math.Abs(float64(luma)-want) > 6 {
				t.Errorf("luma %d, want %.0f: the conversion changed the desktop's brightness", luma, want)
			} else {
				t.Logf("luma %d, want %.0f", luma, want)
			}
		})
	}
}
