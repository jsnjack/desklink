package video

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gstapp"
)

func TestMicrophonePipelineDescription(t *testing.T) {
	desc := MicrophonePipelineDescription("desklink_mic_feed")
	for _, want := range []string{
		"appsrc name=micsrc is-live=true", "leaky-type=downstream", "rate=48000,channels=1,channel-mapping-family=0",
		"opusdec plc=true", "pulsesink device=desklink_mic_feed sync=false",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("pipeline %q lacks %q", desc, want)
		}
	}
}

func TestMicrophoneCaptureDescription(t *testing.T) {
	desc := MicrophoneCaptureDescription()
	for _, want := range []string{"pulsesrc ", "rate=48000,channels=1", "opusenc frame-size=20 bitrate=32000 audio-type=voice", "drop=false"} {
		if !strings.Contains(desc, want) {
			t.Errorf("pipeline %q lacks %q", desc, want)
		}
	}
}

// encodeTone encodes n frames of a 440 Hz tone with the laptop client's
// microphone encoder.
func encodeTone(t *testing.T, n int) [][]byte {
	t.Helper()
	var mu sync.Mutex
	var frames [][]byte
	enc, err := startAudio("audiotestsrc num-buffers="+itoa(n)+" samplesperbuffer=960 freq=440 ! "+micEncodeDescription(), func(f Frame) {
		mu.Lock()
		frames = append(frames, f.Data)
		mu.Unlock()
	})
	if err != nil {
		t.Skipf("Opus encoder unavailable: %v", err)
	}
	select {
	case <-enc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("encoding the tone did not finish")
	}
	enc.Stop()
	mu.Lock()
	defer mu.Unlock()
	if len(frames) < n {
		t.Fatalf("%d frames encoded, want %d", len(frames), n)
	}
	return frames[:n]
}

func itoa(n int) string {
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	if len(b) == 0 {
		return "0"
	}
	return string(b)
}

// TestMicrophonePlaysFramesAndDropsLateOnes feeds the receive pipeline, with
// an appsink in place of PipeWire, as a client would: one frame every 20 ms,
// one of them lost and sent again after its turn.
func TestMicrophonePlaysFramesAndDropsLateOnes(t *testing.T) {
	Init()
	frames := encodeTone(t, 30)
	m, err := startMicrophone(micDecodeDescription() + " ! appsink name=micout sync=false max-buffers=1000 drop=false")
	if err != nil {
		t.Skipf("microphone pipeline unavailable: %v", err)
	}
	sink, ok := m.pipeline.GetByName("micout").(gstapp.AppSink)
	if !ok {
		m.Stop()
		t.Fatal("no appsink")
	}
	var pcmBytes atomic.Int64
	pulled := make(chan struct{})
	go func() {
		defer close(pulled)
		for {
			f, ok, err := pullFrame(sink)
			if err != nil || !ok {
				return
			}
			pcmBytes.Add(int64(len(f.data)))
		}
	}()
	const lost = 10
	for i, f := range frames {
		if i != lost {
			m.Push(uint16(i), f)
		}
		if i == lost+10 {
			// Its turn passed some 200 ms ago.
			m.Push(lost, frames[lost])
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	st := m.Stats()
	m.Stop()
	<-pulled
	m.Push(31, frames[0]) // after Stop: dropped, no crash
	t.Logf("%+v, %d samples decoded", st, pcmBytes.Load()/2)

	if st.Late != 1 {
		t.Errorf("%d late frames, want the one sent after its turn", st.Late)
	}
	if st.Concealed < 1 {
		t.Error("the lost frame was not concealed")
	}
	if st.Played < 26 {
		t.Errorf("%d frames played, want about 29", st.Played)
	}
	// Every played or concealed slot comes out as 20 ms of 16-bit mono.
	slots := int64(st.Played + st.Concealed)
	if got := pcmBytes.Load() / 2; got < (slots-2)*960 {
		t.Errorf("%d samples decoded for %d slots of 960", got, slots)
	}
}
