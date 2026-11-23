package viewer

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/go-gst/go-glib/pkg/glib/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"

	dlnet "desklink/internal/net"
)

// maxAudioBacklog is how many 10 ms packets may wait in the appsrc before
// new ones are dropped, so sound never drifts behind the picture.
const maxAudioBacklog = 8

// AudioOut plays the host's Opus packets: appsrc, opusdec with packet-loss
// concealment, and pulsesink (PipeWire) with a 60 ms buffer and no clock
// sync, so each packet plays as soon as it arrives, like the video.
type AudioOut struct {
	pipeline gst.Pipeline
	src      gstapp.AppSrc

	mu      sync.Mutex
	last    uint16
	started bool
}

// NewAudioOut builds and starts the playback pipeline.
func NewAudioOut() (*AudioOut, error) {
	desc := `appsrc name=asrc is-live=true format=time do-timestamp=true ` +
		`caps="audio/x-opus,channel-mapping-family=(int)0,channels=(int)2,rate=(int)48000" ! ` +
		`opusdec plc=true ! audioconvert ! audioresample ! pulsesink buffer-time=60000 latency-time=10000 sync=false`
	el, err := gst.ParseLaunch(desc)
	if err != nil {
		return nil, fmt.Errorf("parse audio pipeline: %w", err)
	}
	pipeline, ok := el.(gst.Pipeline)
	if !ok {
		return nil, errors.New("audio pipeline is not a GstPipeline")
	}
	src, ok := pipeline.GetByName("asrc").(gstapp.AppSrc)
	if !ok {
		return nil, errors.New("audio pipeline lacks its appsrc")
	}
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		pipeline.SetState(gst.StateNull)
		return nil, errors.New("audio pipeline refused to start (is gstreamer1-plugins-good installed?)")
	}
	return &AudioOut{pipeline: pipeline, src: src}, nil
}

// Push plays one packet; late or repeated packets and packets beyond the
// backlog are dropped. It runs on the network goroutine.
func (a *AudioOut) Push(p dlnet.AudioPacket) {
	a.mu.Lock()
	if a.started {
		if ahead := p.Counter - a.last; ahead == 0 || ahead > 0x8000 {
			a.mu.Unlock()
			return
		}
	}
	a.last, a.started = p.Counter, true
	a.mu.Unlock()
	if a.src.GetCurrentLevelBuffers() > maxAudioBacklog {
		return
	}
	if ret := a.src.PushBuffer(gst.NewBufferWrappedBytes(glib.NewBytes(p.Data))); ret != gst.FlowOK {
		slog.Debug("audio output refused a packet", "flow", ret)
	}
}

// Close stops playback.
func (a *AudioOut) Close() {
	a.pipeline.SetState(gst.StateNull)
}
