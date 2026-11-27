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

// sharpScalesHint explains why 100 % and 200 % are the sharp scales, as
// the tablet's HUD does.
const sharpScalesHint = "100 % and 200 % are sharpest: at other scales GNOME stretches older (XWayland) apps"

// stepScale is the scale direction steps (-1, 0 or +1) from current, on the
// steps from minScale to maxScale. A scale between steps, such as one Mutter
// rounded, counts from the nearest step.
func stepScale(current float64, direction int) float64 {
	next := (math.Round(current/scaleStep) + float64(direction)) * scaleStep
	return min(max(next, minScale), maxScale)
}

// requestScale is the own screen's scale a session asks for: the user's
// saved choice, on the nearest step, or else the scale of the laptop monitor
// showing the window (monitor, 0 when unknown) as it is, within minScale and
// maxScale. The own screen has the window's size in device pixels, so at the
// monitor's own scale its text has the size of the laptop's (the host rounds
// to the nearest scale Mutter supports for that size). Without a scale the
// host would use its default of 200 %, which turns a 1920×1200 own screen
// into a 960×600-point desktop.
func requestScale(saved, monitor float64) float64 {
	switch {
	case saved > 0:
		return stepScale(saved, 0)
	case monitor > 0:
		return min(max(monitor, minScale), maxScale)
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
	{"sleep", "Sleep", "Suspend the computer 20 s after you leave, unless you connect again", dlnet.KeepAwakeSuspend},
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
	// monitors: the host lists more than one monitor to mirror.
	monitors bool
	// mic: hosts that offer the virtual microphone always include it.
	mic bool
}

func controlsFor(info dlnet.SessionInfo) screenControls {
	return screenControls{
		screen:   info.Screen == dlnet.ScreenOwn || info.Screen == dlnet.ScreenMirror,
		scale:    info.Screen == dlnet.ScreenOwn && info.Scale > 0,
		primary:  info.Primary != nil,
		deskOff:  info.DeskOff != nil,
		monitors: len(info.Monitors) > 1,
		mic:      info.Mic != nil,
	}
}

// monitorTooltip describes a monitor in the picker: its mode, and whether it
// is GNOME's primary monitor.
func monitorTooltip(m dlnet.MonitorInfo) string {
	t := fmt.Sprintf("%d×%d at %.0f Hz", m.Width, m.Height, m.Refresh)
	if m.Primary {
		t += ", primary"
	}
	return t
}

// screenSettings is the stream view's settings menu: the quality preset and
// codec, what the session shows, the own screen's scale, the primary and desk
// switches, keep-awake and the microphone. It shows the host's latest session
// description, which the host sends again after every change, and the
// host's saved preset. Every change is saved for the host, so the next
// session starts with it.
type screenSettings struct {
	button  *gtk.MenuButton
	popover *gtk.Popover
	// quality and codec are the host's preset and the session's codec.
	quality   *adw.ComboRow
	codec     *adw.ComboRow
	screenBox *gtk.Box
	screen    *adw.ToggleGroup
	// screenHint says that switching connects again, for hosts that take
	// no stream message.
	screenHint *gtk.Label
	// monitors picks the monitor to mirror; monitorList is what its toggles
	// show, so they are rebuilt only when the host's list changes.
	monitors    *adw.ToggleGroup
	monitorList []dlnet.MonitorInfo
	rows        *gtk.ListBox
	scaleRow    *adw.ActionRow
	scaleLabel  *gtk.Label
	scaleDown   *gtk.Button
	scaleUp     *gtk.Button
	primary     *adw.SwitchRow
	deskOff     *adw.SwitchRow
	keepAwake   *adw.ToggleGroup
	lockAfter   *adw.SwitchRow
	micList     *gtk.ListBox
	mic         *adw.SwitchRow
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
	s.monitors = adw.NewToggleGroup()
	s.monitors.SetHomogeneous(true)
	s.monitors.SetTooltipText("The monitor to mirror")
	s.screenHint = hint("Switching between own screen and mirror connects again.")
	s.screenBox = gtk.NewBox(gtk.OrientationVertical, 6)
	s.screenBox.Append(heading("Screen"))
	s.screenBox.Append(s.screen)
	s.screenBox.Append(s.monitors)
	s.screenBox.Append(s.screenHint)

	presets := make([]string, len(client.Presets))
	for i, p := range client.Presets {
		presets[i] = p.Label
	}
	s.quality = adw.NewComboRow()
	s.quality.SetTitle("Quality")
	s.quality.SetModel(gtk.NewStringList(presets))
	codecs := make([]string, len(codecChoices))
	for i, c := range codecChoices {
		codecs[i] = c.label
	}
	s.codec = adw.NewComboRow()
	s.codec.SetTitle("Codec")
	s.codec.SetModel(gtk.NewStringList(codecs))
	streamList := gtk.NewListBox()
	streamList.AddCSSClass("boxed-list")
	streamList.SetSelectionMode(gtk.SelectionNone)
	streamList.Append(s.quality)
	streamList.Append(s.codec)

	s.scaleDown = gtk.NewButtonFromIconName("list-remove-symbolic")
	s.scaleDown.SetTooltipText("Smaller")
	s.scaleUp = gtk.NewButtonFromIconName("list-add-symbolic")
	s.scaleUp.SetTooltipText("Larger")
	s.scaleLabel = gtk.NewLabel("")
	s.scaleLabel.SetWidthChars(5)
	s.scaleRow = adw.NewActionRow()
	s.scaleRow.SetTitle("Scale")
	// GNOME draws XWayland apps at 100 % and stretches them to a fractional
	// scale, which blurs their text; whole scales keep every app sharp.
	s.scaleRow.SetSubtitle(sharpScalesHint)
	for _, w := range []gtk.Widgetter{s.scaleDown, s.scaleLabel, s.scaleUp} {
		gtk.BaseWidget(w).SetVAlign(gtk.AlignCenter)
		s.scaleRow.AddSuffix(w)
	}
	s.scaleDown.AddCSSClass("flat")
	s.scaleUp.AddCSSClass("flat")

	s.primary = adw.NewSwitchRow()
	s.primary.SetTitle("Primary screen")
	s.primary.SetSubtitle("Top bar and overview here; off keeps them at the desk")
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
	awakeBox.Append(hint("So a job left running goes on; ∞ lasts until a client connects again, and Sleep suspends the computer 20 s after you leave. The desk still locks."))
	s.lockAfter = adw.NewSwitchRow()
	s.lockAfter.SetTitle("Lock the desk when I disconnect")
	s.lockAfter.SetSubtitle("Also when it was unlocked as you connected")
	lockList := gtk.NewListBox()
	lockList.AddCSSClass("boxed-list")
	lockList.SetSelectionMode(gtk.SelectionNone)
	lockList.Append(s.lockAfter)
	awakeBox.Append(lockList)

	s.mic = adw.NewSwitchRow()
	s.mic.SetTitle("Microphone")
	s.mic.SetSubtitle("Desk apps hear this computer as “DeskLink microphone”")
	s.micList = gtk.NewListBox()
	s.micList.AddCSSClass("boxed-list")
	s.micList.SetSelectionMode(gtk.SelectionNone)
	s.micList.Append(s.mic)

	box := gtk.NewBox(gtk.OrientationVertical, 18)
	box.SetMarginTop(6)
	box.SetMarginBottom(6)
	box.SetMarginStart(6)
	box.SetMarginEnd(6)
	box.Append(streamList)
	box.Append(s.screenBox)
	box.Append(s.rows)
	box.Append(awakeBox)
	box.Append(s.micList)
	s.popover = gtk.NewPopover()
	s.popover.SetChild(box)
	s.button = gtk.NewMenuButton()
	s.button.SetLabel("Settings")
	s.button.SetTooltipText("Quality, screen, scale, keep awake and microphone, saved for this host")
	s.button.SetPopover(s.popover)

	// Connected last: adding toggles selects the first one.
	s.quality.NotifyProperty("selected", func() {
		if i := s.quality.Selected(); !s.syncing && int(i) < len(client.Presets) {
			id := client.Presets[i].ID
			v.changeStream("quality", func(set *client.Settings) { set.Preset = id })
		}
	})
	s.codec.NotifyProperty("selected", func() {
		if i := s.codec.Selected(); !s.syncing && int(i) < len(codecChoices) {
			id := codecChoices[i].id
			v.changeStream("codec", func(set *client.Settings) { set.Codec = id })
		}
	})
	s.screen.NotifyProperty("active", func() {
		if !s.syncing {
			v.switchScreen(s.screen.ActiveName())
		}
	})
	s.monitors.NotifyProperty("active", func() {
		if name := s.monitors.ActiveName(); !s.syncing && name != "" {
			v.chooseMonitor(name)
		}
	})
	s.scaleDown.ConnectClicked(func() { v.changeScale(-1) })
	s.scaleUp.ConnectClicked(func() { v.changeScale(1) })
	s.primary.NotifyProperty("active", func() {
		if !s.syncing {
			v.setPrimary(s.primary.Active())
		}
	})
	s.deskOff.NotifyProperty("active", func() {
		if !s.syncing {
			v.setDeskOff(s.deskOff.Active())
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
	s.mic.NotifyProperty("active", func() {
		if !s.syncing {
			v.setMic(s.mic.Active())
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

// show sets the controls to the session's description and the host's saved
// settings, set. scale is the scale the view last asked for, from which −
// and + step.
func (s *screenSettings) show(info dlnet.SessionInfo, scale float64, set client.Settings) {
	s.syncing = true
	defer func() { s.syncing = false }()
	s.button.SetSensitive(true)
	c := controlsFor(info)
	if i := slices.IndexFunc(client.Presets, func(p client.Preset) bool { return p.ID == set.PresetOf().ID }); i >= 0 {
		s.quality.SetSelected(uint(i))
	}
	if i := slices.IndexFunc(codecChoices, func(k struct{ id, label string }) bool { return k.id == info.Codec }); i >= 0 {
		s.codec.SetSelected(uint(i))
	}
	s.screenHint.SetVisible(!info.Stream)
	s.screenBox.SetVisible(c.screen)
	if c.screen {
		s.screen.SetActiveName(info.Screen)
	}
	s.showMonitors(info.Monitors, info.Mirroring)
	s.monitors.SetVisible(c.monitors)
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
	s.micList.SetVisible(c.mic)
	if c.mic {
		s.mic.SetActive(*info.Mic)
	}
	if i := slices.IndexFunc(keepAwakeChoices, func(k keepAwakeChoice) bool { return k.minutes == info.KeepAwake }); i >= 0 {
		s.keepAwake.SetActive(uint(i))
	} else {
		s.keepAwake.SetActive(gtk.INVALID_LIST_POSITION)
	}
}

// showMonitors gives the monitor picker a toggle per monitor, rebuilt only
// when the list changed, with the mirrored one active; none is active for
// the own screen. The caller holds syncing.
func (s *screenSettings) showMonitors(list []dlnet.MonitorInfo, mirroring string) {
	if !slices.Equal(list, s.monitorList) {
		s.monitorList = slices.Clone(list)
		s.monitors.RemoveAll()
		for _, m := range list {
			t := adw.NewToggle()
			t.SetName(m.Connector)
			t.SetLabel(m.Connector)
			t.SetTooltip(monitorTooltip(m))
			s.monitors.Add(t)
		}
	}
	if slices.ContainsFunc(list, func(m dlnet.MonitorInfo) bool { return m.Connector == mirroring }) {
		s.monitors.SetActiveName(mirroring)
	} else {
		s.monitors.SetActive(gtk.INVALID_LIST_POSITION)
	}
}

// chooseMonitor mirrors the host's monitor on connector and remembers it for
// the host's later mirroring sessions. A mirroring session switches at once
// (mirror); the own screen connects again to mirror it.
func (v *streamView) chooseMonitor(connector string) {
	if v.session == nil {
		return
	}
	if err := v.w.opts.Store.SetMonitor(v.profile.ID, connector); err != nil {
		v.w.toast("Could not save the monitor choice: %v", err)
	}
	v.profile.Monitor = connector
	if v.session.Info().Screen == dlnet.ScreenMirror {
		v.request(func(s *client.Session) error { return s.Mirror(connector) })
		return
	}
	v.switchScreen(dlnet.ScreenMirror)
}

// switchScreen saves what sessions show for the host and switches the
// session to it (changeStream).
func (v *streamView) switchScreen(screen string) {
	if v.session == nil || screen == "" || v.session.Info().Screen == screen {
		return
	}
	v.changeStream("screen choice", func(set *client.Settings) { set.Screen = screen })
}

// changeStream saves a change to the host's preset, codec or screen and
// applies it to the session: at once on a host that takes stream messages,
// otherwise by connecting again, since such a session keeps the stream it
// started with.
func (v *streamView) changeStream(what string, change func(*client.Settings)) {
	if v.session == nil {
		return
	}
	store := v.w.opts.Store
	if err := store.ChangeSettings(v.profile.ID, change); err != nil {
		v.w.toast("Could not save the %s: %v", what, err)
		v.settings.show(v.session.Info(), v.scale, store.Settings(v.profile.ID))
		return
	}
	if v.session.Info().Stream {
		c := v.streamChange(store.Settings(v.profile.ID))
		v.request(func(s *client.Session) error { return s.SetStream(c) })
		return
	}
	v.reconnect()
}

// streamChange is the stream message for set: its preset, at most at the
// monitor's rate as a hello asks, its codec and screen, the monitor to mirror
// and, when the preset adapts, the level remembered for this network.
func (v *streamView) streamChange(set client.Settings) dlnet.StreamChange {
	preset := set.PresetOf()
	c := dlnet.StreamChange{
		Codec: codecOf(set), FPS: v.cappedFPS(preset.FPS), QP: preset.QP, Auto: preset.Auto, Fixed: preset.Fixed,
		Screen: set.Screen, Monitor: v.profile.Monitor,
	}
	if p, ok := v.w.opts.Store.Host(v.profile.ID); ok && preset.Auto {
		if level, ok := p.Levels[v.network]; ok {
			c.Start = &level
		}
	}
	return c
}

// reconnect ends the session and connects again with the host's saved
// settings.
func (v *streamView) reconnect() {
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

// setPrimary puts GNOME's top bar and overview on the own screen, or back on
// the desk, for this session and later ones.
func (v *streamView) setPrimary(on bool) {
	if v.session == nil {
		return
	}
	if err := v.w.opts.Store.ChangeSettings(v.profile.ID, func(set *client.Settings) { set.PrimaryOnDesk = !on }); err != nil {
		v.w.toast("Could not save the primary screen choice: %v", err)
	}
	v.request(func(c *client.Session) error { return c.SetPrimary(on) })
}

// setDeskOff turns the desk's monitors off, or on again, for this session
// and later ones.
func (v *streamView) setDeskOff(off bool) {
	if v.session == nil {
		return
	}
	if err := v.w.opts.Store.ChangeSettings(v.profile.ID, func(set *client.Settings) { set.DeskOff = off }); err != nil {
		v.w.toast("Could not save the desk screens choice: %v", err)
	}
	v.request(func(c *client.Session) error { return c.SetDeskOff(off) })
}

// changeScale makes the own screen's scale one step smaller (direction -1)
// or larger (+1), at once and for later sessions.
func (v *streamView) changeScale(direction int) {
	next := stepScale(v.scale, direction)
	if v.session == nil || next == v.scale {
		return
	}
	v.scale = next
	if err := v.w.opts.Store.ChangeSettings(v.profile.ID, func(set *client.Settings) { set.Scale = next }); err != nil {
		v.w.toast("Could not save the scale: %v", err)
	}
	v.settings.show(v.session.Info(), next, v.w.opts.Store.Settings(v.profile.ID))
	v.request(func(s *client.Session) error { return s.SetScale(next) })
}

// setKeepAwake sets how long the computer stays awake after the session, for
// this session and later ones.
func (v *streamView) setKeepAwake(minutes int) {
	if v.session == nil {
		return
	}
	if err := v.w.opts.Store.ChangeSettings(v.profile.ID, func(set *client.Settings) { set.KeepAwake = minutes }); err != nil {
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
	if err := v.w.opts.Store.ChangeSettings(v.profile.ID, func(set *client.Settings) { set.LockAfter = lock }); err != nil {
		v.w.toast("Could not save the lock choice: %v", err)
	}
	v.request(func(s *client.Session) error { return s.SetLockAfter(lock) })
}
