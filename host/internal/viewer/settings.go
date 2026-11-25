package viewer

import (
	"fmt"
	"math"
	"slices"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"desklink/internal/client"
	dlnet "desklink/internal/net"
)

// Own screen scales the settings offer, the tablet's steps.
const (
	minScale  = 1.0
	maxScale  = 2.0
	scaleStep = 0.25
)

// stepScale is the scale direction steps (-1, 0 or +1) from current, on the
// steps from minScale to maxScale. A scale between steps, such as one Mutter
// rounded, counts from the nearest step.
func stepScale(current float64, direction int) float64 {
	next := (math.Round(current/scaleStep) + float64(direction)) * scaleStep
	return min(max(next, minScale), maxScale)
}

// requestScale is the own screen's scale a session asks for: the user's
// saved choice, or else the scale of the laptop monitor showing the window
// (monitor, 0 when unknown), on the nearest step. Without it the host would
// use its default of 200 %, which turns a 1920×1200 own screen into a
// 960×600-point desktop.
func requestScale(saved, monitor float64) float64 {
	switch {
	case saved > 0:
		return stepScale(saved, 0)
	case monitor > 0:
		return stepScale(monitor, 0)
	default:
		return minScale
	}
}

// keepAwakeChoice is one way to keep the computer awake after a session.
type keepAwakeChoice struct {
	name, label, tooltip string
	// minutes is the dlnet.Hello.KeepAwake value.
	minutes int
}

// keepAwakeChoices are the ones the tablet offers.
var keepAwakeChoices = []keepAwakeChoice{
	{"off", "Off", "The computer may suspend as usual", dlnet.KeepAwakeOff},
	{"1h", "1 h", "Stay awake for an hour", 60},
	{"4h", "4 h", "Stay awake for four hours", 240},
	{"return", "∞", "Stay awake until a client connects again", dlnet.KeepAwakeUntilReturn},
}

// screenControls says which settings apply to a session.
type screenControls struct {
	// screen: the host says whether the session shows its own screen or a
	// mirrored monitor. Hosts before the own screen do not.
	screen bool
	// scale: only the own screen has a scale.
	scale bool
	// primary and deskOff: the host includes these only while the session
	// can switch them.
	primary, deskOff bool
}

func controlsFor(info dlnet.SessionInfo) screenControls {
	return screenControls{
		screen:  info.Screen == dlnet.ScreenOwn || info.Screen == dlnet.ScreenMirror,
		scale:   info.Screen == dlnet.ScreenOwn && info.Scale > 0,
		primary: info.Primary != nil,
		deskOff: info.DeskOff != nil,
	}
}

// screenSettings is the stream view's settings menu: what the session shows,
// the own screen's scale, the primary and desk switches and keep-awake. It
// shows the host's latest session description, which the host sends again
// after every change.
type screenSettings struct {
	button     *gtk.MenuButton
	popover    *gtk.Popover
	screenBox  *gtk.Box
	screen     *adw.ToggleGroup
	rows       *gtk.ListBox
	scaleRow   *adw.ActionRow
	scaleLabel *gtk.Label
	scaleDown  *gtk.Button
	scaleUp    *gtk.Button
	primary    *adw.SwitchRow
	deskOff    *adw.SwitchRow
	keepAwake  *adw.ToggleGroup
	lockAfter  *adw.SwitchRow
	// syncing is set while the controls take the host's values, so their
	// change signals do not send those values back.
	syncing bool
}

func heading(text string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.AddCSSClass("heading")
	l.SetXAlign(0)
	return l
}

func hint(text string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.AddCSSClass("dim-label")
	l.AddCSSClass("caption")
	l.SetXAlign(0)
	l.SetWrap(true)
	l.SetMaxWidthChars(40)
	return l
}

func newScreenSettings(v *streamView) *screenSettings {
	s := &screenSettings{}

	s.screen = adw.NewToggleGroup()
	s.screen.SetHomogeneous(true)
	for _, c := range []struct{ name, label string }{{dlnet.ScreenOwn, "Own screen"}, {dlnet.ScreenMirror, "Mirror"}} {
		t := adw.NewToggle()
		t.SetName(c.name)
		t.SetLabel(c.label)
		s.screen.Add(t)
	}
	s.screenBox = gtk.NewBox(gtk.OrientationVertical, 6)
	s.screenBox.Append(heading("Screen"))
	s.screenBox.Append(s.screen)
	s.screenBox.Append(hint("Switching connects again."))

	s.scaleDown = gtk.NewButtonFromIconName("list-remove-symbolic")
	s.scaleDown.SetTooltipText("Smaller")
	s.scaleUp = gtk.NewButtonFromIconName("list-add-symbolic")
	s.scaleUp.SetTooltipText("Larger")
	s.scaleLabel = gtk.NewLabel("")
	s.scaleLabel.SetWidthChars(5)
	s.scaleRow = adw.NewActionRow()
	s.scaleRow.SetTitle("Scale")
	for _, w := range []gtk.Widgetter{s.scaleDown, s.scaleLabel, s.scaleUp} {
		gtk.BaseWidget(w).SetVAlign(gtk.AlignCenter)
		s.scaleRow.AddSuffix(w)
	}
	s.scaleDown.AddCSSClass("flat")
	s.scaleUp.AddCSSClass("flat")

	s.primary = adw.NewSwitchRow()
	s.primary.SetTitle("Primary screen")
	s.primary.SetSubtitle("Top bar and overview here until you disconnect")
	s.deskOff = adw.NewSwitchRow()
	s.deskOff.SetTitle("Desk screens off")
	s.deskOff.SetSubtitle("Every window moves here; they go back when the desk turns on")
	s.rows = gtk.NewListBox()
	s.rows.AddCSSClass("boxed-list")
	s.rows.SetSelectionMode(gtk.SelectionNone)
	s.rows.Append(s.scaleRow)
	s.rows.Append(s.primary)
	s.rows.Append(s.deskOff)

	s.keepAwake = adw.NewToggleGroup()
	s.keepAwake.SetHomogeneous(true)
	for _, c := range keepAwakeChoices {
		t := adw.NewToggle()
		t.SetName(c.name)
		t.SetLabel(c.label)
		t.SetTooltip(c.tooltip)
		s.keepAwake.Add(t)
	}
	awakeBox := gtk.NewBox(gtk.OrientationVertical, 6)
	awakeBox.Append(heading("Keep awake after disconnect"))
	awakeBox.Append(s.keepAwake)
	awakeBox.Append(hint("So a job left running goes on; ∞ lasts until a client connects again. The desk still locks."))
	s.lockAfter = adw.NewSwitchRow()
	s.lockAfter.SetTitle("Lock the desk when I disconnect")
	s.lockAfter.SetSubtitle("Also when it was unlocked as you connected")
	lockList := gtk.NewListBox()
	lockList.AddCSSClass("boxed-list")
	lockList.SetSelectionMode(gtk.SelectionNone)
	lockList.Append(s.lockAfter)
	awakeBox.Append(lockList)

	box := gtk.NewBox(gtk.OrientationVertical, 18)
	box.SetMarginTop(6)
	box.SetMarginBottom(6)
	box.SetMarginStart(6)
	box.SetMarginEnd(6)
	box.Append(s.screenBox)
	box.Append(s.rows)
	box.Append(awakeBox)
	s.popover = gtk.NewPopover()
	s.popover.SetChild(box)
	s.button = gtk.NewMenuButton()
	s.button.SetLabel("Settings")
	s.button.SetTooltipText("Screen, scale and keep awake")
	s.button.SetPopover(s.popover)

	// Connected last: adding toggles selects the first one.
	s.screen.NotifyProperty("active", func() {
		if !s.syncing {
			v.switchScreen(s.screen.ActiveName())
		}
	})
	s.scaleDown.ConnectClicked(func() { v.changeScale(-1) })
	s.scaleUp.ConnectClicked(func() { v.changeScale(1) })
	s.primary.NotifyProperty("active", func() {
		if !s.syncing {
			on := s.primary.Active()
			v.request(func(c *client.Session) error { return c.SetPrimary(on) })
		}
	})
	s.deskOff.NotifyProperty("active", func() {
		if !s.syncing {
			off := s.deskOff.Active()
			v.request(func(c *client.Session) error { return c.SetDeskOff(off) })
		}
	})
	s.keepAwake.NotifyProperty("active", func() {
		if i := s.keepAwake.Active(); !s.syncing && int(i) < len(keepAwakeChoices) {
			v.setKeepAwake(keepAwakeChoices[i].minutes)
		}
	})
	s.lockAfter.NotifyProperty("active", func() {
		if !s.syncing {
			v.setLockAfter(s.lockAfter.Active())
		}
	})
	s.reset()
	return s
}

// reset disables the menu until a session describes itself.
func (s *screenSettings) reset() {
	s.popover.Popdown()
	s.button.SetSensitive(false)
}

// show sets the controls to the session's description. scale is the scale
// the view last asked for, from which − and + step.
func (s *screenSettings) show(info dlnet.SessionInfo, scale float64) {
	s.syncing = true
	defer func() { s.syncing = false }()
	s.button.SetSensitive(true)
	c := controlsFor(info)
	s.screenBox.SetVisible(c.screen)
	if c.screen {
		s.screen.SetActiveName(info.Screen)
	}
	s.scaleRow.SetVisible(c.scale)
	s.scaleLabel.SetText(fmt.Sprintf("%.0f%%", info.Scale*100))
	s.scaleDown.SetSensitive(stepScale(scale, -1) != scale)
	s.scaleUp.SetSensitive(stepScale(scale, 1) != scale)
	s.primary.SetVisible(c.primary)
	if c.primary {
		s.primary.SetActive(*info.Primary)
	}
	s.deskOff.SetVisible(c.deskOff)
	if c.deskOff {
		s.deskOff.SetActive(*info.DeskOff)
	}
	s.rows.SetVisible(c.scale || c.primary || c.deskOff)
	s.lockAfter.SetActive(info.LockAfter)
	if i := slices.IndexFunc(keepAwakeChoices, func(k keepAwakeChoice) bool { return k.minutes == info.KeepAwake }); i >= 0 {
		s.keepAwake.SetActive(uint(i))
	} else {
		s.keepAwake.SetActive(gtk.INVALID_LIST_POSITION)
	}
}

// switchScreen saves what sessions show and connects again, since a session
// keeps the screen it started with, as the tablet does.
func (v *streamView) switchScreen(screen string) {
	if v.session == nil || screen == "" || v.session.Info().Screen == screen {
		return
	}
	if err := v.w.opts.Store.SetScreen(screen); err != nil {
		v.w.toast("Could not save the screen choice: %v", err)
		v.settings.show(v.session.Info(), v.scale)
		return
	}
	p, ok := v.w.opts.Store.Host(v.profile.ID)
	if !ok {
		p = v.profile
	}
	s := v.session
	v.gen++
	v.teardown()
	s.Close()
	v.start(p)
}

// changeScale makes the own screen's scale one step smaller (direction -1)
// or larger (+1), at once and for later sessions.
func (v *streamView) changeScale(direction int) {
	next := stepScale(v.scale, direction)
	if v.session == nil || next == v.scale {
		return
	}
	v.scale = next
	if err := v.w.opts.Store.SetScale(next); err != nil {
		v.w.toast("Could not save the scale: %v", err)
	}
	v.settings.show(v.session.Info(), next)
	v.request(func(s *client.Session) error { return s.SetScale(next) })
}

// setKeepAwake sets how long the computer stays awake after the session, for
// this session and later ones.
func (v *streamView) setKeepAwake(minutes int) {
	if v.session == nil {
		return
	}
	if err := v.w.opts.Store.SetKeepAwake(minutes); err != nil {
		v.w.toast("Could not save the keep-awake choice: %v", err)
	}
	v.request(func(s *client.Session) error { return s.SetKeepAwake(minutes) })
}

// setLockAfter sets whether the desk locks when the session ends, for this
// session and later ones.
func (v *streamView) setLockAfter(lock bool) {
	if v.session == nil {
		return
	}
	if err := v.w.opts.Store.SetLockAfter(lock); err != nil {
		v.w.toast("Could not save the lock choice: %v", err)
	}
	v.request(func(s *client.Session) error { return s.SetLockAfter(lock) })
}
