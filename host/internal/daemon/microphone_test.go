package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"

	dlnet "desklink/internal/net"
)

// fakeMic records the frames played into it and how often it was closed;
// closing fails while failClose is set.
type fakeMic struct {
	mu        sync.Mutex
	frames    []uint16
	closed    int
	failClose bool
}

func (m *fakeMic) Push(counter uint16, _ []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frames = append(m.frames, counter)
}

func (m *fakeMic) Close(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed++
	if m.failClose {
		return errors.New("pactl unload-module failed")
	}
	return nil
}

func (m *fakeMic) played() []uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]uint16(nil), m.frames...)
}

// withMics makes the session's daemon hand out mics in turn, or err.
func withMics(f sessionFixture, err error, mics ...*fakeMic) *int {
	opened := new(int)
	f.s.d.openMic = func(context.Context) (microphone, error) {
		*opened++
		if err != nil {
			return nil, err
		}
		m := mics[0]
		mics = mics[1:]
		return m, nil
	}
	return opened
}

func micFrame(counter uint16) dlnet.AudioPacket {
	return dlnet.AudioPacket{Counter: counter, Data: []byte{31 << 3}}
}

func TestTheMicrophonePlaysOnlyWhileItIsOn(t *testing.T) {
	f := newSessionFixture(t)
	m := &fakeMic{}
	withMics(f, nil, m)
	ctx := context.Background()
	f.s.HandleMicrophone(micFrame(1))
	if err := f.s.startMic(ctx); err != nil {
		t.Fatal(err)
	}
	if on := f.s.micState(); on == nil || !*on {
		t.Errorf("mic state %v, want on", on)
	}
	f.s.HandleMicrophone(micFrame(2))
	f.s.removeMic(ctx)
	f.s.HandleMicrophone(micFrame(3))
	if got := m.played(); len(got) != 1 || got[0] != 2 {
		t.Errorf("played %v, want only the frame sent while the microphone was on", got)
	}
	if m.closed != 1 {
		t.Errorf("closed %d times, want 1", m.closed)
	}
	if on := f.s.micState(); on == nil || *on {
		t.Errorf("mic state %v, want present and off", on)
	}
}

func TestTurningTheMicrophoneOnTwiceMakesOne(t *testing.T) {
	f := newSessionFixture(t)
	opened := withMics(f, nil, &fakeMic{}, &fakeMic{})
	for range 2 {
		if err := f.s.startMic(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if *opened != 1 {
		t.Errorf("opened %d microphones, want 1", *opened)
	}
}

func TestAMicrophoneThatCouldNotBeMadeIsReportedOff(t *testing.T) {
	f := newSessionFixture(t)
	withMics(f, errors.New("pactl: not found"), nil)
	if err := f.s.startMic(context.Background()); err == nil {
		t.Fatal("startMic succeeded")
	}
	if on := f.s.micState(); on == nil || *on {
		t.Errorf("mic state %v, want present and off", on)
	}
	f.s.HandleMicrophone(micFrame(1)) // nothing to play into: no panic
}

func TestCleanupRemovesTheMicrophone(t *testing.T) {
	t.Run("one that is on", func(t *testing.T) {
		f := newSessionFixture(t)
		m := &fakeMic{}
		withMics(f, nil, m)
		if err := f.s.startMic(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.s.cleanup()
		if m.closed != 1 {
			t.Errorf("closed %d times, want 1", m.closed)
		}
		f.s.HandleMicrophone(micFrame(9))
		if len(m.played()) != 0 {
			t.Error("a frame after the session ended was played")
		}
	})
	t.Run("one whose removal failed while the session ran", func(t *testing.T) {
		f := newSessionFixture(t)
		m := &fakeMic{failClose: true}
		withMics(f, nil, m)
		if err := f.s.startMic(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.s.removeMic(context.Background())
		m.failClose = false
		f.s.cleanup()
		if m.closed != 2 {
			t.Errorf("closed %d times, want the failed removal tried again", m.closed)
		}
	})
}

func TestANewMicrophoneWaitsForTheOldOneToGo(t *testing.T) {
	f := newSessionFixture(t)
	old := &fakeMic{failClose: true}
	opened := withMics(f, nil, old, &fakeMic{})
	ctx := context.Background()
	if err := f.s.startMic(ctx); err != nil {
		t.Fatal(err)
	}
	f.s.removeMic(ctx)
	if err := f.s.startMic(ctx); err == nil {
		t.Error("a second microphone was made while the first could not be removed")
	}
	old.failClose = false
	if err := f.s.startMic(ctx); err != nil {
		t.Fatal(err)
	}
	if *opened != 2 || old.closed != 3 {
		t.Errorf("opened %d, old closed %d times; want 2 and 3", *opened, old.closed)
	}
}
