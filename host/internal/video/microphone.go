package video

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0
#include <gst/gst.h>
#include <gst/app/gstappsrc.h>

// push_opus copies one Opus packet into a new buffer and hands it to the
// appsrc, which takes it over.
static GstFlowReturn push_opus(GstElement *src, const void *data, gsize size, GstClockTime pts, GstClockTime dur, gboolean discont) {
	GstBuffer *b = gst_buffer_new_memdup(data, size);
	GST_BUFFER_PTS(b) = pts;
	GST_BUFFER_DURATION(b) = dur;
	if (discont)
		GST_BUFFER_FLAG_SET(b, GST_BUFFER_FLAG_DISCONT);
	return gst_app_src_push_buffer(GST_APP_SRC(src), b);
}

// push_gap sends a gap event through the appsrc, which queues it in order
// with the buffers; opusdec with plc=true conceals the missing packet.
static gboolean push_gap(GstElement *src, GstClockTime pts, GstClockTime dur) {
	return gst_element_send_event(src, gst_event_new_gap(pts, dur));
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unsafe"

	"github.com/go-gst/go-gst/pkg/gst"

	"desklink/internal/logging"
)

// The client's microphone: 48 kHz mono Opus, one frame per packet.
const (
	micSrcName = "micsrc"
	// micQueue bounds the packets waiting in the appsrc while the output
	// blocks; older ones are dropped, so the delay cannot grow there either.
	micQueue = 3
	// maxPlayoutLag is how far playout may fall behind its schedule (the
	// process was not scheduled) before it starts the schedule again
	// instead of hurrying to catch up.
	maxPlayoutLag = 100 * time.Millisecond
)

// MicrophonePipelineDescription decodes the client's Opus frames and plays
// them on the PulseAudio sink device (served by PipeWire) without clock
// sync, so each frame plays as the jitter buffer releases it. The output
// keeps at most 40 ms.
func MicrophonePipelineDescription(device string) string {
	return micDecodeDescription() + fmt.Sprintf(" ! pulsesink device=%s sync=false buffer-time=40000 latency-time=10000", device)
}

func micDecodeDescription() string {
	return fmt.Sprintf("appsrc name=%s is-live=true format=time max-buffers=%d leaky-type=downstream "+
		"caps=audio/x-opus,rate=%d,channels=1,channel-mapping-family=0 ! opusdec plc=true ! "+
		"audioconvert ! audioresample ! audio/x-raw,rate=%d,channels=1",
		micSrcName, micQueue, AudioRate, AudioRate)
}

// Microphone plays the client's microphone into a PulseAudio sink: a jitter
// buffer orders the frames, and a playout goroutine hands one to the decoder
// per frame slot, or has it conceal a missing one.
type Microphone struct {
	runner
	src gst.Element

	mu     sync.Mutex
	jitter *jitterBuffer
	closed bool

	// pts and discont belong to the playout goroutine.
	pts     time.Duration
	discont bool

	stopPlay chan struct{}
	played   chan struct{}
	playOnce sync.Once
}

// StartMicrophone starts playing the client's microphone on the sink device.
func StartMicrophone(device string) (*Microphone, error) {
	return startMicrophone(MicrophonePipelineDescription(device))
}

func startMicrophone(desc string) (*Microphone, error) {
	slog.Debug("starting microphone pipeline", "pipeline", desc)
	el, err := gst.ParseLaunch(desc)
	if err != nil {
		return nil, fmt.Errorf("parse microphone pipeline: %w", err)
	}
	pipeline, ok := el.(gst.Pipeline)
	if !ok {
		return nil, errors.New("parsed microphone pipeline is not a GstPipeline")
	}
	m := &Microphone{
		runner: newRunner(pipeline, "microphone playback ended"),
		jitter: newJitterBuffer(), stopPlay: make(chan struct{}), played: make(chan struct{}),
	}
	if m.src = pipeline.GetByName(micSrcName); m.src == nil {
		return nil, errors.New("microphone pipeline has no appsrc")
	}
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		pipeline.SetState(gst.StateNull)
		return nil, errors.New("microphone pipeline refused to start: check that gstreamer1-plugins-base and -good are installed")
	}
	go m.watchBus()
	go m.play()
	return m, nil
}

// Push queues one of the client's frames. It runs on the UDP goroutine and
// never blocks for long.
func (m *Microphone) Push(counter uint16, frame []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.jitter.put(counter, append([]byte(nil), frame...))
	}
}

// Stats returns what the jitter buffer did so far.
func (m *Microphone) Stats() MicStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jitter.stats
}

// play hands the decoder one frame slot at a time, on a schedule kept from
// the frames' durations, until Stop.
func (m *Microphone) play() {
	defer close(m.played)
	timer := time.NewTimer(0)
	defer timer.Stop()
	deadline := time.Now()
	for {
		select {
		case <-m.stopPlay:
			return
		case <-timer.C:
		}
		m.mu.Lock()
		frame, dur, kind := m.jitter.pop()
		m.mu.Unlock()
		switch kind {
		case playFrame:
			m.push(frame, dur)
		case playConceal:
			m.conceal(dur)
		case playIdle:
			// The decoder starts afresh with the next frame.
			m.discont = true
		}
		deadline = deadline.Add(dur)
		if now := time.Now(); now.Sub(deadline) > maxPlayoutLag {
			deadline = now
		}
		timer.Reset(time.Until(deadline))
	}
}

func (m *Microphone) element() *C.GstElement {
	return (*C.GstElement)(gst.UnsafeElementToGlibNone(m.src))
}

func (m *Microphone) push(frame []byte, dur time.Duration) {
	discont := C.gboolean(0)
	if m.discont {
		discont = 1
	}
	ret := C.push_opus(m.element(), unsafe.Pointer(&frame[0]), C.gsize(len(frame)), C.GstClockTime(m.pts), C.GstClockTime(dur), discont)
	if ret != C.GST_FLOW_OK {
		slog.Log(context.Background(), logging.LevelTrace, "microphone pipeline refused a frame", "flow", int(ret))
	}
	m.pts += dur
	m.discont = false
}

func (m *Microphone) conceal(dur time.Duration) {
	if C.push_gap(m.element(), C.GstClockTime(m.pts), C.GstClockTime(dur)) == 0 {
		slog.Log(context.Background(), logging.LevelTrace, "microphone pipeline refused a gap")
	}
	m.pts += dur
}

// Stop ends playout and tears the pipeline down; later frames are dropped.
func (m *Microphone) Stop() {
	m.playOnce.Do(func() {
		close(m.stopPlay)
		<-m.played
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
	})
	m.runner.Stop()
}
