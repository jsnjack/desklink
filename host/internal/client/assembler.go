package client

import (
	"slices"
	"sync"

	dlnet "desklink/internal/net"
)

// EncodedFrame is a complete encoded frame, released in decode order.
type EncodedFrame struct {
	ID            uint16
	Data          []byte
	Keyframe      bool
	CaptureMicros uint64
}

// lossDistance is how far ahead the newest frame must be before an
// incomplete expected frame counts as lost.
const lossDistance = 2

// maxBuffered bounds the frames held while waiting.
const maxBuffered = 64

type partialFrame struct {
	parts    [][]byte
	received int
	size     int
	// known is set once a fragment has given the frame's keyframe flag
	// and capture time; a frame first seen through parity lacks them.
	known    bool
	keyframe bool
	capture  uint64
	parity   []dlnet.ParityPacket
}

// FrameAssembler reassembles fragments into frames and releases them in
// frame order. A parity packet rebuilds the one missing fragment of its group
// once the group's other fragments are in; an incomplete frame keeps only the
// first parity packet of each group. When the expected frame is still incomplete and a frame two
// IDs newer has arrived, the expected one counts as lost: frames are then
// dropped until a keyframe arrives, because frames predicted from a lost one
// would decode as garbage, and NeedKeyframe asks the host for one. The
// Android client's FrameAssembler follows the same rules.
type FrameAssembler struct {
	mu           sync.Mutex
	partial      map[uint16]*partialFrame
	ready        map[uint16]EncodedFrame
	next         uint16
	haveNext     bool
	newest       uint16
	haveNewest   bool
	needKeyframe bool
}

// NewFrameAssembler waits for a keyframe first.
func NewFrameAssembler() *FrameAssembler {
	return &FrameAssembler{partial: map[uint16]*partialFrame{}, ready: map[uint16]EncodedFrame{}, needKeyframe: true}
}

// distance is the signed distance a - b on the 16-bit frame ID circle.
func distance(a, b uint16) int { return int(int16(a - b)) }

// NeedKeyframe reports whether the assembler is waiting for a keyframe.
func (a *FrameAssembler) NeedKeyframe() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.needKeyframe
}

// ForceKeyframe drops frames until the next keyframe, for decoder errors.
func (a *FrameAssembler) ForceKeyframe() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.needKeyframe = true
}

// LastContiguous is the frame before the next one expected.
func (a *FrameAssembler) LastContiguous() uint16 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.next - 1
}

// Add takes one fragment and returns the frames it made ready.
func (a *FrameAssembler) Add(f dlnet.VideoFragment) []EncodedFrame {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.haveNext && distance(f.FrameID, a.next) < 0 {
		return nil
	}
	if !a.haveNewest || distance(f.FrameID, a.newest) > 0 {
		a.newest, a.haveNewest = f.FrameID, true
	}
	if _, done := a.ready[f.FrameID]; done {
		return nil
	}
	p := a.partial[f.FrameID]
	if p == nil {
		p = &partialFrame{parts: make([][]byte, f.Count)}
		a.partial[f.FrameID] = p
	}
	if int(f.Count) == len(p.parts) {
		p.insert(f)
		a.recover(f.FrameID, p)
		a.complete(f.FrameID, p)
	}
	return a.drain()
}

// AddParity takes one parity packet and returns the frames it made ready.
func (a *FrameAssembler) AddParity(q dlnet.ParityPacket) []EncodedFrame {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.haveNext && distance(q.FrameID, a.next) < 0 {
		return nil
	}
	if _, done := a.ready[q.FrameID]; done {
		return nil
	}
	if !a.haveNewest || distance(q.FrameID, a.newest) > 0 {
		a.newest, a.haveNewest = q.FrameID, true
	}
	p := a.partial[q.FrameID]
	if p == nil {
		p = &partialFrame{parts: make([][]byte, q.Count)}
		a.partial[q.FrameID] = p
	}
	if int(q.Count) != len(p.parts) {
		return nil
	}
	// The first record of a group wins: a repeat can neither add
	// information nor be allowed to pile up in an incomplete frame.
	if slices.ContainsFunc(p.parity, func(r dlnet.ParityPacket) bool { return r.Group == q.Group }) {
		return nil
	}
	p.parity = append(p.parity, dlnet.ParityPacket{
		FrameID: q.FrameID, Group: q.Group, Stride: q.Stride, Count: q.Count, LenXor: q.LenXor,
		Data: append([]byte(nil), q.Data...),
	})
	a.recover(q.FrameID, p)
	a.complete(q.FrameID, p)
	return a.drain()
}

func (p *partialFrame) insert(f dlnet.VideoFragment) {
	if p.parts[f.Index] != nil {
		return
	}
	if !p.known {
		p.known, p.keyframe, p.capture = true, f.Keyframe, f.CaptureMicros
	}
	p.parts[f.Index] = append([]byte(nil), f.Data...)
	p.received++
	p.size += len(f.Data)
}

// recover rebuilds fragments from parity packets whose group lacks exactly
// one fragment, until no group can help.
func (a *FrameAssembler) recover(id uint16, p *partialFrame) {
	for progress := true; progress && p.received < len(p.parts); {
		progress = false
		for _, q := range p.parity {
			missing := -1
			var present [][]byte
			for _, i := range q.Members() {
				if p.parts[i] == nil {
					if missing >= 0 {
						missing = -2 // two or more lost: this group cannot help
						break
					}
					missing = i
					continue
				}
				present = append(present, p.payload(i))
			}
			if missing < 0 {
				continue
			}
			payload := q.Recover(present)
			h := dlnet.Header{Type: dlnet.PacketVideoFrag, FrameID: id, FragIdx: uint8(missing), FragCnt: uint8(len(p.parts))}
			f, err := dlnet.ParseVideoFragment(h, payload)
			if err != nil || int(f.Index) != missing || int(f.Count) != len(p.parts) {
				continue
			}
			p.insert(f)
			progress = true
		}
	}
}

// payload rebuilds fragment i's video payload as the host sent it.
func (p *partialFrame) payload(i int) []byte {
	return dlnet.VideoFragment{
		Index: uint16(i), Count: uint16(len(p.parts)), Keyframe: p.keyframe, CaptureMicros: p.capture, Data: p.parts[i],
	}.AppendPayload(nil)
}

func (a *FrameAssembler) complete(id uint16, p *partialFrame) {
	if p.received != len(p.parts) {
		return
	}
	delete(a.partial, id)
	data := make([]byte, 0, p.size)
	for _, part := range p.parts {
		data = append(data, part...)
	}
	a.ready[id] = EncodedFrame{ID: id, Data: data, Keyframe: p.keyframe, CaptureMicros: p.capture}
}

func (a *FrameAssembler) drain() []EncodedFrame {
	var out []EncodedFrame
	for {
		if a.needKeyframe {
			key, ok := a.oldestReadyKeyframe()
			if !ok {
				break
			}
			a.discardBefore(key)
			a.needKeyframe = false
			a.next, a.haveNext = key, true
		}
		if !a.haveNext {
			break
		}
		if f, ok := a.ready[a.next]; ok {
			delete(a.ready, a.next)
			out = append(out, f)
			a.next++
			continue
		}
		if a.haveNewest && distance(a.newest, a.next) >= lossDistance {
			a.needKeyframe = true
			continue
		}
		break
	}
	a.trim()
	return out
}

func (a *FrameAssembler) oldestReadyKeyframe() (uint16, bool) {
	var best uint16
	found := false
	for id, f := range a.ready {
		if f.Keyframe && (!found || distance(id, best) < 0) {
			best, found = id, true
		}
	}
	return best, found
}

func (a *FrameAssembler) discardBefore(id uint16) {
	for k := range a.ready {
		if distance(k, id) < 0 {
			delete(a.ready, k)
		}
	}
	for k := range a.partial {
		if distance(k, id) < 0 {
			delete(a.partial, k)
		}
	}
}

func (a *FrameAssembler) trim() {
	if len(a.partial)+len(a.ready) <= maxBuffered || !a.haveNewest {
		return
	}
	for k := range a.partial {
		if distance(a.newest, k) > maxBuffered {
			delete(a.partial, k)
		}
	}
	for k := range a.ready {
		if distance(a.newest, k) > maxBuffered {
			delete(a.ready, k)
		}
	}
}

// LossMeter estimates packet loss from gaps in the host's sequence numbers.
// Every host packet takes a sequence number, so every packet is counted.
type LossMeter struct {
	mu          sync.Mutex
	highest     uint32
	windowStart uint32
	started     bool
	received    uint32
}

// Observe records a received sequence number.
func (m *LossMeter) Observe(seq uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		m.windowStart, m.started = seq-1, true
	}
	if seq > m.highest {
		m.highest = seq
	}
	m.received++
}

// Sample returns the loss since the previous sample in hundredths of a percent.
func (m *LossMeter) Sample() uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return 0
	}
	expected := int64(m.highest) - int64(m.windowStart)
	lost := max(expected-int64(m.received), 0)
	var out uint16
	if expected > 0 {
		out = uint16(lost * 10_000 / expected)
	}
	m.windowStart, m.received = m.highest, 0
	return out
}
