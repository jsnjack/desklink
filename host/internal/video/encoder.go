// Package video runs the zero-copy capture and hardware encode pipeline:
// PipeWire DMA-BUF frames from Mutter's screen cast, VA-API colour
// conversion to 10-bit P010 and VA-API AV1 or HEVC encoding in CQP mode.
package video

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	// keyframeInterval is the GOP length, the longest the VA-API encoders
	// take. Losses are repaired by forced keyframes, which clients ask for in
	// every telemetry report until one arrives, so the periodic keyframe is
	// only a backstop. At 120 frames it was the stream's heaviest load: a
	// 2560x1600 keyframe of 270-340 KB every second at 120 fps, every 12 s
	// on a static screen (keepaliveMs), each burst long enough to look like
	// congestion.
	keyframeInterval = 1024
	// av1TargetUsage is vaav1enc's speed preset. With Mesa's radeonsi
	// driver (measured on an RX 7800 XT), 2 encodes a 2560x1600 frame in
	// about half the time of the default 4: scrolling took 6 ms a frame
	// instead of 13.5 (95th percentile 10 ms instead of 34), so 120 fps
	// fits, for 10-25 % more bytes while scrolling and the same text quality.
	// 1 sent ten times the bytes while scrolling, and 6 and 7 take twice as
	// long as 4.
	av1TargetUsage = 2
	// captureName is the pipeline's source; the frame limiter sits on its
	// output, so the encoder and the scan branch get the same frames.
	captureName = "capture"
	busPoll     = 100 * time.Millisecond
	// streamColorimetry is the GstVideoColorimetry string
	// range:matrix:transfer:primaries of the stream: limited range, the BT.709
	// matrix and primaries, and the sRGB transfer the captured desktop has.
	// Limited range is what every decoder assumes. Full range looked
	// stretched on the Tab S9: its AV1 decoder ignores the range in the
	// sequence header and treats the video as limited, which deepens blacks
	// and saturates colours. At 10 bits limited range still has 877 levels.
	// The transfer must match the capture's: with the BT.709 transfer (5),
	// vapostproc re-encoded the sRGB values through the BT.709 curve, which
	// lifted every mid-tone (grey 128 became 10-bit luma 531 instead of 504)
	// and made the stream look washed out. Matching transfers leave the
	// desktop's values as they are; only the matrix and range change.
	streamColorimetry = "2:3:7:1"
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

// encoderInput is what the pipeline gives the encoder: vapostproc's 10-bit
// frames.
const encoderInput = "video/x-raw(memory:VAMemory),format=P010_10LE"

// EncoderInfo describes the VA-API encoder for a codec as GStreamer's
// registry lists it.
type EncoderInfo struct {
	// Available is set when the encoder is installed and usable on this GPU.
	Available bool
	// TenBit is set when it takes the 10-bit frames the pipeline sends.
	TenBit bool
	// Device is the GPU it encodes on, as the driver names it; "" when the
	// registry does not say.
	Device string
}

// Usable reports whether the pipeline can encode with it.
func (e EncoderInfo) Usable() bool { return e.Available && e.TenBit }

// ProbeEncoder describes the VA-API encoder for c without opening the GPU.
// Init must have run.
func ProbeEncoder(c Codec) EncoderInfo {
	f := gst.ElementFactoryFind(encoderElement(c))
	if f == nil {
		return EncoderInfo{}
	}
	return EncoderInfo{
		Available: true,
		TenBit:    f.CanSinkAnyCaps(gst.CapsFromString(encoderInput)),
		Device:    encoderDevice(f.GetMetadata("long-name")),
	}
}

// encoderDevice is the GPU in a VA element's long name, such as "VA-API AV1
// Encoder in AMD Radeon 890M Graphics".
func encoderDevice(longName string) string {
	if _, device, ok := strings.Cut(longName, " in "); ok {
		return device
	}
	return ""
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
	// CaptureWidth, CaptureHeight and CaptureFPS, when set, fix the size
	// and maximum rate the stream negotiates; for a virtual monitor they are
	// the monitor's mode. Width and Height may be smaller (a lower quality
	// level), and vapostproc scales.
	CaptureWidth, CaptureHeight, CaptureFPS int
	// Scan, when set, gets every frame the encoder may encode as exact
	// pixels at the stream's size, from a second branch (scan.go).
	Scan func(ScanFrame)
}

// PipelineDescription returns the gst-launch description for the settings.
// vapostproc scales to the client size and adds borders when the desktop
// could not be switched to that size. The appsink keeps every frame: dropping
// an encoded frame would corrupt every frame that references it.
func PipelineDescription(s Settings) string {
	capture := ""
	if s.CaptureWidth > 0 && s.CaptureHeight > 0 {
		capture = fmt.Sprintf("video/x-raw(memory:DMABuf),width=%d,height=%d,max-framerate=%d/1 ! ",
			s.CaptureWidth, s.CaptureHeight, max(s.CaptureFPS, 1))
	}
	src := fmt.Sprintf("pipewiresrc name=%s path=%d do-timestamp=true keepalive-time=%d ! %squeue max-size-buffers=2 leaky=downstream ! ",
		captureName, s.NodeID, resendAfterMs(s.FPS), capture)
	if s.Scan != nil {
		return src + fmt.Sprintf("tee name=%s ! ", splitName) + encodeDescription(s) + scanDescription(s)
	}
	return src + encodeDescription(s)
}

// encodeDescription is the pipeline after the source: conversion, encoder,
// parser and appsink. The frame-rate cap is a pad probe on the source's
// output (frameLimiter, attachLimiter), because Mutter's screen cast has a
// variable frame rate (framerate=0/1), which videorate passes through
// unchanged.
func encodeDescription(s Settings) string {
	convert := convertDescription(s)
	sink := fmt.Sprintf(" ! appsink name=%s sync=false max-buffers=8 drop=false", sinkName)
	if s.Scan != nil {
		// Beside the scan branch, which shares its thread (scan.go).
		sink += " async=false"
	}
	if s.Codec == CodecHEVC {
		return convert + fmt.Sprintf("vah265enc name=%s rate-control=cqp qpi=%d qpp=%d b-frames=0 key-int-max=%d ref-frames=1 ! "+
			"h265parse config-interval=-1 ! video/x-h265,stream-format=byte-stream,alignment=au",
			encoderName, s.QP, s.QP, keyframeInterval) + sink
	}
	return convert + fmt.Sprintf("vaav1enc name=%s rate-control=cqp qp=%d key-int-max=%d ref-frames=1 palette-mode=true target-usage=%d ! "+
		"av1parse ! video/x-av1,stream-format=obu-stream,alignment=tu",
		encoderName, AV1QIndex(s.QP), keyframeInterval, av1TargetUsage) + sink
}

// convertDescription scales and converts the captured desktop to the
// encoder's 10-bit format and colours. Square pixels make add-borders
// letterbox a desktop of another shape; left open, caps negotiation keeps the
// shape with non-square pixels, which the encoder ignores, and the client
// shows it stretched.
func convertDescription(s Settings) string {
	return fmt.Sprintf("vapostproc name=%s add-borders=true ! %s,width=%d,height=%d,pixel-aspect-ratio=1/1,colorimetry=(string)%s ! ",
		postName, encoderInput, s.Width, s.Height, streamColorimetry)
}

// Frame is one encoded access unit (HEVC) or temporal unit (AV1).
type Frame struct {
	Data     []byte
	Keyframe bool
	// Captured is when the screen was captured: the source's timestamp,
	// so clients can measure the whole delay, encoding included.
	Captured time.Time
	// PTS is the source's timestamp of the captured buffer, which the scan
	// branch's frames carry too; -1 when unknown.
	PTS time.Duration
}

// runner is a playing pipeline whose first bus error is reported once; the
// video and audio encoders embed it.
type runner struct {
	pipeline gst.Pipeline
	// eos explains an end of stream, which a live capture never reaches.
	eos      string
	errs     chan error
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newRunner(pipeline gst.Pipeline, eos string) runner {
	return runner{pipeline: pipeline, eos: eos, errs: make(chan error, 1), stop: make(chan struct{}), done: make(chan struct{})}
}

// Encoder is a running video pipeline.
type Encoder struct {
	runner
	codec   Codec
	enc     gst.Element
	limiter *frameLimiter
	// capture is the source when it is a pipewiresrc, whose resend delay
	// follows the frame rate; nil otherwise.
	capture gst.Element
	sink    gstapp.AppSink
	frames  atomic.Uint64
	// captures holds the capture times and source timestamps of frames that
	// entered the encoder and have not come out yet. Frames come out in the
	// order they went in (no B-frames) and nothing drops them in between.
	capMu    sync.Mutex
	captures []entered
	// scan is the scan branch, nil without Settings.Scan.
	scan *scanBranch
}

// entered is a frame that entered the encoder.
type entered struct {
	captured time.Time
	pts      time.Duration
}

// maxPendingCaptures bounds captures, should the encoder ever drop a frame.
const maxPendingCaptures = 16

func (e *Encoder) frameEntered(t time.Time, pts time.Duration) {
	e.capMu.Lock()
	defer e.capMu.Unlock()
	if len(e.captures) >= maxPendingCaptures {
		e.captures = e.captures[1:]
	}
	e.captures = append(e.captures, entered{captured: t, pts: pts})
}

// frameLeft returns the capture time and source timestamp of the frame the
// encoder produced, or fallback and -1 when none is pending.
func (e *Encoder) frameLeft(fallback time.Time) (time.Time, time.Duration) {
	e.capMu.Lock()
	defer e.capMu.Unlock()
	if len(e.captures) == 0 {
		return fallback, -1
	}
	f := e.captures[0]
	e.captures = e.captures[1:]
	return f.captured, f.pts
}

// Start builds the pipeline and sets it playing. onFrame runs on the
// streaming thread for every encoded frame and must not block for long.
func Start(s Settings, onFrame func(Frame)) (*Encoder, error) {
	return startWithScan(PipelineDescription(s), s.Codec, s.FPS, onFrame, s.Scan)
}

func start(desc string, codec Codec, fps int, onFrame func(Frame)) (*Encoder, error) {
	return startWithScan(desc, codec, fps, onFrame, nil)
}

// startWithScan starts the pipeline desc; with onScan, desc must hold the
// scan branch (scanDescription).
func startWithScan(desc string, codec Codec, fps int, onFrame func(Frame), onScan func(ScanFrame)) (*Encoder, error) {
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
		runner:  newRunner(pipeline, "pipeline reached end of stream: the screen cast ended"),
		codec:   codec,
		enc:     pipeline.GetByName(encoderName),
		limiter: newFrameLimiter(fps),
	}
	post := pipeline.GetByName(postName)
	if e.sink, ok = pipeline.GetByName(sinkName).(gstapp.AppSink); !ok || e.enc == nil || post == nil {
		return nil, errors.New("pipeline is missing the converter, the encoder or the appsink")
	}
	if err := e.attachLimiter(pipeline); err != nil {
		return nil, err
	}
	post.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		pts, ok := probePTS(info)
		if !ok {
			pts = -1
		}
		e.frameEntered(probeCaptured(post, info), pts)
		return gst.PadProbeOK
	})
	e.sink.SetCallbacks(&gstapp.AppSinkCallbacks{NewSample: func(sink gstapp.AppSink) gst.FlowReturn {
		f, ok, err := pullFrame(sink)
		if err != nil {
			return gst.FlowError
		}
		if !ok {
			return gst.FlowEOS
		}
		e.frames.Add(1)
		captured, pts := e.frameLeft(f.captured)
		onFrame(Frame{Data: f.data, Keyframe: f.keyframe, Captured: captured, PTS: pts})
		return gst.FlowOK
	}})
	if onScan != nil {
		if e.scan, err = attachScan(pipeline, onScan); err != nil {
			return nil, err
		}
	}
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		pipeline.SetState(gst.StateNull)
		return nil, errors.New("pipeline refused to start: check that the VA-API driver and pipewire-gstreamer are installed")
	}
	go e.watchBus()
	return e, nil
}

// attachLimiter drops the frames above the frame rate where they leave the
// source (captureName), before the queue, conversion and encoding. A
// pipewiresrc's resends of its last frame are told from new frames there,
// where every buffer passes in the order the source made it.
func (e *Encoder) attachLimiter(pipeline gst.Pipeline) error {
	src := pipeline.GetByName(captureName)
	if src == nil {
		return errors.New("pipeline is missing the capture source")
	}
	var resends *resendDetector
	if isPipeWireSrc(src) {
		e.capture, resends = src, &resendDetector{}
	}
	src.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		resend := resends != nil && resends.resend(probeMemory(info))
		pts, ok := probePTS(info)
		if ok && !e.limiter.allow(pts, resend) {
			return gst.PadProbeDrop
		}
		return gst.PadProbeOK
	})
	return nil
}

func (e *runner) watchBus() {
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
			e.report(errors.New(e.eos))
			return
		}
	}
}

func (e *runner) report(err error) {
	select {
	case e.errs <- err:
	default:
	}
}

// Errors delivers the first pipeline error.
func (e *runner) Errors() <-chan error { return e.errs }

// Done is closed when the pipeline stops, after any error was delivered.
func (e *runner) Done() <-chan struct{} { return e.done }

// Codec is the codec this pipeline encodes.
func (e *Encoder) Codec() Codec { return e.codec }

// Frames is the number of frames encoded so far.
func (e *Encoder) Frames() uint64 { return e.frames.Load() }

// SetQP changes the quantizer of the running encoder. The VA-API encoders
// start a new sequence whenever it changes, so every change sends a keyframe.
func (e *Encoder) SetQP(qp int) {
	if e.codec == CodecHEVC {
		e.enc.SetObjectProperty("qpi", uint32(qp))
		e.enc.SetObjectProperty("qpp", uint32(qp))
		return
	}
	e.enc.SetObjectProperty("qp", uint32(AV1QIndex(qp)))
}

// SetMaxFPS changes the frame-rate cap of the running pipeline.
func (e *Encoder) SetMaxFPS(fps int) {
	e.limiter.setFPS(fps)
	if e.capture != nil {
		e.capture.SetObjectProperty("keepalive-time", int32(resendAfterMs(fps)))
	}
}

// ForceKeyframe asks the encoder for a keyframe with headers, sent upstream
// from the appsink so it passes the parser.
func (e *Encoder) ForceKeyframe() bool {
	return e.sink.SendEvent(gstvideo.VideoEventNewUpstreamForceKeyUnit(gst.ClockTimeNone, true, 0))
}

// Stop tears the pipeline down.
func (e *runner) Stop() {
	e.stopOnce.Do(func() {
		close(e.stop)
		<-e.done
		e.pipeline.SetState(gst.StateNull)
	})
}
