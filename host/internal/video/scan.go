package video

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0 gstreamer-video-1.0
#include <gst/gst.h>
#include <gst/app/gstappsink.h>
#include <gst/video/video.h>

typedef struct {
	GstSample *sample;
	GstVideoFrame frame;
	GstClockTime pts;
} scanned_frame;

// scan_map pulls one sample and maps it as a video frame: 1 on success, 0 at
// end of stream, -1 on error. scan_release unmaps and releases it.
static int scan_map(GstAppSink *sink, scanned_frame *f) {
	GstVideoInfo info;
	GstBuffer *b;
	f->sample = gst_app_sink_pull_sample(sink);
	if (!f->sample)
		return 0;
	b = gst_sample_get_buffer(f->sample);
	if (!b || !gst_video_info_from_caps(&info, gst_sample_get_caps(f->sample)) ||
	    !gst_video_frame_map(&f->frame, &info, b, GST_MAP_READ)) {
		gst_sample_unref(f->sample);
		return -1;
	}
	f->pts = GST_BUFFER_PTS(b);
	return 1;
}

static void scan_release(scanned_frame *f) {
	gst_video_frame_unmap(&f->frame);
	gst_sample_unref(f->sample);
}

static guint8 *scan_pixels(scanned_frame *f) { return GST_VIDEO_FRAME_PLANE_DATA(&f->frame, 0); }
static gint scan_stride(scanned_frame *f) { return GST_VIDEO_FRAME_PLANE_STRIDE(&f->frame, 0); }
static gint scan_width(scanned_frame *f) { return GST_VIDEO_FRAME_WIDTH(&f->frame); }
static gint scan_height(scanned_frame *f) { return GST_VIDEO_FRAME_HEIGHT(&f->frame); }
*/
import "C"

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// The scan branch hands exact pixels of the captured frames to the tile
// tracker (internal/tiles). A tee after the capture's one queue splits the
// frames, and both branches run in that queue's thread: the encoder's first,
// so the scan never delays an encoded frame, then the scan's vapostproc,
// which scales and letterboxes as the encoder's does, to the stream's size,
// and downloads BGRx to system memory. Two vapostprocs on Mutter's DMA-BUFs
// in two threads crashed Mesa's VA post-processor (radeonsi, Mesa 26.2) within
// a minute in desklinkd 0.50.0, so no VA element may run in another thread.
// The appsinks do not wait to preroll (async=false): without a queue per
// branch, a sink that blocked in preroll would hold the thread the other
// branch needs. At 2560×1600 and 60 frames a second the scan costs about a
// tenth of a CPU core, almost all of it reading the 16 MB frame once to hash
// it.
const (
	splitName    = "split"
	scanPostName = "scanpost"
	scanSinkName = "scan"
)

// ScanFrame is a captured frame at the stream's size as BGRx rows, Stride
// bytes apart. Pix is valid only while the callback runs, which must not
// keep it.
type ScanFrame struct {
	// PTS is the source's timestamp, the same as the encoder's Frame.PTS
	// for this capture.
	PTS           time.Duration
	Width, Height int
	Stride        int
	Pix           []byte
}

// scanDescription is the scan branch, after the encoder's description.
func scanDescription(s Settings) string {
	return fmt.Sprintf(" %s. ! vapostproc name=%s add-borders=true ! "+
		"video/x-raw,format=BGRx,width=%d,height=%d,pixel-aspect-ratio=1/1 ! appsink name=%s sync=false async=false max-buffers=1 drop=false",
		splitName, scanPostName, s.Width, s.Height, scanSinkName)
}

// scanBranch is the running scan branch.
type scanBranch struct {
	limiter *frameLimiter
	// on is cleared to stop scanning without rebuilding the pipeline;
	// frames are then dropped where the branch begins.
	on atomic.Bool
}

// attachScan hooks the scan branch of pipeline to onScan before it plays.
// The branch takes at most fps frames a second, as the encoder does, so
// frames the encoder skips are not scanned either.
func attachScan(pipeline gst.Pipeline, fps int, onScan func(ScanFrame)) (*scanBranch, error) {
	post := pipeline.GetByName(scanPostName)
	sink, ok := pipeline.GetByName(scanSinkName).(gstapp.AppSink)
	if post == nil || !ok {
		return nil, errors.New("pipeline is missing the scan branch")
	}
	b := &scanBranch{limiter: newFrameLimiter(fps)}
	b.on.Store(true)
	// Frames the scan skips are dropped before its vapostproc does any work.
	post.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if !b.on.Load() {
			return gst.PadProbeDrop
		}
		if pts, ok := probePTS(info); ok && !b.limiter.allow(pts) {
			return gst.PadProbeDrop
		}
		return gst.PadProbeOK
	})
	sink.SetCallbacks(&gstapp.AppSinkCallbacks{NewSample: func(sink gstapp.AppSink) gst.FlowReturn {
		switch withScanFrame(sink, onScan) {
		case 0:
			return gst.FlowEOS
		case -1:
			return gst.FlowError
		}
		return gst.FlowOK
	}})
	return b, nil
}

// withScanFrame pulls one frame from sink and passes it to f while it is
// mapped, then releases it at once (see buffers.go for why nothing may hold
// a buffer); it returns scan_map's result.
func withScanFrame(sink gstapp.AppSink, f func(ScanFrame)) int {
	var c C.scanned_frame
	r := C.scan_map((*C.GstAppSink)(gstapp.UnsafeAppSinkToGlibNone(sink)), &c)
	if r != 1 {
		return int(r)
	}
	defer C.scan_release(&c)
	stride, height := int(C.scan_stride(&c)), int(C.scan_height(&c))
	pts := time.Duration(-1)
	if c.pts != ^C.GstClockTime(0) { // GST_CLOCK_TIME_NONE
		pts = time.Duration(c.pts)
	}
	f(ScanFrame{
		PTS: pts, Width: int(C.scan_width(&c)), Height: height, Stride: stride,
		Pix: unsafe.Slice((*byte)(unsafe.Pointer(C.scan_pixels(&c))), stride*height),
	})
	return 1
}

// SetScanning turns the scan branch on or off; it does nothing without one.
func (e *Encoder) SetScanning(on bool) {
	if e.scan != nil {
		e.scan.on.Store(on)
	}
}
