package viewer

/*
#cgo pkg-config: gobject-2.0
#include <stdlib.h>
#include <glib-object.h>

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
	"github.com/go-gst/go-glib/pkg/glib/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"

	"desklink/internal/client"
)

const (
	busPoll = 100 * time.Millisecond
	// maxBacklog is how many frames may wait for a slow decoder before the
	// client skips to the next keyframe instead of falling further behind.
	maxBacklog = 8
)

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
}

// NewDecoder builds and starts the pipeline. onBehind runs on the caller of
// Push when the decoder falls behind and needs a keyframe to catch up;
// onError runs on a GStreamer thread when decoding fails.
func NewDecoder(codec string, onBehind func(), onError func(error)) (*Decoder, error) {
	caps, parser, dec, convert := decoderChain(codec)
	desc := fmt.Sprintf(`appsrc name=src is-live=true format=time do-timestamp=true caps="%s" ! %s ! %s ! %s ! gtk4paintablesink name=sink sync=false`,
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
	}
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
	if ret := d.src.PushBuffer(gst.NewBufferWrappedBytes(glib.NewBytes(f.Data))); ret != gst.FlowOK {
		slog.Debug("decoder refused a frame", "flow", ret)
	}
}

// Close stops the pipeline.
func (d *Decoder) Close() {
	d.stopOnce.Do(func() {
		close(d.stop)
		<-d.done
		d.pipeline.SetState(gst.StateNull)
	})
}
