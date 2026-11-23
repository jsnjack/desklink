package video

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
)

// The audio stream: 48 kHz stereo Opus in 10 ms frames. At 128 kbit/s a
// frame is about 160 bytes, so each one fits a single datagram.
const (
	AudioRate     = 48000
	AudioChannels = 2
	audioFrameMs  = 10
	audioBitrate  = 128000
	audioSinkName = "audiosink"
)

// AudioPipelineDescription captures the PulseAudio source device (served by
// PipeWire; a sink's monitor captures what plays on that sink) with a 10 ms
// capture buffer and encodes it as Opus. restricted-lowdelay drops Opus's
// speech modes to cut its algorithmic delay from 26.5 ms to 5 ms.
func AudioPipelineDescription(device string) string {
	return fmt.Sprintf("pulsesrc device=%s buffer-time=20000 latency-time=10000 ! ", device) + audioEncodeDescription()
}

func audioEncodeDescription() string {
	return fmt.Sprintf("audioconvert ! audioresample ! audio/x-raw,format=S16LE,rate=%d,channels=%d ! "+
		"opusenc frame-size=%d bitrate=%d audio-type=restricted-lowdelay ! "+
		"appsink name=%s sync=false max-buffers=50 drop=false",
		AudioRate, AudioChannels, audioFrameMs, audioBitrate, audioSinkName)
}

// AudioEncoder is a running audio pipeline.
type AudioEncoder struct {
	runner
	sink gstapp.AppSink
}

// StartAudio captures device and calls onFrame with every Opus frame, on the
// streaming thread.
func StartAudio(device string, onFrame func(Frame)) (*AudioEncoder, error) {
	return startAudio(AudioPipelineDescription(device), onFrame)
}

func startAudio(desc string, onFrame func(Frame)) (*AudioEncoder, error) {
	slog.Debug("starting audio pipeline", "pipeline", desc)
	el, err := gst.ParseLaunch(desc)
	if err != nil {
		return nil, fmt.Errorf("parse audio pipeline: %w", err)
	}
	pipeline, ok := el.(gst.Pipeline)
	if !ok {
		return nil, errors.New("parsed audio pipeline is not a GstPipeline")
	}
	a := &AudioEncoder{runner: newRunner(pipeline, "audio capture ended")}
	if a.sink, ok = pipeline.GetByName(audioSinkName).(gstapp.AppSink); !ok {
		return nil, errors.New("audio pipeline has no appsink")
	}
	a.sink.SetCallbacks(&gstapp.AppSinkCallbacks{NewSample: func(sink gstapp.AppSink) gst.FlowReturn {
		data, _, ok, err := pullFrame(sink)
		if err != nil {
			return gst.FlowError
		}
		if !ok {
			return gst.FlowEOS
		}
		onFrame(Frame{Data: data, Keyframe: true, Captured: time.Now()})
		return gst.FlowOK
	}})
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		pipeline.SetState(gst.StateNull)
		return nil, errors.New("audio pipeline refused to start: check that gstreamer1-plugins-good is installed")
	}
	go a.watchBus()
	return a, nil
}
