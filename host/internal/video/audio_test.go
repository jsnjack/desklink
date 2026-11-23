package video

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAudioPipelineDescription(t *testing.T) {
	desc := AudioPipelineDescription("desklink.monitor")
	for _, want := range []string{
		"pulsesrc device=desklink.monitor", "rate=48000,channels=2",
		"opusenc frame-size=10 bitrate=128000 audio-type=restricted-lowdelay", "drop=false",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("pipeline %q lacks %q", desc, want)
		}
	}
}

func TestAudioEncoderProducesSmallOpusFrames(t *testing.T) {
	Init()
	var frames, tooBig atomic.Int64
	a, err := startAudio("audiotestsrc is-live=true wave=pink-noise ! "+audioEncodeDescription(), func(f Frame) {
		frames.Add(1)
		if len(f.Data) > 1000 {
			tooBig.Add(1)
		}
	})
	if err != nil {
		t.Skipf("audio pipeline unavailable: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	a.Stop()
	// 10 ms frames: about 50 in half a second.
	if n := frames.Load(); n < 30 {
		t.Errorf("%d Opus frames in 500 ms, want about 50", n)
	}
	if tooBig.Load() > 0 {
		t.Errorf("%d frames do not fit one datagram", tooBig.Load())
	}
}
