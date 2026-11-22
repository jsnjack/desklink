package viewer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"desklink/internal/client"
	dlnet "desklink/internal/net"
)

const (
	connectTimeout = 60 * time.Second
	statsPeriod    = time.Second
	// revealZone is how close to the top edge the pointer must come to show
	// the toolbar in fullscreen.
	revealZone = 4
)

// Local shortcuts, held with Ctrl+Alt+Shift so remote apps keep every other
// combination: F toggles fullscreen, Q disconnects.
const localModifiers = gdk.ControlMask | gdk.AltMask | gdk.ShiftMask

// streamView is the page that shows a session and captures input.
type streamView struct {
	w       *window
	root    *adw.ToolbarView
	title   *adw.WindowTitle
	picture *gtk.Picture
	status  *gtk.Box
	label   *gtk.Label
	apps    *gtk.ListBox
	search  *gtk.SearchEntry

	profile  client.Profile
	network  string
	session  *client.Session
	keys     client.KeyTracker
	scroll   client.ScrollAccumulator
	videoW   float64
	videoH   float64
	statsID  glib.SourceHandle
	appList  []client.App
	lastClip string
	inhibit  bool

	decMu   sync.Mutex
	decoder *Decoder
	codec   string
}

func newStreamView(w *window) *streamView {
	v := &streamView{w: w, root: adw.NewToolbarView()}
	header := adw.NewHeaderBar()
	v.title = adw.NewWindowTitle("DeskLink", "")
	header.SetTitleWidget(v.title)

	disconnect := gtk.NewButtonWithLabel("Disconnect")
	disconnect.AddCSSClass("destructive-action")
	disconnect.ConnectClicked(v.disconnect)
	header.PackStart(disconnect)

	fullscreen := gtk.NewButtonWithLabel("Fullscreen")
	fullscreen.SetTooltipText("Ctrl+Alt+Shift+F")
	fullscreen.ConnectClicked(v.toggleFullscreen)
	header.PackEnd(fullscreen)

	pull := gtk.NewButtonWithLabel("Get clipboard")
	pull.ConnectClicked(func() {
		if v.session != nil {
			v.report(v.session.RequestClipboard())
		}
	})
	header.PackEnd(pull)
	push := gtk.NewButtonWithLabel("Send clipboard")
	push.ConnectClicked(func() { v.pushClipboard(false) })
	header.PackEnd(push)
	header.PackEnd(v.buildAppsMenu())
	v.root.AddTopBar(header)

	v.picture = gtk.NewPicture()
	v.picture.SetContentFit(gtk.ContentFitContain)
	v.picture.SetCanShrink(true)
	v.picture.SetHExpand(true)
	v.picture.SetVExpand(true)
	v.picture.SetFocusable(true)
	// The desktop's cursor is in the video; a second, local one would only lag behind it.
	v.picture.SetCursorFromName("none")
	v.attachInput()

	v.label = gtk.NewLabel("")
	v.label.SetWrap(true)
	spinner := gtk.NewSpinner()
	spinner.Start()
	v.status = gtk.NewBox(gtk.OrientationHorizontal, 12)
	v.status.AddCSSClass("card")
	v.status.AddCSSClass("osd")
	v.status.SetHAlign(gtk.AlignCenter)
	v.status.SetVAlign(gtk.AlignStart)
	v.status.SetMarginTop(24)
	v.status.Append(spinner)
	v.status.Append(v.label)

	overlay := gtk.NewOverlay()
	overlay.SetChild(v.picture)
	overlay.AddOverlay(v.status)
	v.root.SetContent(overlay)
	return v
}

func (v *streamView) buildAppsMenu() *gtk.MenuButton {
	v.search = gtk.NewSearchEntry()
	v.apps = gtk.NewListBox()
	v.apps.SetFilterFunc(func(row *gtk.ListBoxRow) bool {
		q := strings.ToLower(v.search.Text())
		i := row.Index()
		return q == "" || i < 0 || i >= len(v.appList) || strings.Contains(strings.ToLower(v.appList[i].Name), q)
	})
	v.search.ConnectSearchChanged(v.apps.InvalidateFilter)
	scroller := gtk.NewScrolledWindow()
	scroller.SetMinContentHeight(320)
	scroller.SetChild(v.apps)
	box := gtk.NewBox(gtk.OrientationVertical, 6)
	box.Append(v.search)
	box.Append(scroller)
	popover := gtk.NewPopover()
	popover.SetChild(box)
	v.apps.ConnectRowActivated(func(row *gtk.ListBoxRow) {
		i := row.Index()
		if v.session == nil || i < 0 || i >= len(v.appList) {
			return
		}
		popover.Popdown()
		v.report(v.session.Launch(v.appList[i]))
	})
	menu := gtk.NewMenuButton()
	menu.SetLabel("Apps")
	menu.SetPopover(popover)
	return menu
}

// start connects to a paired host.
func (v *streamView) start(p client.Profile) {
	v.profile = p
	v.title.SetTitle(p.Name)
	v.title.SetSubtitle("")
	v.showStatus("Connecting to " + p.IP + "…")
	preset := v.w.opts.Store.Preset()
	codec := v.w.opts.Store.Codec()
	if codec == "" {
		codec = codecChoices[0].id
	}
	req := client.Request{Codec: codec, Width: preset.Width, Height: preset.Height, FPS: preset.FPS, QP: preset.QP, Auto: preset.Auto}
	if fps := v.monitorFPS(); fps > 0 && fps < req.FPS {
		req.FPS = fps
	}
	v.network = client.NetworkKey(p.IP)
	if level, ok := p.Levels[v.network]; ok && preset.Auto {
		req.StartLevel = &level
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		s, err := client.Connect(ctx, p, req, v.onFrame, v)
		onMain(func() {
			if err != nil {
				v.w.showHosts("Could not connect: " + err.Error())
				return
			}
			v.session = s
			v.picture.GrabFocus()
			v.statsID = glib.TimeoutAdd(uint(statsPeriod/time.Millisecond), v.updateStats)
		})
	}()
}

// monitorFPS is the refresh rate of the monitor showing the window.
func (v *streamView) monitorFPS() int {
	surface := v.w.win.Surface()
	if surface == nil {
		return 0
	}
	mon := v.w.display().MonitorAtSurface(surface)
	if mon == nil {
		return 0
	}
	return (mon.RefreshRate() + 500) / 1000
}

func (v *streamView) showStatus(text string) {
	v.label.SetText(text)
	v.status.SetVisible(true)
}

func (v *streamView) report(err error) {
	if err != nil {
		v.w.toast("%v", err)
	}
}

func (v *streamView) updateStats() bool {
	if v.session == nil {
		return false
	}
	st := v.session.Stats()
	info := v.session.Info()
	quality := fmt.Sprintf("%s %dx%d@%d QP%d", strings.ToUpper(info.Codec), info.Width, info.Height, info.FPS, info.QP)
	if info.Auto {
		quality = fmt.Sprintf("Auto %d/%d · %s", info.Level+1, len(info.Levels), quality)
	}
	v.title.SetSubtitle(fmt.Sprintf("%d ms | %d FPS | %.1f Mbps · %s · %s",
		st.RTT.Milliseconds(), st.FPS, st.Mbps, quality, info.Display))
	return true
}

func (v *streamView) onFrame(f client.EncodedFrame) {
	v.decMu.Lock()
	d := v.decoder
	v.decMu.Unlock()
	if d != nil {
		d.Push(f)
	}
}

// SessionInfo implements client.Events.
func (v *streamView) SessionInfo(info dlnet.SessionInfo) {
	onMain(func() {
		v.videoW, v.videoH = float64(info.Width), float64(info.Height)
		if info.Auto {
			if err := v.w.opts.Store.SetLevel(v.profile.ID, v.network, info.Level); err != nil {
				slog.Debug("remember quality level", "err", err)
			}
		}
		v.decMu.Lock()
		needNew := v.decoder == nil || v.codec != info.Codec
		v.decMu.Unlock()
		if needNew {
			v.replaceDecoder(info.Codec)
		}
		v.status.SetVisible(false)
	})
}

// replaceDecoder builds a decoder for the codec on the main thread and asks
// for a keyframe, since frames before it were dropped.
func (v *streamView) replaceDecoder(codec string) {
	d, err := NewDecoder(codec, func(err error) {
		slog.Warn("decoder failed", "err", err)
		onMain(func() {
			if v.session != nil {
				v.replaceDecoder(codec)
			}
		})
	})
	v.decMu.Lock()
	old := v.decoder
	v.decoder, v.codec = d, codec
	v.decMu.Unlock()
	if old != nil {
		old.Close()
	}
	if err != nil {
		v.w.showHosts("Cannot decode the stream: " + err.Error())
		v.disconnect()
		return
	}
	v.picture.SetPaintable(d.Paintable())
	if v.session != nil {
		v.session.RequestKeyframe()
	}
}

// Apps implements client.Events.
func (v *streamView) Apps(apps []client.App) {
	onMain(func() {
		v.appList = apps
		v.apps.RemoveAll()
		for i, a := range apps {
			label := gtk.NewLabel(a.Name)
			label.SetXAlign(0)
			if i == 0 {
				label.AddCSSClass("heading")
			}
			v.apps.Append(label)
		}
	})
}

// Clipboard implements client.Events.
func (v *streamView) Clipboard(text string) {
	onMain(func() {
		v.lastClip = text
		v.w.display().Clipboard().SetText(text)
	})
}

// Notice implements client.Events.
func (v *streamView) Notice(message string) {
	onMain(func() { v.w.toast("%s", message) })
}

// Closed implements client.Events.
func (v *streamView) Closed(err error) {
	onMain(func() {
		v.teardown()
		msg := ""
		if err != nil {
			msg = "Disconnected: " + err.Error()
		}
		v.w.showHosts(msg)
	})
}

func (v *streamView) disconnect() {
	if v.session != nil {
		v.session.Close()
		return
	}
	v.teardown()
	v.w.showHosts("")
}

func (v *streamView) teardown() {
	if v.statsID != 0 {
		glib.SourceRemove(v.statsID)
		v.statsID = 0
	}
	v.session = nil
	v.setInhibit(false)
	v.decMu.Lock()
	d := v.decoder
	v.decoder = nil
	v.decMu.Unlock()
	v.picture.SetPaintable(nil)
	if d != nil {
		d.Close()
	}
}

func (v *streamView) toggleFullscreen() {
	if v.w.win.IsFullscreen() {
		v.w.win.Unfullscreen()
		v.root.SetRevealTopBars(true)
		return
	}
	v.w.win.Fullscreen()
	v.root.SetRevealTopBars(false)
	v.picture.GrabFocus()
}

// setInhibit asks GNOME to pass system shortcuts (Super, Alt+Tab) to the
// window while it streams with focus. GNOME asks the user once.
func (v *streamView) setInhibit(on bool) {
	if on == v.inhibit {
		return
	}
	surface := v.w.win.Surface()
	if surface == nil {
		return
	}
	top := &gdk.Toplevel{Surface: *gdk.BaseSurface(surface)}
	if on {
		top.InhibitSystemShortcuts(nil)
	} else {
		top.RestoreSystemShortcuts()
	}
	v.inhibit = on
}

func (v *streamView) send(events ...dlnet.InputEvent) {
	if v.session != nil {
		v.session.SendInput(events...)
	}
}

// pushClipboard sends the laptop clipboard to the host. With onlyIfChanged
// it skips text the host already has.
func (v *streamView) pushClipboard(onlyIfChanged bool) {
	if v.session == nil {
		return
	}
	v.w.display().Clipboard().ReadTextAsync(context.Background(), func(res gio.AsyncResulter) {
		text, err := v.w.display().Clipboard().ReadTextFinish(res)
		if err != nil || text == "" || (onlyIfChanged && text == v.lastClip) || v.session == nil {
			return
		}
		v.lastClip = text
		v.report(v.session.PushClipboard(text))
	})
}

// attachInput forwards keyboard, pointer, buttons and scrolling to the host.
func (v *streamView) attachInput() {
	keys := gtk.NewEventControllerKey()
	keys.SetPropagationPhase(gtk.PhaseCapture)
	keys.ConnectKeyPressed(func(keyval, keycode uint, state gdk.ModifierType) bool {
		if v.session == nil {
			return false
		}
		if state&localModifiers == localModifiers {
			switch keyval {
			case gdk.KEY_F, gdk.KEY_f:
				v.toggleFullscreen()
				return true
			case gdk.KEY_Q, gdk.KEY_q:
				v.disconnect()
				return true
			}
		}
		code, ok := client.EvdevFromKeycode(keycode)
		if !ok {
			return false
		}
		if !v.keys.Press(code) {
			v.send(dlnet.InputEvent{Kind: dlnet.InputKey, Flags: 1, Code: code})
		}
		return true
	})
	keys.ConnectKeyReleased(func(keyval, keycode uint, state gdk.ModifierType) {
		code, ok := client.EvdevFromKeycode(keycode)
		if !ok || v.session == nil {
			return
		}
		v.keys.Release(code)
		v.send(dlnet.InputEvent{Kind: dlnet.InputKey, Code: code})
	})
	v.w.win.AddController(keys)

	v.w.win.NotifyProperty("is-active", func() {
		active := v.w.win.IsActive()
		if v.session == nil {
			return
		}
		if !active {
			// Keys held while the window lost focus never get their release.
			v.send(v.keys.ReleaseAll()...)
		} else {
			v.pushClipboard(true)
		}
		v.setInhibit(active)
	})

	motion := gtk.NewEventControllerMotion()
	motion.ConnectMotion(func(x, y float64) {
		if v.w.win.IsFullscreen() {
			v.root.SetRevealTopBars(y < revealZone)
		}
		if v.session == nil {
			return
		}
		r := client.ContentRect(float64(v.picture.Width()), float64(v.picture.Height()), v.videoW, v.videoH)
		nx, ny := client.Normalize(x, y, r)
		v.send(dlnet.InputEvent{Kind: dlnet.InputAbsPointer, X: nx, Y: ny})
	})
	v.picture.AddController(motion)

	buttons := gtk.NewEventControllerLegacy()
	buttons.ConnectEvent(func(ev gdk.Eventer) bool {
		base := gdk.BaseEvent(ev)
		t := base.EventType()
		if t != gdk.ButtonPress && t != gdk.ButtonRelease {
			return false
		}
		be := &gdk.ButtonEvent{Event: *base}
		code, ok := client.ButtonCode(be.Button())
		if !ok || v.session == nil {
			return false
		}
		if t == gdk.ButtonPress {
			v.picture.GrabFocus()
		}
		var flags uint8
		if t == gdk.ButtonPress {
			flags = 1
		}
		v.send(dlnet.InputEvent{Kind: dlnet.InputMouseButton, Flags: flags, Code: code})
		return true
	})
	v.picture.AddController(buttons)

	scroll := gtk.NewEventControllerScroll(gtk.EventControllerScrollBothAxes)
	scroll.ConnectScroll(func(dx, dy float64) bool {
		if v.session == nil {
			return false
		}
		h, w := v.scroll.Add(dx, dy, scroll.Unit() == gdk.ScrollUnitSurface)
		if h != 0 || w != 0 {
			v.send(dlnet.InputEvent{Kind: dlnet.InputScroll, X: h, Y: w})
		}
		return true
	})
	v.picture.AddController(scroll)
}
