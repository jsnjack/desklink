package video

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0
#include <gst/gst.h>
#include <gst/app/gstappsink.h>

// buffer_age is how long ago buffer b was captured, in nanoseconds: the
// pipeline's running time now minus the running time the source stamped on
// it; -1 when either is unknown.
static gint64 buffer_age(GstElement *el, GstBuffer *b) {
	GstClockTime pts = GST_BUFFER_PTS(b);
	GstClock *clock = gst_element_get_clock(el);
	gint64 age = -1;
	if (clock && GST_CLOCK_TIME_IS_VALID(pts)) {
		GstClockTime now = gst_clock_get_time(clock);
		GstClockTime base = gst_element_get_base_time(el);
		if (now >= base && now - base >= pts)
			age = (gint64)(now - base - pts);
	}
	if (clock)
		gst_object_unref(clock);
	return age;
}

// probe_age is buffer_age for the buffer a pad probe of el sees.
static gint64 probe_age(GstElement *el, GstPadProbeInfo *info) {
	GstBuffer *b = gst_pad_probe_info_get_buffer(info);
	return b ? buffer_age(el, b) : -1;
}

// probe_pts is the timestamp of the buffer a pad probe sees. It takes no
// reference, so the buffer goes back to its pool as soon as the pipeline is
// done with it.
static GstClockTime probe_pts(GstPadProbeInfo *info) {
	GstBuffer *b = gst_pad_probe_info_get_buffer(info);
	return b ? GST_BUFFER_PTS(b) : GST_CLOCK_TIME_NONE;
}

// probe_memory is the address of the first memory of the buffer a pad probe
// sees, or 0; it takes no reference.
static guintptr probe_memory(GstPadProbeInfo *info) {
	GstBuffer *b = gst_pad_probe_info_get_buffer(info);
	return b && gst_buffer_n_memory(b) > 0 ? (guintptr)gst_buffer_peek_memory(b, 0) : 0;
}

// is_pipewiresrc reports whether el is a pipewiresrc.
static gboolean is_pipewiresrc(GstElement *el) {
	GstElementFactory *f = gst_element_get_factory(el);
	return f && g_strcmp0(gst_plugin_feature_get_name(GST_PLUGIN_FEATURE(f)), "pipewiresrc") == 0;
}

typedef struct {
	GstSample *sample;
	GstBuffer *buffer;
	GstMapInfo map;
	gboolean keyframe;
	// age is buffer_age of the pulled buffer.
	gint64 age;
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
	f->age = buffer_age(GST_ELEMENT(sink), f->buffer);
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

// probeMemory identifies the memory of the buffer a pad probe sees: its
// first block's address, or 0 without one.
func probeMemory(info *gst.PadProbeInfo) uintptr {
	return uintptr(C.probe_memory((*C.GstPadProbeInfo)(gst.UnsafePadProbeInfoToGlibNone(info))))
}

// isPipeWireSrc reports whether el is a pipewiresrc.
func isPipeWireSrc(el gst.Element) bool {
	return C.is_pipewiresrc((*C.GstElement)(gst.UnsafeElementToGlibNone(el))) != 0
}

// maxFrameAge bounds a believable capture age; anything older means the
// timestamps are not the source's capture times.
const maxFrameAge = time.Second

// capturedAt turns a buffer's age in nanoseconds into the time it was
// captured, or now when the age is unknown or not believable.
func capturedAt(now time.Time, age C.gint64) time.Time {
	if d := time.Duration(age); d >= 0 && d < maxFrameAge {
		return now.Add(-d)
	}
	return now
}

// probeCaptured returns when the buffer a pad probe of el sees was captured.
func probeCaptured(el gst.Element, info *gst.PadProbeInfo) time.Time {
	age := C.probe_age((*C.GstElement)(gst.UnsafeElementToGlibNone(el)),
		(*C.GstPadProbeInfo)(gst.UnsafePadProbeInfoToGlibNone(info)))
	return capturedAt(time.Now(), age)
}

// pulledFrame is one encoded frame copied out of the appsink.
type pulledFrame struct {
	data     []byte
	keyframe bool
	// captured is when the source captured the frame, from the buffer's
	// timestamp, or when it was pulled if the timestamp does not say. Video
	// encoders may restamp their output (vah265enc counts frames from 1000
	// hours), so the video encoder takes capture times before encoding.
	captured time.Time
}

// pullFrame pulls one encoded frame from sink and copies it; ok is false at
// end of stream.
func pullFrame(sink gstapp.AppSink) (f pulledFrame, ok bool, err error) {
	var c C.pulled_frame
	switch C.pull_frame((*C.GstAppSink)(gstapp.UnsafeAppSinkToGlibNone(sink)), &c) {
	case 0:
		return pulledFrame{}, false, nil
	case -1:
		return pulledFrame{}, false, errPull
	}
	f = pulledFrame{
		data:     C.GoBytes(unsafe.Pointer(c._map.data), C.int(c._map.size)),
		keyframe: c.keyframe != 0,
		captured: capturedAt(time.Now(), c.age),
	}
	C.pull_release(&c)
	return f, true, nil
}
