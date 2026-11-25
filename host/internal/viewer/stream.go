package viewer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
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
	// decoderFailureLimit failures within decoderFailureWindow end the
	// session instead of rebuilding the decoder again.
	decoderFailureLimit  = 3
	decoderFailureWindow = 30 * time.Second
)

// Local shortcuts, held with Ctrl+Alt+Shift so remote apps keep every other
// combination: F toggles fullscreen, Q disconnects.
const localModifiers = gdk.ControlMask | gdk.AltMask | gdk.ShiftMask

// buttonMasks are the GDK state bits of the buttons the pointer state
// reports; side and extra buttons have none.
var buttonMasks = map[uint16]gdk.ModifierType{
	client.ButtonLeft:   gdk.Button1Mask,
	client.ButtonMiddle: gdk.Button2Mask,
	client.ButtonRight:  gdk.Button3Mask,
}

// streamView is the page that shows a session and captures input. Its
// fields are used on the GTK main thread, except the decoder under decMu.
type streamView struct {
	w       *window
	root    *adw.ToolbarView
	title   *adw.WindowTitle
	picture *gtk.Picture
	status  *gtk.Box
	label   *gtk.Label
	apps    *gtk.ListBox
	search  *gtk.SearchEntry
	// settings is the menu with the session's screen and keep-awake.
	settings *screenSettings
	// transfers sends files to the host.
	transfers *fileTransfers

	// gen identifies the current connection attempt; Disconnect and every
	// new attempt bump it, so a late Connect result or event from an
	// abandoned session is recognised and dropped.
	gen           uint64
	cancelConnect context.CancelFunc
	// live mirrors session for the decoder's goroutines.
	live atomic.Pointer[client.Session]
	// audio plays the host's sound; the network goroutine reads it.
	audio    atomic.Pointer[AudioOut]
	profile  client.Profile
	network  string
	session  *client.Session
	keys     client.KeyTracker
	buttons  client.KeyTracker
	scroll   client.ScrollAccumulator
	videoW   float64
	videoH   float64
	statsID  glib.SourceHandle
	appList  []client.App
	lastClip string
	// lastImage is the hash of the clipboard image last sent or received,
	// so focusing the window does not send it again.
	lastImage [sha256.Size]byte
	inhibit   bool
	failures  []time.Time
	// mediaUp is set once the session's first UDP datagram from the host
	// arrived; until then the status card stays up.
	mediaUp bool
	// scale is the own screen's scale the view last asked for; − and + in
	// the settings step from it.
	scale float64
	// follow decides when the own screen follows the window's size.
	follow screenFollower

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
	v.settings = newScreenSettings(v)
	header.PackEnd(v.settings.button)

	pull := gtk.NewButtonWithLabel("Get clipboard")
	pull.ConnectClicked(func() { v.request(func(s *client.Session) error { return s.RequestClipboard() }) })
	header.PackEnd(pull)
	push := gtk.NewButtonWithLabel("Send clipboard")
	push.ConnectClicked(func() { v.pushClipboard(false) })
	header.PackEnd(push)
	v.transfers = newFileTransfers(v)
	sendFile := gtk.NewButtonWithLabel("Send file…")
	sendFile.SetTooltipText("Save files in the host's Downloads; dropping files on the stream does the same")
	sendFile.ConnectClicked(v.transfers.choose)
	header.PackEnd(sendFile)
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
	overlay.AddOverlay(v.transfers.card)
	overlay.AddController(v.transfers.dropTarget())
	v.root.SetContent(overlay)

	// The header bar is hidden only in fullscreen, however fullscreen ends.
	w.win.NotifyProperty("fullscreened", func() {
		if !w.win.IsFullscreen() {
			v.root.SetRevealTopBars(true)
		}
	})
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
		if i < 0 || i >= len(v.appList) {
			return
		}
		popover.Popdown()
		app := v.appList[i]
		v.request(func(s *client.Session) error { return s.Launch(app) })
	})
	menu := gtk.NewMenuButton()
	menu.SetLabel("Apps")
	menu.SetPopover(popover)
	return menu
}

// ticket names the current session for a request that finishes later.
func (v *streamView) ticket() sessionTicket { return sessionTicket{gen: v.gen, session: v.session} }

// request runs a control request for the current session.
func (v *streamView) request(fn func(*client.Session) error) { v.requestOn(v.ticket(), fn) }

// requestOn runs a control request on the ticket's session, if it is still
// the current one, off the main thread, which a stalled link would otherwise
// freeze. Its error shows only while that session is still current.
func (v *streamView) requestOn(t sessionTicket, fn func(*client.Session) error) {
	if !t.holds(v.gen, v.session) {
		return
	}
	go func() {
		if err := fn(t.session); err != nil {
			onMain(func() {
				if t.holds(v.gen, v.session) {
					v.w.toast("%v", err)
				}
			})
		}
	}()
}

// start connects to a paired host.
func (v *streamView) start(p client.Profile) {
	v.gen++
	gen := v.gen
	v.profile = p
	v.failures = nil
	v.mediaUp = false
	v.lastClip = ""
	v.lastImage = [sha256.Size]byte{}
	v.keys.ReleaseAll()
	v.buttons.ReleaseAll()
	v.title.SetTitle(p.Name)
	v.title.SetSubtitle("")
	v.showStatus("Connecting to " + p.IP + "…")
	v.settings.reset()
	store := v.w.opts.Store
	preset := store.Preset()
	codec := store.Codec()
	if codec == "" {
		codec = codecChoices[0].id
	}
	v.scale = requestScale(store.Scale(), v.monitorScale())
	v.follow = screenFollower{}
	req := client.Request{Codec: codec, Width: preset.Width, Height: preset.Height, FPS: preset.FPS, QP: preset.QP, Auto: preset.Auto,
		Screen: store.Screen(), Scale: v.scale, KeepAwake: store.KeepAwake(), Monitor: p.Monitor,
		LockAfter: store.LockAfter(), ClipboardImages: dlnet.DefaultClipboardImageLimit}
	out, err := NewAudioOut()
	if err != nil {
		slog.Warn("no sound from the host", "err", err)
	}
	// A connect still running from an earlier start loses its output here.
	if old := v.audio.Swap(out); old != nil {
		old.Close()
	}
	if out != nil {
		req.OnAudio = func(p dlnet.AudioPacket) {
			if a := v.audio.Load(); a != nil {
				a.Push(p)
			}
		}
	}
	if fps := v.monitorFPS(); fps > 0 && fps < req.FPS {
		req.FPS = fps
	}
	ctx, cancel := context.WithTimeout(context.Background(), client.WakeTimeout+connectTimeout)
	v.cancelConnect = cancel
	go func() {
		defer cancel()
		// A host that does not answer is woken first when its MAC is known.
		reached, err := client.WakeAndWait(ctx, p, func(msg string) {
			onMain(func() {
				if gen == v.gen {
					v.showStatus(msg)
				}
			})
		})
		if err != nil {
			onMain(func() {
				v.dropAudio(out)
				if gen == v.gen {
					v.w.showHosts("Could not wake " + p.Name + ": " + err.Error())
				}
			})
			return
		}
		// Remember the address that answered and any the host reported.
		changed := reached.IP != p.IP || !slices.Equal(reached.Addresses, p.Addresses)
		p = reached
		if changed {
			if saved, err := v.w.opts.Store.Learn(reached); err != nil {
				slog.Debug("save the host's addresses", "err", err)
			} else {
				p = saved
			}
		}
		ctx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		// The stream page has been laid out by now, so the picture has the
		// size the stream will be shown at.
		sized := make(chan [2]int, 1)
		onMain(func() {
			if w, h, ok := v.ownScreenSize(); ok {
				sized <- [2]int{w, h}
				return
			}
			sized <- [2]int{preset.Width, preset.Height}
		})
		select {
		case wh := <-sized:
			req.Width, req.Height = wh[0], wh[1]
		case <-ctx.Done():
		}
		// Resolving the host can block, so it runs here, not on the main thread.
		network := client.NetworkKey(p.IP)
		if level, ok := p.Levels[network]; ok && preset.Auto {
			req.Start = &level
		}
		s, err := client.Connect(ctx, p, req, v.onFrame, &sessionEvents{v: v, gen: gen})
		onMain(func() {
			if gen != v.gen {
				if s != nil {
					s.Close() // the user left while it was connecting
				}
				return
			}
			if err != nil {
				v.dropAudio(out)
				v.w.showHosts("Could not connect: " + err.Error())
				return
			}
			v.session, v.network = s, network
			v.live.Store(s)
			v.decMu.Lock()
			haveDecoder := v.decoder != nil
			v.decMu.Unlock()
			if haveDecoder {
				// The first keyframe may have reached the decoder before the
				// session was installed here, or been dropped before it existed.
				s.RequestKeyframe()
			}
			v.picture.GrabFocus()
			v.setInhibit(v.w.win.IsActive())
			v.statsID = glib.TimeoutAdd(uint(statsPeriod/time.Millisecond), v.updateStats)
		})
	}()
}

// dropAudio closes out, the output a failed connect made, unless another
// start or teardown has already replaced and closed it.
func (v *streamView) dropAudio(out *AudioOut) {
	if out != nil && v.audio.CompareAndSwap(out, nil) {
		out.Close()
	}
}

// monitor is the monitor showing the window, or nil before it is shown.
func (v *streamView) monitor() *gdk.Monitor {
	surface := v.w.win.Surface()
	if surface == nil {
		return nil
	}
	return v.w.display().MonitorAtSurface(surface)
}

// monitorFPS is the refresh rate of the monitor showing the window.
func (v *streamView) monitorFPS() int {
	mon := v.monitor()
	if mon == nil {
		return 0
	}
	return (mon.RefreshRate() + 500) / 1000
}

// monitorScale is the scale of the monitor showing the window, fractional
// with GNOME's fractional scaling, or 0 before the window is shown.
func (v *streamView) monitorScale() float64 {
	mon := v.monitor()
	if mon == nil {
		return 0
	}
	return mon.Scale()
}

// ownScreenSize is the size in device pixels of the own screen this window
// asks for (ownScreenSizeFor), or false before the window has a size.
func (v *streamView) ownScreenSize() (w, h int, ok bool) {
	surface := v.w.win.Surface()
	mon := v.monitor()
	if surface == nil || mon == nil {
		return 0, 0, false
	}
	geo := mon.Geometry()
	return ownScreenSizeFor(v.w.win.IsFullscreen(),
		v.picture.Width(), v.picture.Height(), gdk.BaseSurface(surface).Scale(),
		geo.Width(), geo.Height(), mon.Scale())
}

// ownScreenSizeFor is the own screen's size in device pixels for a window
// whose picture is picW x picH logical pixels on a surface at surfaceScale,
// on a monitor of monW x monH logical pixels at monScale. In fullscreen it
// is the monitor's size, whatever the toolbar revealed at the top edge
// covers. In a window it is the picture's, the part of the window that
// shows the stream, so the stream fills it pixel for pixel: a screen the
// size of the monitor would be scaled down to the window, which blurs text.
// Each side is made even and kept within what the host accepts, shrinking
// both alike if one is too long; ok is false while the window has no size.
func ownScreenSizeFor(fullscreen bool, picW, picH int, surfaceScale float64, monW, monH int, monScale float64) (w, h int, ok bool) {
	lw, lh, scale := picW, picH, surfaceScale
	if fullscreen {
		lw, lh, scale = monW, monH, monScale
	}
	if lw <= 0 || lh <= 0 || scale <= 0 {
		return 0, 0, false
	}
	fw, fh := math.Round(float64(lw)*scale), math.Round(float64(lh)*scale)
	if long := max(fw, fh); long > dlnet.MaxScreenSide {
		fw, fh = math.Floor(fw*dlnet.MaxScreenSide/long), math.Floor(fh*dlnet.MaxScreenSide/long)
	}
	return max(int(fw), dlnet.MinScreenSide) &^ 1, max(int(fh), dlnet.MinScreenSide) &^ 1, true
}

// screenSize is a width and height in pixels.
type screenSize struct{ w, h int }

// screenFollower decides when the own screen follows the window: a size the
// window has kept for two looks a second apart, and that differs from the
// screen's, is asked for once, until the screen has that size or the window
// goes back to the screen's. Asking restarts the screen on the host, so a
// window still being dragged asks nothing.
type screenFollower struct{ seen, asked screenSize }

// look takes the size the window wants now and the size the own screen has,
// and reports whether to ask for want.
func (f *screenFollower) look(want, have screenSize) bool {
	steady := want == f.seen
	f.seen = want
	if want == have {
		f.asked = screenSize{}
		return false
	}
	if !steady || want == f.asked {
		return false
	}
	f.asked = want
	return true
}

// followWindow asks for the own screen at the window's size once the window
// has kept a new size (screenFollower), as the tablet does when it rotates.
func (v *streamView) followWindow(info dlnet.SessionInfo) {
	if info.Screen != dlnet.ScreenOwn || len(info.Levels) == 0 {
		return
	}
	w, h, ok := v.ownScreenSize()
	if !ok {
		return
	}
	top := info.Levels[0]
	if v.follow.look(screenSize{w, h}, screenSize{top.Width, top.Height}) {
		v.request(func(s *client.Session) error { return s.Resize(w, h) })
	}
}

func (v *streamView) showStatus(text string) {
	v.label.SetText(text)
	v.status.SetVisible(true)
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
	link := fmt.Sprintf("%d ms RTT", st.RTT.Milliseconds())
	if st.Delay > 0 {
		link += fmt.Sprintf(" · %d ms delay", st.Delay.Milliseconds())
	}
	v.title.SetSubtitle(fmt.Sprintf("%s | %d FPS | %.1f Mbps · %s · %s",
		link, st.FPS, st.Mbps, quality, info.Display))
	v.followWindow(info)
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

// sessionEvents delivers one connection attempt's events to the main thread
// and drops them once the attempt is no longer current.
type sessionEvents struct {
	v   *streamView
	gen uint64
}

func (e *sessionEvents) onCurrent(fn func(v *streamView)) {
	onMain(func() {
		if e.gen == e.v.gen {
			fn(e.v)
		}
	})
}

// SessionInfo implements client.Events.
func (e *sessionEvents) SessionInfo(info dlnet.SessionInfo) {
	e.onCurrent(func(v *streamView) {
		v.videoW, v.videoH = float64(info.Width), float64(info.Height)
		if info.Auto && info.Level >= 0 && info.Level < len(info.Levels) {
			if err := v.w.opts.Store.SetLevel(v.profile.ID, v.network, info.Levels[info.Level]); err != nil {
				slog.Debug("remember quality level", "err", err)
			}
		}
		v.decMu.Lock()
		needNew := v.decoder == nil || v.codec != info.Codec
		v.decMu.Unlock()
		if needNew {
			v.replaceDecoder(info.Codec)
		}
		v.settings.show(info, v.scale)
		// The stream is ready only when the host's first UDP datagram
		// arrives (MediaReady), not when it describes the stream.
		if !v.mediaUp {
			v.showStatus("Waiting for video from " + v.profile.Name + "…")
		}
	})
}

// MediaReady implements client.Events.
func (e *sessionEvents) MediaReady() {
	e.onCurrent(func(v *streamView) {
		v.mediaUp = true
		v.status.SetVisible(false)
	})
}

// MediaQuiet implements client.Events. The session closes by itself if the
// host stays silent, which returns to the host list.
func (e *sessionEvents) MediaQuiet(message string) {
	e.onCurrent(func(v *streamView) { v.showStatus(message) })
}

// Apps implements client.Events.
func (e *sessionEvents) Apps(apps []client.App) {
	e.onCurrent(func(v *streamView) {
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
func (e *sessionEvents) Clipboard(text string) {
	e.onCurrent(func(v *streamView) {
		v.lastClip = text
		v.w.display().Clipboard().SetText(text)
	})
}

// ClipboardImage implements client.Events: the image goes on the laptop
// clipboard as a texture, which GTK offers to other apps as PNG and more.
func (e *sessionEvents) ClipboardImage(png []byte) {
	e.onCurrent(func(v *streamView) {
		tex, err := gdk.NewTextureFromBytes(glibv2.NewBytes(png))
		if err != nil {
			slog.Warn("decode the host's clipboard image", "err", err)
			return
		}
		v.lastImage = sha256.Sum256(png)
		v.w.display().Clipboard().SetTexture(tex)
	})
}

// Notice implements client.Events. The host answers a primary or desk
// switch it refuses with an error and no new description, so the settings
// go back to the last one.
func (e *sessionEvents) Notice(message string) {
	e.onCurrent(func(v *streamView) {
		v.w.toast("%s", message)
		if v.session != nil {
			v.settings.show(v.session.Info(), v.scale)
		}
	})
}

// Closed implements client.Events.
func (e *sessionEvents) Closed(err error) {
	e.onCurrent(func(v *streamView) {
		v.gen++
		v.teardown()
		msg := ""
		if err != nil {
			msg = "Disconnected: " + err.Error()
		}
		v.w.showHosts(msg)
	})
}

// replaceDecoder builds a decoder for the codec on the main thread and asks
// for a keyframe, since frames before it were dropped. A decoder that keeps
// failing ends the session rather than being rebuilt forever.
func (v *streamView) replaceDecoder(codec string) {
	gen := v.gen
	onBehind := func() {
		if s := v.live.Load(); s != nil {
			s.RequestKeyframe()
		}
	}
	d, err := NewDecoder(codec, onBehind, func(err error) {
		slog.Warn("decoder failed", "err", err)
		onMain(func() {
			if gen != v.gen || v.session == nil {
				return
			}
			now := time.Now()
			v.failures = append(v.failures, now)
			for len(v.failures) > 0 && now.Sub(v.failures[0]) > decoderFailureWindow {
				v.failures = v.failures[1:]
			}
			if len(v.failures) >= decoderFailureLimit {
				v.disconnectWith("The decoder keeps failing: " + err.Error())
				return
			}
			v.replaceDecoder(codec)
		})
	}, func(captureMicros uint64) {
		if s := v.live.Load(); s != nil {
			s.FrameShown(captureMicros)
		}
	})
	v.decMu.Lock()
	old := v.decoder
	v.decoder, v.codec = d, codec
	v.decMu.Unlock()
	if old != nil {
		old.Close()
	}
	if err != nil {
		v.disconnectWith("Cannot decode the stream: " + err.Error())
		return
	}
	v.picture.SetPaintable(d.Paintable())
	if v.session != nil {
		v.session.RequestKeyframe()
	}
}

func (v *streamView) disconnect() { v.disconnectWith("") }

// disconnectWith ends the session or abandons a pending connect and returns
// to the host list with message.
func (v *streamView) disconnectWith(message string) {
	v.gen++
	if v.cancelConnect != nil {
		v.cancelConnect()
		v.cancelConnect = nil
	}
	s := v.session
	v.teardown()
	if s != nil {
		s.Close()
	}
	v.w.showHosts(message)
}

func (v *streamView) teardown() {
	if v.statsID != 0 {
		glib.SourceRemove(v.statsID)
		v.statsID = 0
	}
	v.session = nil
	v.live.Store(nil)
	v.settings.reset()
	v.transfers.stop()
	v.keys.ReleaseAll()
	v.buttons.ReleaseAll()
	v.setInhibit(false)
	if a := v.audio.Swap(nil); a != nil {
		a.Close()
	}
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
	if v.session != nil && len(events) > 0 {
		v.session.SendInput(events...)
	}
}

// passwordManagerHint is the clipboard target password managers such as
// KeePassXC add to a copied secret, with the value "secret", so clipboard
// tools leave it alone.
const passwordManagerHint = "x-kde-passwordManagerHint"

// secretClipboard reports whether clipboard content offered in these MIME
// types is marked as a password manager's secret. The types are known
// without reading the content, so only the hint's presence is checked.
func secretClipboard(mimeTypes []string) bool {
	return slices.Contains(mimeTypes, passwordManagerHint)
}

// pushClipboard sends the laptop clipboard to the host. With onlyIfChanged
// (the automatic push when the window gains focus) it skips text the host
// already has and a password manager's secret, which only Send clipboard
// sends. The read is asynchronous, so its text goes only to the session that
// started it, and is dropped if that session has ended or been replaced by
// the time the read finishes.
func (v *streamView) pushClipboard(onlyIfChanged bool) {
	push := clipboardPush{ticket: v.ticket(), onlyIfChanged: onlyIfChanged}
	if push.ticket.session == nil {
		return
	}
	clip := v.w.display().Clipboard()
	types := clip.Formats().MIMETypes()
	if onlyIfChanged && secretClipboard(types) {
		slog.Debug("not sending the clipboard: a password manager marked it secret")
		return
	}
	if dlnet.ClipboardPrefersImage(types) && push.ticket.session.Info().ClipboardImages > 0 {
		v.pushClipboardImage(clip, push)
		return
	}
	clip.ReadTextAsync(context.Background(), func(res gio.AsyncResulter) {
		text, err := clip.ReadTextFinish(res)
		if !push.accepts(text, err, v.gen, v.session, v.lastClip) {
			return
		}
		v.lastClip = text
		v.requestOn(push.ticket, func(s *client.Session) error {
			if err := s.PushClipboard(text); errors.Is(err, client.ErrClipboardTooLarge) && onlyIfChanged {
				return nil // too large to sync automatically; the button reports it
			} else if err != nil {
				return err
			}
			return nil
		})
	})
}

// pushClipboardImage sends the laptop clipboard's image to the host as PNG.
// With onlyIfChanged it skips an image the host put there (the clipboard is
// this window's own: GTK would re-encode it, so it would not match) and one
// it sent already, and an image over the host's limit goes unreported, as
// for text.
func (v *streamView) pushClipboardImage(clip *gdk.Clipboard, push clipboardPush) {
	if push.onlyIfChanged && clip.IsLocal() {
		return
	}
	clip.ReadTextureAsync(context.Background(), func(res gio.AsyncResulter) {
		tex, err := clip.ReadTextureFinish(res)
		if err != nil || tex == nil || !push.ticket.holds(v.gen, v.session) {
			if err != nil {
				slog.Debug("read the clipboard image", "err", err)
			}
			return
		}
		png := gdk.BaseTexture(tex).SaveToPNGBytes().Data()
		sum := sha256.Sum256(png)
		if push.onlyIfChanged && sum == v.lastImage {
			return
		}
		v.lastImage = sum
		v.requestOn(push.ticket, func(s *client.Session) error {
			if err := s.PushClipboardImage(png); errors.Is(err, client.ErrClipboardImageTooLarge) && push.onlyIfChanged {
				return nil // too large to sync automatically; the button reports it
			} else if err != nil {
				return err
			}
			return nil
		})
	})
}

// releaseButtons sends the release of every button GTK no longer reports as
// pressed, for example after GNOME took the pointer mid-drag.
func (v *streamView) releaseButtons(state gdk.ModifierType) {
	for code, mask := range buttonMasks {
		if state&mask == 0 && v.buttons.Held(code) {
			v.buttons.Release(code)
			v.send(dlnet.InputEvent{Kind: dlnet.InputMouseButton, Code: code})
		}
	}
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
		if !ok {
			return
		}
		// Always forget the key, even between sessions, or its next press
		// would look like a repeat.
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
			// Keys and buttons held while the window lost focus never get
			// their release.
			v.send(v.keys.ReleaseAll()...)
			for _, b := range v.buttons.ReleaseAll() {
				v.send(dlnet.InputEvent{Kind: dlnet.InputMouseButton, Code: b.Code})
			}
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
		v.releaseButtons(motion.CurrentEventState())
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
		var flags uint8
		if t == gdk.ButtonPress {
			v.picture.GrabFocus()
			v.buttons.Press(code)
			flags = 1
		} else {
			v.buttons.Release(code)
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
