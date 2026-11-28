// Package input injects client input into the kernel through /dev/uinput as
// a virtual keyboard, a relative mouse and an absolute pen digitizer.
package input

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// DevicePath is the uinput character device.
const DevicePath = "/dev/uinput"

// CheckAccess returns nil when this process may create devices through the
// uinput node at path, and otherwise why not. It opens nothing.
func CheckAccess(path string) error {
	if err := unix.Access(path, unix.W_OK); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Linux input event types and codes from linux/input-event-codes.h.
const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	synReport = 0

	relX             = 0x00
	relY             = 0x01
	relHWheel        = 0x06
	relWheel         = 0x08
	relWheelHiRes    = 0x0b
	relHWheelHiRes   = 0x0c
	absX             = 0x00
	absY             = 0x01
	absPressure      = 0x18
	absTiltX         = 0x1a
	absTiltY         = 0x1b
	btnLeft          = 0x110
	btnRight         = 0x111
	btnMiddle        = 0x112
	btnSide          = 0x113
	btnExtra         = 0x114
	btnToolPen       = 0x140
	btnTouch         = 0x14a
	inputPropPointer = 0x00

	busVirtual = 0x06
)

// uinput ioctl numbers from linux/uinput.h.
const (
	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502
	uiDevSetup   = 0x405c5503
	uiAbsSetup   = 0x401c5504
	uiSetEvBit   = 0x40045564
	uiSetKeyBit  = 0x40045565
	uiSetRelBit  = 0x40045566
	uiSetAbsBit  = 0x40045567
	uiSetPropBit = 0x4004556e
)

// AbsMax is the top of the 0..65535 digitizer range.
const AbsMax = 65535

// PressureMax is full pen pressure; the wire carries 1..PressureMax while
// the pen touches.
const PressureMax = 65535

// TiltMax is the pen tilt range, ±90 degrees in hundredths of a degree.
const TiltMax = 9000

// tiltResolution is tilt units per radian (100 * 180 / pi), which libinput
// uses to turn the axis into degrees.
const tiltResolution = 5730

// WheelUnit is one wheel notch in the scroll event's 1/120 units.
const WheelUnit = 120

const (
	// absResolution is units per millimetre, about the Tab S9's 11-inch panel.
	absResolution = 270
	vendorID      = 0x1d6b
	productKbd    = 0xd101
	productMouse  = 0xd102
	productPen    = 0xd103
	productAbs    = 0xd104
)

// Event kinds, matching the wire protocol.
const (
	KindKey         = 1
	KindRelMove     = 2
	KindAbsMove     = 3
	KindMouseButton = 4
	KindScroll      = 5
	KindAbsPointer  = 6
	KindHeld        = 7
)

// Held is the client's snapshot of what it holds down. Buttons use the wire
// codes 1..5.
type Held struct {
	Keys       []uint16
	Buttons    []uint16
	PenTouch   bool
	PenInRange bool
}

// Pen flags of KindAbsMove events.
const (
	PenHover     = 0
	PenTouch     = 1
	PenProximity = 2 // the pen left the screen
)

// Event is one client input record. For a pen record (KindAbsMove) Code is
// the pressure, 1..PressureMax while touching or 0 when the client does not
// report it, and Tilt says whether TiltX and TiltY, in hundredths of a degree
// towards +x and +y, are known.
type Event struct {
	Kind  uint8
	Flags uint8
	Code  uint16
	X, Y  int32

	Tilt         bool
	TiltX, TiltY int32
}

var buttonCodes = map[uint16]uint16{1: btnLeft, 2: btnRight, 3: btnMiddle, 4: btnSide, 5: btnExtra}

type rawEvent struct {
	typ, code uint16
	value     int32
}

// encodeEvents writes struct input_event records for a 64-bit kernel. The
// timestamp is left zero; the input core stamps injected events itself.
func encodeEvents(events []rawEvent) []byte {
	var buf bytes.Buffer
	for _, e := range events {
		var rec [24]byte
		binary.LittleEndian.PutUint16(rec[16:], e.typ)
		binary.LittleEndian.PutUint16(rec[18:], e.code)
		binary.LittleEndian.PutUint32(rec[20:], uint32(e.value))
		buf.Write(rec[:])
	}
	return buf.Bytes()
}

// Injector owns the three virtual devices and tracks pressed keys and buttons
// so ReleaseAll can lift them when a session ends mid-press. It also records
// the sequence number of the packet that last changed each key, button and
// the pen, so Reconcile only acts on snapshots newer than that change.
//
// A press counts as soon as it is tried, even when its write fails, because
// the kernel ignores the release of something it never saw pressed. A release
// counts only once it is written: one that failed stays pending, so the next
// Reconcile or ReleaseAll sends it again instead of leaving a key repeating.
type Injector struct {
	kbd, mouse, pen, abs io.Writer
	closers              []func() error

	mu      sync.Mutex
	keys    map[uint16]bool
	buttons map[uint16]bool
	seqs    map[seqKey]uint32
	// absActive is set while the pointer was last moved by absolute
	// records. Clicks then go through the absolute device too: libinput reads
	// each device separately, so a click on the relative mouse could be
	// processed before the move and land at the old position.
	absActive bool
	// pressedOn is the device each held button was pressed on, which must
	// also release it.
	pressedOn map[uint16]io.Writer
	penIn     bool
	penDown   bool
	// pressure, tiltX and tiltY are the pen axes last written.
	pressure     int32
	tiltX, tiltY int32
	wheelRest    [2]int32
}

// seqKey names a key (evKey code), a button (evdev code) or the pen.
type seqKey struct {
	device byte
	code   uint16
}

const (
	seqKeyboard byte = iota
	seqButton
	seqPen
)

func newInjector(kbd, mouse, pen, abs io.Writer) *Injector {
	return &Injector{
		kbd: kbd, mouse: mouse, pen: pen, abs: abs,
		keys: map[uint16]bool{}, buttons: map[uint16]bool{}, seqs: map[seqKey]uint32{}, pressedOn: map[uint16]io.Writer{},
	}
}

// OpenInjector creates the virtual devices on the uinput device at path.
func OpenInjector(path string) (*Injector, error) {
	kbd, err := createDevice(path, "DeskLink Keyboard", productKbd, func(fd uintptr) error {
		if err := ioctlInt(fd, uiSetEvBit, evKey); err != nil {
			return err
		}
		for code := MinKey; code <= MaxKey; code++ {
			if err := ioctlInt(fd, uiSetKeyBit, code); err != nil {
				return err
			}
		}
		return nil
	}, nil)
	if err != nil {
		return nil, err
	}
	mouse, err := createDevice(path, "DeskLink Mouse", productMouse, func(fd uintptr) error {
		for _, bit := range [][2]int{{uiSetEvBit, evKey}, {uiSetEvBit, evRel}} {
			if err := ioctlInt(fd, uintptr(bit[0]), bit[1]); err != nil {
				return err
			}
		}
		for _, b := range []int{btnLeft, btnRight, btnMiddle, btnSide, btnExtra} {
			if err := ioctlInt(fd, uiSetKeyBit, b); err != nil {
				return err
			}
		}
		for _, r := range []int{relX, relY, relWheel, relHWheel, relWheelHiRes, relHWheelHiRes} {
			if err := ioctlInt(fd, uiSetRelBit, r); err != nil {
				return err
			}
		}
		return nil
	}, nil)
	if err != nil {
		return nil, errors.Join(err, kbd.Close())
	}
	pen, err := createDevice(path, "DeskLink Pen", productPen, func(fd uintptr) error {
		for _, bit := range [][2]int{{uiSetEvBit, evKey}, {uiSetEvBit, evAbs}, {uiSetPropBit, inputPropPointer}} {
			if err := ioctlInt(fd, uintptr(bit[0]), bit[1]); err != nil {
				return err
			}
		}
		for _, b := range []int{btnToolPen, btnTouch} {
			if err := ioctlInt(fd, uiSetKeyBit, b); err != nil {
				return err
			}
		}
		for _, a := range []int{absX, absY, absPressure, absTiltX, absTiltY} {
			if err := ioctlInt(fd, uiSetAbsBit, a); err != nil {
				return err
			}
		}
		return nil
	}, func(fd uintptr) error {
		for _, a := range []uint16{absX, absY} {
			if err := absSetup(fd, a, 0, AbsMax, absResolution); err != nil {
				return err
			}
		}
		if err := absSetup(fd, absPressure, 0, PressureMax, 0); err != nil {
			return err
		}
		for _, a := range []uint16{absTiltX, absTiltY} {
			if err := absSetup(fd, a, -TiltMax, TiltMax, tiltResolution); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.Join(err, kbd.Close(), mouse.Close())
	}
	// Absolute axes with mouse buttons and no touch or tool bits make udev and
	// libinput treat this as an absolute mouse, like a virtual machine's tablet.
	abs, err := createDevice(path, "DeskLink Pointer", productAbs, func(fd uintptr) error {
		for _, bit := range [][2]int{{uiSetEvBit, evKey}, {uiSetEvBit, evAbs}} {
			if err := ioctlInt(fd, uintptr(bit[0]), bit[1]); err != nil {
				return err
			}
		}
		for _, b := range []int{btnLeft, btnRight, btnMiddle} {
			if err := ioctlInt(fd, uiSetKeyBit, b); err != nil {
				return err
			}
		}
		for _, a := range []int{absX, absY} {
			if err := ioctlInt(fd, uiSetAbsBit, a); err != nil {
				return err
			}
		}
		return nil
	}, func(fd uintptr) error {
		for _, a := range []uint16{absX, absY} {
			if err := absSetup(fd, a, 0, AbsMax, 0); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.Join(err, kbd.Close(), mouse.Close(), pen.Close())
	}
	in := newInjector(kbd, mouse, pen, abs)
	in.closers = []func() error{kbd.Close, mouse.Close, pen.Close, abs.Close}
	return in, nil
}

// Handle injects one event that arrived without a sequence number.
func (in *Injector) Handle(ev Event) error { return in.HandleAt(0, ev) }

// HandleAt injects one event from the packet with sequence number seq.
func (in *Injector) HandleAt(seq uint32, ev Event) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch ev.Kind {
	case KindKey:
		if in.stale(seqKey{seqKeyboard, ev.Code}, seq) {
			return nil
		}
		return in.key(ev.Code, ev.Flags == 1)
	case KindRelMove:
		in.absActive = false
		var evs []rawEvent
		if ev.X != 0 {
			evs = append(evs, rawEvent{evRel, relX, ev.X})
		}
		if ev.Y != 0 {
			evs = append(evs, rawEvent{evRel, relY, ev.Y})
		}
		return in.write(in.mouse, evs...)
	case KindAbsMove:
		if in.stale(seqKey{seqPen, 0}, seq) {
			return nil
		}
		return in.penEvent(ev)
	case KindMouseButton:
		code, ok := buttonCodes[ev.Code]
		if !ok {
			return fmt.Errorf("unknown mouse button %d", ev.Code)
		}
		if in.stale(seqKey{seqButton, code}, seq) {
			return nil
		}
		down := ev.Flags == 1
		if in.buttons[code] == down {
			return nil
		}
		if !down {
			return in.releaseButton(code)
		}
		dev := in.buttonDevice(code)
		in.buttons[code], in.pressedOn[code] = true, dev
		return in.write(dev, rawEvent{evKey, code, 1})
	case KindScroll:
		return in.scroll(ev.X, ev.Y)
	case KindAbsPointer:
		in.absActive = true
		return in.write(in.abs, rawEvent{evAbs, absX, clamp(ev.X, 0, AbsMax)}, rawEvent{evAbs, absY, clamp(ev.Y, 0, AbsMax)})
	default:
		return fmt.Errorf("unknown input kind %d", ev.Kind)
	}
}

// buttonDevice picks the device for a button press: the absolute pointer
// when it moved the pointer last and has the button, otherwise the relative
// mouse. The caller holds mu.
func (in *Injector) buttonDevice(code uint16) io.Writer {
	if in.absActive && (code == btnLeft || code == btnRight || code == btnMiddle) {
		return in.abs
	}
	return in.mouse
}

// releaseButton lifts a held button on the device that pressed it. The button
// stays held, on that device, until the release is written, so a retry goes
// to the same device. The caller holds mu.
func (in *Injector) releaseButton(code uint16) error {
	dev, ok := in.pressedOn[code]
	if !ok {
		dev = in.mouse
	}
	if err := in.write(dev, rawEvent{evKey, code, 0}); err != nil {
		return err
	}
	delete(in.buttons, code)
	delete(in.pressedOn, code)
	return nil
}

// stale reports whether a packet older than the last one that changed k
// arrived late, and otherwise records seq for k. A reordered key-up would
// otherwise undo a newer key-down. Events without a sequence number are
// never stale.
func (in *Injector) stale(k seqKey, seq uint32) bool {
	if seq != 0 && seq < in.seqs[k] {
		return true
	}
	in.seqs[k] = seq
	return false
}

func (in *Injector) key(code uint16, down bool) error {
	if !ValidKey(code) {
		return fmt.Errorf("key code %d is outside %d..%d", code, MinKey, MaxKey)
	}
	if in.keys[code] == down {
		// Android repeats key-down while a key is held; the compositor repeats keys itself.
		return nil
	}
	if !down {
		return in.releaseKey(code)
	}
	in.keys[code] = true
	return in.write(in.kbd, rawEvent{evKey, code, 1})
}

// releaseKey lifts a held key, which stays held until the release is
// written. The caller holds mu.
func (in *Injector) releaseKey(code uint16) error {
	if err := in.write(in.kbd, rawEvent{evKey, code, 0}); err != nil {
		return err
	}
	delete(in.keys, code)
	return nil
}

// scroll takes 1/120-notch units and emits high-resolution wheel events plus
// a classic wheel event for every whole notch accumulated.
func (in *Injector) scroll(hwheel, wheel int32) error {
	var evs []rawEvent
	for i, v := range []int32{wheel, hwheel} {
		if v == 0 {
			continue
		}
		hiRes, classic := uint16(relWheelHiRes), uint16(relWheel)
		if i == 1 {
			hiRes, classic = relHWheelHiRes, relHWheel
		}
		evs = append(evs, rawEvent{evRel, hiRes, v})
		in.wheelRest[i] += v
		if notches := in.wheelRest[i] / WheelUnit; notches != 0 {
			in.wheelRest[i] -= notches * WheelUnit
			evs = append(evs, rawEvent{evRel, classic, notches})
		}
	}
	return in.write(in.mouse, evs...)
}

func (in *Injector) penEvent(ev Event) error {
	var evs []rawEvent
	if ev.Flags == PenProximity {
		if !in.penIn {
			return nil
		}
		return in.writePenLift(nil, true)
	}
	if !in.penIn {
		in.penIn = true
		evs = append(evs, rawEvent{evKey, btnToolPen, 1})
	}
	evs = append(evs, rawEvent{evAbs, absX, clamp(ev.X, 0, AbsMax)}, rawEvent{evAbs, absY, clamp(ev.Y, 0, AbsMax)})
	if ev.Tilt {
		if x := clamp(ev.TiltX, -TiltMax, TiltMax); x != in.tiltX {
			in.tiltX = x
			evs = append(evs, rawEvent{evAbs, absTiltX, x})
		}
		if y := clamp(ev.TiltY, -TiltMax, TiltMax); y != in.tiltY {
			in.tiltY = y
			evs = append(evs, rawEvent{evAbs, absTiltY, y})
		}
	}
	down := ev.Flags == PenTouch
	if !down {
		return in.writePenLift(evs, false)
	}
	// A client that does not report pressure draws at full pressure.
	pressure := int32(ev.Code)
	if pressure == 0 {
		pressure = PressureMax
	}
	if pressure != in.pressure {
		in.pressure = pressure
		evs = append(evs, rawEvent{evAbs, absPressure, pressure})
	}
	if !in.penDown {
		in.penDown = true
		evs = append(evs, rawEvent{evKey, btnTouch, 1})
	}
	return in.write(in.pen, evs...)
}

// writePenLift writes evs followed by the events that take the pen off the
// screen, if it is on it: no contact and no pressure, and with leave, out of
// range too. The pen counts as lifted only once the report is written, so a
// lift that failed is sent again. The caller holds mu.
func (in *Injector) writePenLift(evs []rawEvent, leave bool) error {
	if in.penDown {
		evs = append(evs, rawEvent{evKey, btnTouch, 0})
	}
	if in.pressure != 0 {
		evs = append(evs, rawEvent{evAbs, absPressure, 0})
	}
	if leave {
		evs = append(evs, rawEvent{evKey, btnToolPen, 0})
	}
	if err := in.write(in.pen, evs...); err != nil {
		return err
	}
	in.penDown, in.pressure = false, 0
	if leave {
		in.penIn = false
	}
	return nil
}

// Reconcile lifts every key, button or pen contact that the client's
// snapshot from packet seq no longer holds, unless a newer packet changed it.
// It never presses anything: a press the snapshot shows but the host missed
// would arrive late and out of context.
func (in *Injector) Reconcile(seq uint32, h Held) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	older := func(k seqKey) bool { return in.seqs[k] < seq }
	var errs []error
	for code, down := range in.keys {
		if down && !slices.Contains(h.Keys, code) && older(seqKey{seqKeyboard, code}) {
			errs = append(errs, in.releaseKey(code))
		}
	}
	held := make([]uint16, 0, len(h.Buttons))
	for _, b := range h.Buttons {
		if code, ok := buttonCodes[b]; ok {
			held = append(held, code)
		}
	}
	for code, down := range in.buttons {
		if down && !slices.Contains(held, code) && older(seqKey{seqButton, code}) {
			errs = append(errs, in.releaseButton(code))
		}
	}
	if older(seqKey{seqPen, 0}) && !h.PenTouch {
		errs = append(errs, in.writePenLift(nil, in.penIn && !h.PenInRange))
	}
	return errors.Join(errs...)
}

// ReleaseAll lifts every pressed key, button and the pen. A release that
// fails stays pending for the next session's first snapshot or ReleaseAll.
func (in *Injector) ReleaseAll() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	var errs []error
	for code, down := range in.keys {
		if down {
			errs = append(errs, in.releaseKey(code))
		}
	}
	for code, down := range in.buttons {
		if down {
			errs = append(errs, in.releaseButton(code))
		}
	}
	if in.penIn {
		errs = append(errs, in.writePenLift(nil, true))
	}
	// The next session counts sequence numbers from 1 again, so its first
	// snapshot is newer than every change recorded here.
	in.seqs, in.absActive = map[seqKey]uint32{}, false
	in.wheelRest = [2]int32{}
	return errors.Join(errs...)
}

// Close destroys the devices.
func (in *Injector) Close() error {
	var errs []error
	for _, c := range in.closers {
		errs = append(errs, c())
	}
	return errors.Join(errs...)
}

// write sends one report: the events followed by SYN_REPORT, in one write so
// the kernel never sees half a report.
func (in *Injector) write(w io.Writer, evs ...rawEvent) error {
	if len(evs) == 0 {
		return nil
	}
	if _, err := w.Write(encodeEvents(append(evs, rawEvent{evSyn, synReport, 0}))); err != nil {
		return fmt.Errorf("write uinput events: %w", err)
	}
	return nil
}

func clamp(v, lo, hi int32) int32 {
	return max(lo, min(v, hi))
}

type device struct {
	f *os.File
}

func (d *device) Write(p []byte) (int, error) { return d.f.Write(p) }

func (d *device) Close() error {
	errDestroy := ioctlInt(d.f.Fd(), uiDevDestroy, 0)
	return errors.Join(errDestroy, d.f.Close())
}

// uinputSetup mirrors struct uinput_setup.
type uinputSetup struct {
	BusType, Vendor, Product, Version uint16
	Name                              [80]byte
	FFEffectsMax                      uint32
}

// uinputAbsSetup mirrors struct uinput_abs_setup.
type uinputAbsSetup struct {
	Code                                            uint16
	_                                               uint16
	Value, Minimum, Maximum, Fuzz, Flat, Resolution int32
}

func createDevice(path, name string, product uint16, bits, abs func(fd uintptr) error) (*device, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s (see deploy/60-desklink.rules): %w", path, err)
	}
	fd := f.Fd()
	fail := func(err error) (*device, error) {
		return nil, errors.Join(fmt.Errorf("create %s: %w", name, err), f.Close())
	}
	if err := bits(fd); err != nil {
		return fail(err)
	}
	if abs != nil {
		if err := abs(fd); err != nil {
			return fail(err)
		}
	}
	setup := uinputSetup{BusType: busVirtual, Vendor: vendorID, Product: product, Version: 1}
	copy(setup.Name[:], name)
	if err := ioctlPtr(fd, uiDevSetup, unsafe.Pointer(&setup)); err != nil {
		return fail(err)
	}
	if err := ioctlInt(fd, uiDevCreate, 0); err != nil {
		return fail(err)
	}
	return &device{f: f}, nil
}

func absSetup(fd uintptr, code uint16, minValue, maxValue, resolution int32) error {
	s := uinputAbsSetup{Code: code, Minimum: minValue, Maximum: maxValue, Resolution: resolution}
	return ioctlPtr(fd, uiAbsSetup, unsafe.Pointer(&s))
}

func ioctlInt(fd, req uintptr, arg int) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return fmt.Errorf("ioctl %#x: %w", req, errno)
	}
	return nil
}

func ioctlPtr(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return fmt.Errorf("ioctl %#x: %w", req, errno)
	}
	return nil
}
