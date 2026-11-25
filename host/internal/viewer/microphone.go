package viewer

import (
	"desklink/internal/client"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

// micWanted reports whether this computer's microphone streams to the desk:
// the user turned it on (chosen) and the host reports that its virtual
// microphone plays it. Nothing is captured before the host has said so, or
// after the user turned it off.
func micWanted(chosen bool, info dlnet.SessionInfo) bool {
	return chosen && info.Mic != nil && *info.Mic
}

// syncMic starts or stops capturing the microphone to match the session's
// latest description. It runs on the main thread.
func (v *streamView) syncMic(info dlnet.SessionInfo) {
	want := v.session != nil && micWanted(v.w.opts.Store.Mic(), info)
	if want == (v.mic != nil) {
		return
	}
	if !want {
		v.stopMic()
		return
	}
	s := v.session
	enc, err := video.StartMicrophoneCapture(func(f video.Frame) { s.SendMicrophone(f.Data, f.Captured) })
	if err != nil {
		v.w.toast("Could not open the microphone: %v", err)
		return
	}
	v.mic = enc
}

func (v *streamView) stopMic() {
	if v.mic != nil {
		v.mic.Stop()
		v.mic = nil
	}
}

// setMic saves whether sessions stream the microphone and asks the host for
// its virtual microphone, or to remove it. Turning it off stops capturing at
// once instead of waiting for the host's answer.
func (v *streamView) setMic(on bool) {
	if v.session == nil {
		return
	}
	if err := v.w.opts.Store.SetMic(on); err != nil {
		v.w.toast("Could not save the microphone choice: %v", err)
	}
	if !on {
		v.stopMic()
	}
	v.request(func(s *client.Session) error { return s.SetMic(on) })
}
