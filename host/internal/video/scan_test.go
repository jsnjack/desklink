package video

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

func TestPipelineDescriptionWithScan(t *testing.T) {
	s := Settings{Codec: CodecAV1, Width: 1920, Height: 1200, QP: 18, FPS: 60, NodeID: 7, Scan: func(ScanFrame) {}}
	desc := PipelineDescription(s)
	for _, want := range []string{
		"keepalive-time=100 ! tee name=split ! queue max-size-buffers=2 leaky=downstream ! vapostproc name=post",
		"split. ! queue name=scanqueue max-size-buffers=1 leaky=downstream ! vapostproc add-borders=true ! " +
			"video/x-raw,format=BGRx,width=1920,height=1200,pixel-aspect-ratio=1/1 ! appsink name=scan",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("pipeline lacks %q:\n%s", want, desc)
		}
	}
	s.Scan = nil
	if desc := PipelineDescription(s); strings.Contains(desc, "tee") {
		t.Errorf("a pipeline without scanning must not split the capture:\n%s", desc)
	}
}

// referenceFrame renders one frame of a videotestsrc pattern as BGRx.
func referenceFrame(t *testing.T, caps string) []byte {
	t.Helper()
	el, err := gst.ParseLaunch("videotestsrc num-buffers=1 pattern=colors ! " + caps + " ! appsink name=out")
	if err != nil {
		t.Fatal(err)
	}
	pipeline := el.(gst.Pipeline)
	sink := pipeline.GetByName("out").(gstapp.AppSink)
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		t.Fatal("the reference pipeline did not start")
	}
	defer pipeline.SetState(gst.StateNull)
	f, ok, err := pullFrame(sink)
	if err != nil || !ok {
		t.Fatalf("no reference frame: ok=%v err=%v", ok, err)
	}
	return f.data
}

// TestScanBranchOnGPU runs the encoder with its scan branch on GPU memory,
// as the capture arrives: the scanned frames must hold the source's exact
// pixels and carry the timestamps of the frames the encoder encodes, which
// is how the tile tracker matches them. It needs a VA-API GPU and skips
// without one.
func TestScanBranchOnGPU(t *testing.T) {
	if _, err := os.Stat("/dev/dri/renderD128"); err != nil {
		t.Skip("no render node")
	}
	Init()
	if !Available(CodecAV1) {
		t.Skip("AV1 encoder not available")
	}
	const w, h = 640, 400
	caps := "video/x-raw,format=BGRx,width=640,height=400"
	want := referenceFrame(t, caps)

	s := Settings{Codec: CodecAV1, Width: w, Height: h, QP: 20, FPS: 30}
	src := "videotestsrc is-live=true pattern=colors ! " + caps + ",framerate=30/1 ! vapostproc ! video/x-raw(memory:VAMemory),format=BGRx ! " +
		"tee name=split ! queue max-size-buffers=2 leaky=downstream ! "
	var mu sync.Mutex
	scanned := map[time.Duration]bool{}
	var encoded []time.Duration
	var mismatch string
	enc, err := startWithScan(src+encodeDescription(s)+scanDescription(s), CodecAV1, s.FPS, func(f Frame) {
		mu.Lock()
		defer mu.Unlock()
		encoded = append(encoded, f.PTS)
	}, func(f ScanFrame) {
		mu.Lock()
		defer mu.Unlock()
		scanned[f.PTS] = true
		if f.Width != w || f.Height != h || f.Stride < w*4 {
			mismatch = "frame geometry"
			return
		}
		for y := 0; y < h && mismatch == ""; y++ {
			for x := 0; x < w; x++ {
				got, ref := f.Pix[y*f.Stride+x*4:], want[(y*w+x)*4:]
				if got[0] != ref[0] || got[1] != ref[1] || got[2] != ref[2] {
					mismatch = fmt.Sprintf("pixel (%d,%d) is %v, the source has %v", x, y, got[:3], ref[:3])
					break
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Stop()
	time.Sleep(time.Second)
	enc.SetScanning(false)
	mu.Lock()
	before, encodedBefore := len(scanned), len(encoded)
	mu.Unlock()
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if mismatch != "" {
		t.Fatalf("the scanned frames are not the source's pixels (%s)", mismatch)
	}
	if encodedBefore < 10 {
		t.Fatalf("only %d frames encoded", encodedBefore)
	}
	matched := 0
	for _, pts := range encoded[:encodedBefore] {
		if scanned[pts] {
			matched++
		}
	}
	// The first frames may have reached the encoder before the scan branch
	// had negotiated.
	if matched < encodedBefore-2 {
		t.Errorf("%d of %d encoded frames had a scan with their timestamp", matched, encodedBefore)
	}
	if after := len(scanned); after > before+1 {
		t.Errorf("%d frames were scanned after scanning stopped", after-before)
	}
}
