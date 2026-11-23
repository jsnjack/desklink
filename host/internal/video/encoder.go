// Package video runs the zero-copy capture and hardware encode pipeline:
// PipeWire DMA-BUF frames from Mutter's screen cast, VA-API colour
// conversion to 10-bit P010 and VA-API AV1 or HEVC encoding in CQP mode.
package video

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
	"github.com/go-gst/go-gst/pkg/gstvideo"
)

// Codec is a stream codec name used on the wire.
type Codec string

// Supported codecs.
const (
	CodecAV1  Codec = "av1"
	CodecHEVC Codec = "hevc"
)

const (
	encoderName = "enc"
	sinkName    = "sink"
	postName    = "post"
	defaultFPS  = 60
	// keyframeInterval is the GOP length; losses are repaired by forced
	// keyframes, so the periodic one is only a backstop.
	keyframeInterval = 120
	// keepaliveMs makes pipewiresrc resend the last frame on a static
	// screen, so a forced keyframe is produced within this time.
	keepaliveMs = 100
	busPoll     = 100 * time.Millisecond
	// fullRangeBT709 is the GstVideoColorimetry string range:matrix:transfer:primaries
	// for full-range BT.709, which keeps text edges at full contrast.
	fullRangeBT709 = "1:3:5:1"
)

var initOnce sync.Once

// Init initialises GStreamer once per process.
func Init() { initOnce.Do(gst.Init) }

func encoderElement(c Codec) string {
	if c == CodecHEVC {
		return "vah265enc"
	}
	return "vaav1enc"
}

// Available reports whether the VA-API encoder for the codec is installed
// and usable on this GPU.
func Available(c Codec) bool {
	return gst.ElementFactoryFind(encoderElement(c)) != nil
}

// AV1QIndex maps an HEVC-scale quantizer (0..51) to the AV1 base_q_idx
// scale (0..255) that vaav1enc's qp property uses.
func AV1QIndex(qp int) int {
	return max(0, min(qp*255/51, 255))
}

// Settings configure one pipeline run.
type Settings struct {
	Codec  Codec
	Width  int
	Height int
	QP     int
	FPS    int
	// NodeID is the screen cast's PipeWire node, on the user's PipeWire
	// daemon.
	NodeID uint32
}

// PipelineDescription returns the gst-launch description for the settings.
// vapostproc scales to the client size and adds borders when the desktop
// could not be switched to that size. The appsink keeps every frame: dropping
// an encoded frame would corrupt every frame that references it.
func PipelineDescription(s Settings) string {
	return fmt.Sprintf("pipewiresrc path=%d do-timestamp=true keepalive-time=%d ! queue max-size-buffers=2 leaky=downstream ! ",
		s.NodeID, keepaliveMs) + encodeDescription(s)
}

// encodeDescription is the pipeline after the source: conversion, encoder,
// parser and appsink. The frame-rate cap is a pad probe on vapostproc's sink
// (frameLimiter), because Mutter's screen cast has a variable frame rate
// (framerate=0/1), which videorate passes through unchanged.
func encodeDescription(s Settings) string {
	convert := fmt.Sprintf("vapostproc name=%s add-borders=true ! video/x-raw(memory:VAMemory),format=P010_10LE,width=%d,height=%d,colorimetry=(string)%s ! ",
		postName, s.Width, s.Height, fullRangeBT709)
	sink := fmt.Sprintf(" ! appsink name=%s sync=false max-buffers=8 drop=false", sinkName)
	if s.Codec == CodecHEVC {
		return convert + fmt.Sprintf("vah265enc name=%s rate-control=cqp qpi=%d qpp=%d b-frames=0 key-int-max=%d ref-frames=1 ! "+
			"h265parse config-interval=-1 ! video/x-h265,stream-format=byte-stream,alignment=au",
			encoderName, s.QP, s.QP, keyframeInterval) + sink
	}
	return convert + fmt.Sprintf("vaav1enc name=%s rate-control=cqp qp=%d key-int-max=%d ref-frames=1 palette-mode=true ! "+
		"av1parse ! video/x-av1,stream-format=obu-stream,alignment=tu",
		encoderName, AV1QIndex(s.QP), keyframeInterval) + sink
}

// Frame is one encoded access unit (HEVC) or temporal unit (AV1).
type Frame struct {
	Data     []byte
	Keyframe bool
	Captured time.Time
}

// Encoder is a running pipeline.
type Encoder struct {
	codec    Codec
	pipeline gst.Pipeline
	enc      gst.Element
	limiter  *frameLimiter
	sink     gstapp.AppSink
	errs     chan error
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	frames   atomic.Uint64
}

// Start builds the pipeline and sets it playing. onFrame runs on the
// streaming thread for every encoded frame and must not block for long.
func Start(s Settings, onFrame func(Frame)) (*Encoder, error) {
	return start(PipelineDescription(s), s.Codec, s.FPS, onFrame)
}

func start(desc string, codec Codec, fps int, onFrame func(Frame)) (*Encoder, error) {
	if fps <= 0 {
		fps = defaultFPS
	}
	slog.Debug("starting pipeline", "pipeline", desc)
	el, err := gst.ParseLaunch(desc)
	if err != nil {
		return nil, fmt.Errorf("parse pipeline: %w", err)
	}
	pipeline, ok := el.(gst.Pipeline)
	if !ok {
		return nil, errors.New("parsed pipeline is not a GstPipeline")
	}
	e := &Encoder{
		codec:    codec,
		pipeline: pipeline,
		enc:      pipeline.GetByName(encoderName),
		limiter:  newFrameLimiter(fps),
		errs:     make(chan error, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	post := pipeline.GetByName(postName)
	if e.sink, ok = pipeline.GetByName(sinkName).(gstapp.AppSink); !ok || e.enc == nil || post == nil {
		return nil, errors.New("pipeline is missing the converter, the encoder or the appsink")
	}
	post.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if pts, ok := probePTS(info); !ok || e.limiter.allow(pts) {
			return gst.PadProbeOK
		}
		return gst.PadProbeDrop
	})
	e.sink.SetCallbacks(&gstapp.AppSinkCallbacks{NewSample: func(sink gstapp.AppSink) gst.FlowReturn {
		data, keyframe, ok, err := pullFrame(sink)
		if err != nil {
			return gst.FlowError
		}
		if !ok {
			return gst.FlowEOS
		}
		e.frames.Add(1)
		onFrame(Frame{Data: data, Keyframe: keyframe, Captured: time.Now()})
		return gst.FlowOK
	}})
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		pipeline.SetState(gst.StateNull)
		return nil, errors.New("pipeline refused to start: check that the VA-API driver and pipewire-gstreamer are installed")
	}
	go e.watchBus()
	return e, nil
}

func (e *Encoder) watchBus() {
	defer close(e.done)
	bus := e.pipeline.GetBus()
	for {
		select {
		case <-e.stop:
			return
		default:
		}
		msg := bus.TimedPop(gst.ClockTime(busPoll))
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageError:
			debug, err := msg.ParseError()
			e.report(fmt.Errorf("pipeline error: %w (%s)", err, debug))
			return
		case gst.MessageEOS:
			e.report(errors.New("pipeline reached end of stream: the screen cast ended"))
			return
		}
	}
}

func (e *Encoder) report(err error) {
	select {
	case e.errs <- err:
	default:
	}
}

// Errors delivers the first pipeline error.
func (e *Encoder) Errors() <-chan error { return e.errs }

// Done is closed when the pipeline stops, after any error was delivered.
func (e *Encoder) Done() <-chan struct{} { return e.done }

// Codec is the codec this pipeline encodes.
func (e *Encoder) Codec() Codec { return e.codec }

// Frames is the number of frames encoded so far.
func (e *Encoder) Frames() uint64 { return e.frames.Load() }

// SetQP changes the quantizer of the running encoder.
func (e *Encoder) SetQP(qp int) {
	if e.codec == CodecHEVC {
		e.enc.SetObjectProperty("qpi", uint32(qp))
		e.enc.SetObjectProperty("qpp", uint32(qp))
		return
	}
	e.enc.SetObjectProperty("qp", uint32(AV1QIndex(qp)))
}

// SetMaxFPS changes the frame-rate cap of the running pipeline.
func (e *Encoder) SetMaxFPS(fps int) { e.limiter.setFPS(fps) }

// ForceKeyframe asks the encoder for a keyframe with headers, sent upstream
// from the appsink so it passes the parser.
func (e *Encoder) ForceKeyframe() bool {
	return e.sink.SendEvent(gstvideo.VideoEventNewUpstreamForceKeyUnit(gst.ClockTimeNone, true, 0))
}

// Stop tears the pipeline down.
func (e *Encoder) Stop() {
	e.stopOnce.Do(func() {
		close(e.stop)
		<-e.done
		e.pipeline.SetState(gst.StateNull)
	})
}
