package viewer

/*
#cgo pkg-config: gobject-2.0 gstreamer-1.0 gstreamer-app-1.0
#include <stdlib.h>
#include <glib-object.h>
#include <gst/gst.h>
#include <gst/app/gstappsrc.h>

// desklink_push_frame copies one encoded frame into a buffer stamped with
// the pipeline's running time and pushes it; *pts is that stamp, or
// GST_CLOCK_TIME_NONE before the pipeline has a clock.
static GstFlowReturn desklink_push_frame(GstAppSrc *src, const void *data, gsize size, GstClockTime *pts) {
	GstElement *el = GST_ELEMENT(src);
	GstBuffer *b = gst_buffer_new_memdup(data, size);
	GstClock *clock = gst_element_get_clock(el);
	*pts = GST_CLOCK_TIME_NONE;
	if (clock) {
		GstClockTime now = gst_clock_get_time(clock), base = gst_element_get_base_time(el);
		if (now >= base)
			*pts = now - base;
		gst_object_unref(clock);
	}
	GST_BUFFER_PTS(b) = *pts;
	return gst_app_src_push_buffer(src, b);
}

// desklink_probe_pts is the timestamp of the buffer a pad probe sees; it
// takes no reference.
static GstClockTime desklink_probe_pts(GstPadProbeInfo *info) {
	GstBuffer *b = gst_pad_probe_info_get_buffer(info);
	return b ? GST_BUFFER_PTS(b) : GST_CLOCK_TIME_NONE;
}

// desklink_object_property reads an object-valued property; the caller owns
// the returned reference.
static gpointer desklink_object_property(gpointer obj, const char *name) {
	gpointer value = NULL;
	g_object_get(obj, name, &value, NULL);
	return value;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"

	"desklink/internal/client"
)

const (
	busPoll = 100 * time.Millisecond
	// maxBacklog is how many frames may wait for a slow decoder before the
	// client skips to the next keyframe instead of falling further behind.
	maxBacklog = 8
	// maxPending bounds the frames awaiting display that the decoder tracks
	// for the delay measurement.
	maxPending = 32
)

// pendingFrame is a frame pushed into the decoder: its timestamp in the
// pipeline and when the host captured it.
type pendingFrame struct {
	pts           C.GstClockTime
	captureMicros uint64
}

// decoderChain picks the hardware VA-API decoder when the GPU has one, or a
// low-delay software decoder, plus the parser and caps for the codec. The
// stream is 10-bit, which the GTK sink may not take directly, so a converter
// follows: vapostproc on the GPU for VA-API, videoconvert otherwise. Each
// passes frames through untouched when the sink accepts them.
func decoderChain(codec string) (caps, parser, decoder, convert string) {
	if codec == "hevc" {
		caps, parser = "video/x-h265,stream-format=byte-stream,alignment=au", "h265parse"
		if gst.ElementFactoryFind("vah265dec") != nil {
			return caps, parser, "vah265dec", "vapostproc"
		}
		return caps, parser, "avdec_h265 max-threads=1", "videoconvert"
	}
	caps, parser = "video/x-av1,stream-format=obu-stream,alignment=tu", "av1parse"
	if gst.ElementFactoryFind("vaav1dec") != nil {
		return caps, parser, "vaav1dec", "vapostproc"
	}
	return caps, parser, "dav1ddec max-frame-delay=1", "videoconvert"
}

// Decoder turns encoded frames into a GdkPaintable through GStreamer:
// appsrc, parser, decoder and gtk4paintablesink, which shows each frame as
// soon as it is decoded. NewDecoder must run on the GTK main thread.
type Decoder struct {
	pipeline  gst.Pipeline
	src       gstapp.AppSrc
	paintable *gdk.Paintable
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	onBehind  func()
	skipping  atomic.Bool
	// onShown runs on a GStreamer thread with the host's capture time of
	// every frame that reaches the sink.
	onShown   func(captureMicros uint64)
	pendingMu sync.Mutex
	pending   []pendingFrame
}

// NewDecoder builds and starts the pipeline. onBehind runs on the caller of
// Push when the decoder falls behind and needs a keyframe to catch up;
// onError runs on a GStreamer thread when decoding fails; onShown runs on a
// GStreamer thread with the host's capture time of each decoded frame as it
// reaches the sink.
func NewDecoder(codec string, onBehind func(), onError func(error), onShown func(captureMicros uint64)) (*Decoder, error) {
	caps, parser, dec, convert := decoderChain(codec)
	// Push stamps each frame itself, so the stamp finds the frame again at
	// the sink: decoders keep their input's timestamps.
	desc := fmt.Sprintf(`appsrc name=src is-live=true format=time caps="%s" ! %s ! %s ! %s ! gtk4paintablesink name=sink sync=false`,
		caps, parser, dec, convert)
	slog.Debug("starting decoder", "pipeline", desc)
	el, err := gst.ParseLaunch(desc)
	if err != nil {
		return nil, fmt.Errorf("parse decoder pipeline (is gstreamer1-plugin-gtk4 installed?): %w", err)
	}
	pipeline, ok := el.(gst.Pipeline)
	if !ok {
		return nil, errors.New("decoder pipeline is not a GstPipeline")
	}
	src, ok := pipeline.GetByName("src").(gstapp.AppSrc)
	sink := pipeline.GetByName("sink")
	if !ok || sink == nil {
		return nil, errors.New("decoder pipeline lacks its appsrc or sink")
	}
	cname := C.CString("paintable")
	defer C.free(unsafe.Pointer(cname))
	ptr := C.desklink_object_property(C.gpointer(gst.UnsafeElementToGlibNone(sink)), cname)
	if ptr == nil {
		return nil, errors.New("gtk4paintablesink returned no paintable")
	}
	d := &Decoder{
		pipeline:  pipeline,
		src:       src,
		paintable: &gdk.Paintable{Object: coreglib.AssumeOwnership(unsafe.Pointer(ptr))},
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		onBehind:  onBehind,
		onShown:   onShown,
	}
	sink.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		pts := C.desklink_probe_pts((*C.GstPadProbeInfo)(gst.UnsafePadProbeInfoToGlibNone(info)))
		if capture, ok := d.shown(pts); ok && d.onShown != nil {
			d.onShown(capture)
		}
		return gst.PadProbeOK
	})
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		pipeline.SetState(gst.StateNull)
		return nil, fmt.Errorf("decoder pipeline refused to start (%s)", dec)
	}
	go d.watchBus(onError)
	return d, nil
}

func (d *Decoder) watchBus(onError func(error)) {
	defer close(d.done)
	bus := d.pipeline.GetBus()
	for {
		select {
		case <-d.stop:
			return
		default:
		}
		msg := bus.TimedPop(gst.ClockTime(busPoll))
		if msg == nil || msg.Type() != gst.MessageError {
			continue
		}
		debug, err := msg.ParseError()
		onError(fmt.Errorf("decoder: %w (%s)", err, debug))
		return
	}
}

// Paintable is what a GtkPicture shows.
func (d *Decoder) Paintable() *gdk.Paintable { return d.paintable }

// Push feeds one encoded frame from the receiving goroutine. When frames
// pile up in front of a slow decoder it drops them until the next keyframe,
// which it asks for, so the picture stays current instead of lagging more.
func (d *Decoder) Push(f client.EncodedFrame) {
	if !f.Keyframe && (d.skipping.Load() || d.src.GetCurrentLevelBuffers() > maxBacklog) {
		if !d.skipping.Swap(true) {
			slog.Debug("decoder is behind; skipping to the next keyframe")
			d.onBehind()
		}
		return
	}
	d.skipping.Store(false)
	if len(f.Data) == 0 {
		return
	}
	var pts C.GstClockTime
	ret := C.desklink_push_frame((*C.GstAppSrc)(gstapp.UnsafeAppSrcToGlibNone(d.src)),
		unsafe.Pointer(&f.Data[0]), C.gsize(len(f.Data)), &pts)
	if ret != C.GST_FLOW_OK {
		slog.Debug("decoder refused a frame", "flow", int(ret))
		return
	}
	if pts != C.GST_CLOCK_TIME_NONE && f.CaptureMicros != 0 {
		d.pendingMu.Lock()
		if len(d.pending) >= maxPending {
			d.pending = d.pending[1:]
		}
		d.pending = append(d.pending, pendingFrame{pts: pts, captureMicros: f.CaptureMicros})
		d.pendingMu.Unlock()
	}
}

// shown finds the frame stamped pts that reached the sink and forgets it
// and any older one the decoder dropped.
func (d *Decoder) shown(pts C.GstClockTime) (uint64, bool) {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	for i, p := range d.pending {
		if p.pts == pts {
			d.pending = d.pending[i+1:]
			return p.captureMicros, true
		}
	}
	return 0, false
}

// Close stops the pipeline.
func (d *Decoder) Close() {
	d.stopOnce.Do(func() {
		close(d.stop)
		<-d.done
		d.pipeline.SetState(gst.StateNull)
	})
}
