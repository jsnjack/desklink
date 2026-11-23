package video

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0
#include <gst/gst.h>
#include <gst/app/gstappsink.h>

// probe_pts is the timestamp of the buffer a pad probe sees. It takes no
// reference, so the buffer goes back to its pool as soon as the pipeline is
// done with it.
static GstClockTime probe_pts(GstPadProbeInfo *info) {
	GstBuffer *b = gst_pad_probe_info_get_buffer(info);
	return b ? GST_BUFFER_PTS(b) : GST_CLOCK_TIME_NONE;
}

typedef struct {
	GstSample *sample;
	GstBuffer *buffer;
	GstMapInfo map;
	gboolean keyframe;
} pulled_frame;

// pull_frame pulls one sample and maps its buffer for reading: 1 on success,
// 0 at end of stream, -1 on error. pull_release unmaps and releases it.
static int pull_frame(GstAppSink *sink, pulled_frame *f) {
	f->sample = gst_app_sink_pull_sample(sink);
	if (!f->sample)
		return 0;
	f->buffer = gst_sample_get_buffer(f->sample);
	if (!f->buffer || !gst_buffer_map(f->buffer, &f->map, GST_MAP_READ)) {
		gst_sample_unref(f->sample);
		return -1;
	}
	f->keyframe = !GST_BUFFER_FLAG_IS_SET(f->buffer, GST_BUFFER_FLAG_DELTA_UNIT);
	return 1;
}

static void pull_release(pulled_frame *f) {
	gst_buffer_unmap(f->buffer, &f->map);
	gst_sample_unref(f->sample);
}
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// The go-gst wrappers for buffers and samples hold a reference until Go's
// garbage collector finalizes them. On the capture path that starves
// Mutter: it shares a handful of buffers with the pipeline and records no
// new frame until one comes back, so the stream froze for seconds. These
// helpers read what they need through C and release at once.

var errPull = errors.New("appsink returned a sample without readable data")

// probePTS returns the timestamp of the buffer a pad probe sees.
func probePTS(info *gst.PadProbeInfo) (time.Duration, bool) {
	pts := C.probe_pts((*C.GstPadProbeInfo)(gst.UnsafePadProbeInfoToGlibNone(info)))
	if pts == ^C.GstClockTime(0) { // GST_CLOCK_TIME_NONE
		return 0, false
	}
	return time.Duration(pts), true
}

// pullFrame pulls one encoded frame from sink and copies it; ok is false at
// end of stream.
func pullFrame(sink gstapp.AppSink) (data []byte, keyframe, ok bool, err error) {
	var f C.pulled_frame
	switch C.pull_frame((*C.GstAppSink)(gstapp.UnsafeAppSinkToGlibNone(sink)), &f) {
	case 0:
		return nil, false, false, nil
	case -1:
		return nil, false, false, errPull
	}
	data = C.GoBytes(unsafe.Pointer(f._map.data), C.int(f._map.size))
	keyframe = f.keyframe != 0
	C.pull_release(&f)
	return data, keyframe, true, nil
}
