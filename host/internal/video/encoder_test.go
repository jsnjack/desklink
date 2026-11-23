package video

import (
	"os"
	"sync"
	"testing"
	"time"
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
