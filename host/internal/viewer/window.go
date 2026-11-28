// Package viewer is the laptop client's GTK 4 and libadwaita interface: the
// list of computers, pairing and the stream window.
package viewer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"desklink/internal/client"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

// AppID is the application ID of the laptop client.
const AppID = "com.desklink.Client"

// website is where About sends the reader: the source and the releases.
const website = "https://github.com/jsnjack/desklink"

const (
	pairTimeout   = 30 * time.Second
	healthTimeout = 5 * time.Second
	// forgetTimeout bounds asking a host to forget this laptop; the host is
	// removed here either way.
	forgetTimeout = 8 * time.Second
	pageHosts     = "hosts"
	pageStream    = "stream"
)

// Codec choices offered in a computer's settings; empty lets the host decide (AV1
// when it can encode it).
var codecChoices = []struct{ id, label string }{{"av1", "AV1"}, {"hevc", "HEVC"}}

// Options configure Run.
type Options struct {
	Store      *client.Store
	DeviceName string
	// Version is the app's version, which About shows.
	Version string
}

// Run starts the GTK application and returns its exit status.
func Run(opts Options, args []string) int {
	video.Init()
	app := adw.NewApplication(AppID, gio.ApplicationNonUnique)
	app.ConnectActivate(func() {
		w := newWindow(app, opts)
		w.win.Present()
	})
	return app.Run(args)
}

type window struct {
	opts   Options
	win    *adw.ApplicationWindow
	toasts *adw.ToastOverlay
	stack  *gtk.Stack
	hosts  *adw.PreferencesGroup
	rows   []gtk.Widgetter
	stream *streamView
	// waking holds the IDs of the computers Wake is waking.
	waking map[string]bool

	// nearby lists unpaired hosts found with mDNS, by fingerprint.
	nearby      map[string]client.Nearby
	nearbyGroup *adw.PreferencesGroup
	nearbyRows  []gtk.Widgetter
}

// styleOnce adds the app's stylesheet to the display with the first window.
var styleOnce sync.Once

func newWindow(app *adw.Application, opts Options) *window {
	w := &window{opts: opts, win: adw.NewApplicationWindow(&app.Application), nearby: map[string]client.Nearby{}, waking: map[string]bool{}}
	styleOnce.Do(func() { loadStyle(w.display()) })
	w.win.SetTitle("DeskLink")
	w.win.SetDefaultSize(1100, 760)

	w.stack = gtk.NewStack()
	w.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	w.stack.AddNamed(w.buildHostsPage(), pageHosts)
	w.stream = newStreamView(w)
	w.stack.AddNamed(w.stream.root, pageStream)

	w.toasts = adw.NewToastOverlay()
	w.toasts.SetChild(w.stack)
	w.win.SetContent(w.toasts)
	w.refreshHosts()
	w.startDiscovery()
	return w
}

// startDiscovery browses mDNS for hosts while the window exists: a paired
// host announced at a new address gets that address once it answers there
// with its pinned certificate, and an unknown one is listed under Nearby.
func (w *window) startDiscovery() {
	ctx, cancel := context.WithCancel(context.Background())
	w.win.ConnectDestroy(cancel)
	go func() {
		err := client.Discover(ctx,
			func(n client.Nearby) { onMain(func() { w.onDiscovered(n) }) },
			func(name string) { onMain(func() { w.onLost(name) }) })
		if err != nil {
			slog.Debug("mDNS discovery", "err", err)
		}
	}()
}

func (w *window) onDiscovered(n client.Nearby) {
	for _, p := range w.opts.Store.Hosts() {
		if !p.Paired() || !strings.EqualFold(p.Fingerprint, n.Fingerprint) {
			continue
		}
		if !slices.Contains(p.Candidates(), n.IP) {
			w.confirmAddress(p, n.IP)
		}
		return
	}
	if _, ok := w.nearby[n.Fingerprint]; ok {
		return
	}
	w.nearby[n.Fingerprint] = n
	w.refreshNearby()
}

// confirmAddress saves an address a paired host was announced at once the
// host answers there with its pinned certificate: anyone on the network can
// announce any address under a known fingerprint.
func (w *window) confirmAddress(p client.Profile, ip string) {
	go func() {
		reached, err := client.Confirm(context.Background(), p, ip)
		if err != nil {
			slog.Debug("an announced address did not answer with the paired certificate", "host", p.Name, "ip", ip, "err", err)
			return
		}
		if _, err := w.opts.Store.Learn(reached); err != nil {
			slog.Debug("save a discovered address", "err", err)
		}
	}()
}

func (w *window) onLost(name string) {
	for fp, n := range w.nearby {
		if n.Name == name {
			delete(w.nearby, fp)
		}
	}
	w.refreshNearby()
}

// refreshNearby rebuilds the Nearby rows.
func (w *window) refreshNearby() {
	for _, r := range w.nearbyRows {
		w.nearbyGroup.Remove(r)
	}
	w.nearbyRows = nil
	for _, n := range w.nearby {
		row := plainRow()
		row.SetTitle(n.Host())
		row.SetSubtitle(n.IP + " · not paired")
		row.SetTooltipText(fmt.Sprintf("%s:%d", n.IP, n.Port))
		row.AddPrefix(newStatusDot())
		pair := gtk.NewButtonWithLabel("Pair…")
		pair.SetVAlign(gtk.AlignCenter)
		pair.ConnectClicked(func() { w.pairNearby(n) })
		row.AddSuffix(pair)
		w.nearbyGroup.Add(row)
		w.nearbyRows = append(w.nearbyRows, row)
	}
	w.nearbyGroup.SetVisible(len(w.nearby) > 0)
}

// pairNearby opens the pairing dialog for a discovered host. The
// announcement is unauthenticated, so nothing is saved from it: the pairing
// code supplies the address and the certificate.
func (w *window) pairNearby(n client.Nearby) { w.showPair(nil, &n) }

// plainRow is an action row that shows its title and subtitle as plain
// text. Rows parse Pango markup by default, and host names, errors and
// /health fields come from the network.
func plainRow() *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetUseMarkup(false)
	return row
}

func (w *window) display() *gdk.Display { return gtk.BaseWidget(w.win).Display() }

// toast shows a short message at the bottom of the window, as plain text:
// toasts parse Pango markup by default, and messages carry host names and
// errors from the network.
func (w *window) toast(format string, args ...any) {
	t := adw.NewToast("")
	t.SetUseMarkup(false)
	t.SetTitle(fmt.Sprintf(format, args...))
	w.toasts.AddToast(t)
}

// onMain runs fn on the GTK main thread.
func onMain(fn func()) {
	glib.IdleAdd(func() bool {
		fn()
		return false
	})
}

func (w *window) buildHostsPage() gtk.Widgetter {
	view := adw.NewToolbarView()
	header := adw.NewHeaderBar()
	add := gtk.NewButtonFromIconName("list-add-symbolic")
	add.SetTooltipText("Pair With Code…")
	add.ConnectClicked(func() { w.showPair(nil, nil) })
	header.PackStart(add)
	header.PackEnd(w.primaryMenu())
	view.AddTopBar(header)

	page := adw.NewPreferencesPage()
	w.hosts = adw.NewPreferencesGroup()
	w.hosts.SetTitle("Computers")
	page.Add(w.hosts)
	w.nearbyGroup = adw.NewPreferencesGroup()
	w.nearbyGroup.SetTitle("Nearby")
	w.nearbyGroup.SetVisible(false)
	page.Add(w.nearbyGroup)
	view.SetContent(page)
	return view
}

// primaryMenu is the main window's menu: adding a computer by its address,
// and About.
func (w *window) primaryMenu() *gtk.MenuButton {
	for name, fn := range map[string]func(){"add-address": w.showAddHost, "about": w.showAbout} {
		action := gio.NewSimpleAction(name, nil)
		action.ConnectActivate(func(*glibv2.Variant) { fn() })
		w.win.AddAction(action)
	}
	add := gio.NewMenu()
	add.Append("Add by Address…", "win.add-address")
	about := gio.NewMenu()
	about.Append("About DeskLink", "win.about")
	model := gio.NewMenu()
	model.AppendSection("", add)
	model.AppendSection("", about)
	menu := gtk.NewMenuButton()
	menu.SetIconName("open-menu-symbolic")
	menu.SetTooltipText("Main Menu")
	menu.SetPrimary(true)
	menu.SetMenuModel(model)
	return menu
}

// showAbout shows the app's name, version, website and licence.
func (w *window) showAbout() {
	d := adw.NewAboutDialog()
	d.SetApplicationName("DeskLink")
	d.SetApplicationIcon(AppID)
	d.SetVersion(w.opts.Version)
	d.SetComments("Shows your GNOME desktop in a window and sends this laptop's keyboard and mouse to it.")
	d.SetWebsite(website)
	d.SetLicenseType(gtk.LicenseMITX11)
	d.Present(w.win)
}

// screenChoices are what a host's sessions show, as dlnet.Hello.Screen,
// under the feature names (docs/design.md): the own screen is Second Desk.
var screenChoices = []struct{ id, label string }{
	{dlnet.ScreenAuto, "Auto (Second Desk while the desk is locked, otherwise Mirror)"},
	{dlnet.ScreenOwn, "Second Desk"},
	{dlnet.ScreenMirror, "Mirror"},
}

// Settings labels shared by a computer's settings and the session's.
const (
	screensOffTitle = "Screens Off"
	laptopMicTitle  = "Laptop Mic"
	laptopMicHint   = "Desk apps hear this laptop's microphone as “DeskLink microphone”"
)

// showHostSettings opens the settings of p's sessions, grouped as on the
// tablet: When I Connect (the quality preset, codec, screen and Laptop
// Mic), Second Desk (primary screen and Screens Off) and When I Leave
// (keep-awake and lock-after). Each change is saved at once; the stream
// view's settings dialog changes the same ones during a session. Under them
// are p's addresses and certificate, Pair Again and Forget.
func (w *window) showHostSettings(p client.Profile) {
	d := adw.NewPreferencesDialog()
	set := w.opts.Store.Settings(p.ID)
	save := func(what string, change func(*client.Settings)) {
		if err := w.opts.Store.ChangeSettings(p.ID, change); err != nil {
			w.toast("Could not save the %s: %v", what, err)
		}
	}
	combo := func(title string, labels []string, selected int, onChange func(int)) *adw.ComboRow {
		row := adw.NewComboRow()
		row.SetTitle(title)
		row.SetModel(gtk.NewStringList(labels))
		row.SetSelected(uint(max(selected, 0)))
		row.NotifyProperty("selected", func() { onChange(int(row.Selected())) })
		return row
	}
	switchRow := func(title, subtitle string, on bool, onChange func(bool)) *adw.SwitchRow {
		row := adw.NewSwitchRow()
		row.SetTitle(title)
		row.SetSubtitle(subtitle)
		row.SetActive(on)
		row.NotifyProperty("active", func() { onChange(row.Active()) })
		return row
	}
	labels := func(n int, label func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = label(i)
		}
		return out
	}

	whenConnect := adw.NewPreferencesGroup()
	whenConnect.SetTitle("When I Connect")
	whenConnect.Add(combo("Quality", labels(len(client.Presets), func(i int) string { return client.Presets[i].Label }),
		slices.IndexFunc(client.Presets, func(q client.Preset) bool { return q.ID == set.PresetOf().ID }),
		func(i int) { save("quality", func(s *client.Settings) { s.Preset = client.Presets[i].ID }) }))
	whenConnect.Add(combo("Codec", labels(len(codecChoices), func(i int) string { return codecChoices[i].label }),
		slices.IndexFunc(codecChoices, func(c struct{ id, label string }) bool { return c.id == codecOf(set) }),
		func(i int) { save("codec", func(s *client.Settings) { s.Codec = codecChoices[i].id }) }))
	whenConnect.Add(combo("Screen", labels(len(screenChoices), func(i int) string { return screenChoices[i].label }),
		max(slices.IndexFunc(screenChoices, func(c struct{ id, label string }) bool { return c.id == set.Screen }), 0),
		func(i int) { save("screen choice", func(s *client.Settings) { s.Screen = screenChoices[i].id }) }))
	whenConnect.Add(switchRow(laptopMicTitle, laptopMicHint, set.Mic,
		func(on bool) { save("Laptop Mic choice", func(s *client.Settings) { s.Mic = on }) }))

	own := adw.NewPreferencesGroup()
	own.SetTitle("Second Desk")
	own.SetDescription("When the computer gives this laptop a screen of its own")
	own.Add(switchRow("Primary screen", "Top bar and overview on the laptop; off keeps them at the desk", !set.PrimaryOnDesk,
		func(on bool) { save("primary screen choice", func(s *client.Settings) { s.PrimaryOnDesk = !on }) }))
	own.Add(switchRow(screensOffTitle, "Turns the desk's monitors off, so every window comes to the laptop until they turn on again", set.DeskOff,
		func(off bool) { save("Screens Off choice", func(s *client.Settings) { s.DeskOff = off }) }))

	whenLeave := adw.NewPreferencesGroup()
	whenLeave.SetTitle("When I Leave")
	whenLeave.Add(combo("Keep awake after disconnect", labels(len(keepAwakeChoices), func(i int) string { return keepAwakeChoices[i].tooltip }),
		max(slices.IndexFunc(keepAwakeChoices, func(k keepAwakeChoice) bool { return k.minutes == set.KeepAwake }), 0),
		func(i int) {
			save("keep-awake choice", func(s *client.Settings) { s.KeepAwake = keepAwakeChoices[i].minutes })
		}))
	whenLeave.Add(switchRow("Lock the desk when I disconnect", "Also when it was unlocked as you connected", set.LockAfter,
		func(on bool) { save("lock choice", func(s *client.Settings) { s.LockAfter = on }) }))

	page := adw.NewPreferencesPage()
	page.Add(whenConnect)
	page.Add(own)
	page.Add(whenLeave)
	for _, g := range w.connectionGroups(d, p) {
		page.Add(g)
	}
	d.SetTitle(p.Name)
	d.Add(page)
	d.Present(w.win)
}

// connectionGroups end p's settings: its addresses and certificate, then
// Pair Again and Forget, which close the settings dialog d first.
func (w *window) connectionGroups(d *adw.PreferencesDialog, p client.Profile) []*adw.PreferencesGroup {
	info := adw.NewPreferencesGroup()
	info.SetTitle("Connection")
	property := func(title, value string) {
		row := plainRow()
		row.AddCSSClass("property")
		row.SetTitle(title)
		row.SetSubtitle(value)
		row.SetSubtitleSelectable(true)
		info.Add(row)
	}
	property("Addresses", fmt.Sprintf("%s, port %d", strings.Join(p.Candidates(), ", "), p.Port))
	if p.Paired() {
		property("Certificate", p.Fingerprint)
	}

	actions := adw.NewPreferencesGroup()
	pair := adw.NewButtonRow()
	pair.SetTitle("Pair…")
	if p.Paired() {
		pair.SetTitle("Pair Again…")
	}
	pair.ConnectActivated(func() {
		d.Close()
		w.showPair(&p, nil)
	})
	forget := adw.NewButtonRow()
	forget.SetTitle("Forget…")
	forget.AddCSSClass("destructive-action")
	forget.ConnectActivated(func() {
		d.Close()
		w.confirmForget(p)
	})
	actions.Add(pair)
	actions.Add(forget)
	return []*adw.PreferencesGroup{info, actions}
}

// refreshHosts rebuilds the computers' rows from the store and checks each
// computer.
func (w *window) refreshHosts() {
	for _, r := range w.rows {
		w.hosts.Remove(r)
	}
	w.rows = nil
	hosts := w.opts.Store.Hosts()
	for _, p := range hosts {
		row := w.hostRow(p)
		w.hosts.Add(row)
		w.rows = append(w.rows, row)
	}
	if len(hosts) == 0 {
		w.hosts.SetDescription("None yet. On the computer, run desklinkd pair, then choose + here and paste the line below the QR code.")
	} else {
		w.hosts.SetDescription("")
	}
}

// computerRow is a computer's row in the list: a status dot, the
// computer's name and status line, its settings and one action.
type computerRow struct {
	row    *adw.ActionRow
	dot    *gtk.Box
	action *gtk.Button
	status hostStatus
}

// show gives the row the status's dot, line and action.
func (r *computerRow) show(s hostStatus) {
	r.status = s
	setDot(r.dot, s.dot)
	r.row.SetSubtitle(s.text)
	r.action.SetLabel(s.action.label())
	r.action.SetSensitive(s.action != actionWaking)
	if s.main {
		r.action.AddCSSClass("suggested-action")
	} else {
		r.action.RemoveCSSClass("suggested-action")
	}
}

// hostRow is p's row, checking p when it is paired and not being woken.
func (w *window) hostRow(p client.Profile) *adw.ActionRow {
	r := w.newComputerRow(p)
	switch {
	case !p.Paired():
		// Without a certificate to pin, no answer could be trusted.
		r.show(notPairedStatus)
	case w.waking[p.ID]:
		r.show(wakingStatus())
	default:
		w.checkHost(r, p)
	}
	return r.row
}

// newComputerRow builds p's row, whose action follows the status it shows.
func (w *window) newComputerRow(p client.Profile) *computerRow {
	r := &computerRow{row: plainRow(), dot: newStatusDot(), action: gtk.NewButton()}
	r.row.SetTitle(p.Name)
	r.row.SetTooltipText(fmt.Sprintf("%s:%d", p.IP, p.Port))
	r.row.AddPrefix(r.dot)
	settings := gtk.NewButton()
	settings.SetChild(slidersIcon())
	settings.SetVAlign(gtk.AlignCenter)
	settings.AddCSSClass("flat")
	settings.SetTooltipText("Settings for " + p.Name)
	settings.ConnectClicked(func() { w.showHostSettings(p) })
	r.action.SetVAlign(gtk.AlignCenter)
	r.action.ConnectClicked(func() {
		switch r.status.action {
		case actionConnect:
			w.connect(p.ID)
		case actionWake:
			w.wake(p, r)
		case actionPair:
			w.showPair(&p, nil)
		}
	})
	r.row.AddSuffix(settings)
	r.row.AddSuffix(r.action)
	return r
}

// checkHost asks a paired host for /health with its pinned certificate,
// shows the result in its row and saves the addresses it answered at and
// reports.
func (w *window) checkHost(r *computerRow, p client.Profile) {
	r.show(checkingStatus)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
		defer cancel()
		reached, h, err := client.Reach(ctx, p)
		// The stream needs UDP to the same address, which a firewall can drop
		// while HTTPS gets through; hosts before probes report no port.
		var udp time.Duration
		var udpErr error
		var kind string
		if err == nil {
			if h.UDPPort > 0 {
				udp, udpErr = client.ProbeUDP(ctx, reached.IP, h.UDPPort, p.PeerID, p.PeerKey)
			}
			kind = client.NetworkKind(client.NetworkKey(reached.IP))
		}
		onMain(func() {
			r.show(checkedStatus(p, kind, h, udp, udpErr, err))
			if err != nil {
				r.row.SetTooltipText(fmt.Sprintf("%s:%d · %v", p.IP, p.Port, err))
				return
			}
			r.row.SetTooltipText(fmt.Sprintf("%s:%d · desklinkd %s", reached.IP, reached.Port, h.Version))
			if reached.IP != p.IP || !slices.Equal(reached.Addresses, p.Addresses) {
				if _, err := w.opts.Store.Learn(reached); err != nil {
					slog.Debug("save the host's addresses", "err", err)
				}
			}
		})
	}()
}

// wake wakes a computer that did not answer (client.WakeAndWait) without
// connecting, then checks the computers again, so that its row offers
// Connect once it answers.
func (w *window) wake(p client.Profile, r *computerRow) {
	w.waking[p.ID] = true
	r.show(wakingStatus())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), client.WakeTimeout+healthTimeout)
		defer cancel()
		_, err := client.WakeAndWait(ctx, p, func(string) {})
		onMain(func() {
			delete(w.waking, p.ID)
			if err != nil {
				slog.Warn("wake a computer", "host", p.Name, "err", err)
				w.toast("%s", wakeFailure(p.Name, err))
			}
			w.refreshHosts()
		})
	}()
}

func shortFingerprint(fp string) string {
	if len(fp) > 16 {
		return fp[:16] + "…"
	}
	return fp
}

func entryRow(title, text string) *adw.EntryRow {
	r := adw.NewEntryRow()
	r.SetTitle(title)
	r.SetText(text)
	return r
}

// showAddHost saves a computer by its address, without pairing.
func (w *window) showAddHost() {
	name := entryRow("Name", "")
	ip := entryRow("IP address", client.DefaultIP)
	port := entryRow("Port", strconv.Itoa(client.DefaultPort))
	box := gtk.NewListBox()
	box.AddCSSClass("boxed-list")
	box.Append(name)
	box.Append(ip)
	box.Append(port)

	d := alert("Add by Address", "The computer's address on your network or VPN. Pair with it afterwards with the line desklinkd pair prints there.")
	d.SetExtraChild(box)
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("add", "Add")
	d.SetDefaultResponse("add")
	d.ConnectResponse(func(id string) {
		if id != "add" {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(port.Text()))
		if err != nil || n <= 0 || n > 65535 {
			w.toast("The port must be a number from 1 to 65535")
			return
		}
		if _, err := w.opts.Store.Add(strings.TrimSpace(name.Text()), strings.TrimSpace(ip.Text()), n); err != nil {
			w.toast("Could not save %s: %v", strings.TrimSpace(ip.Text()), err)
		}
		w.refreshHosts()
	})
	d.Present(w.win)
}

// showPair asks for the pairing code `desklinkd pair` prints, which carries
// the host's address, PIN and certificate fingerprint. There is no PIN-only
// pairing: whoever answers a pairing request can test every PIN offline, so
// the certificate must be known first. target is the saved host whose Pair…
// or Pair Again… opened the dialog, nearby the announcement whose Pair… did;
// either may be nil. Pair stays off until something is pasted, and Enter in
// the entry pairs.
func (w *window) showPair(target *client.Profile, nearby *client.Nearby) {
	code := gtk.NewEntry()
	code.AddCSSClass("monospace")
	code.SetPlaceholderText(`{"ip":"…","port":9443,"pin":"…","fingerprint":"…"}`)
	code.SetActivatesDefault(true)
	d := alert("Pair With Code", "")
	d.SetBodyUseMarkup(true)
	d.SetBody(pairPrompt(target, nearby))
	d.SetExtraChild(code)
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("pair", "Pair")
	d.SetResponseAppearance("pair", adw.ResponseSuggested)
	d.SetResponseEnabled("pair", false)
	d.SetDefaultResponse("pair")
	d.SetCloseResponse("cancel")
	code.ConnectChanged(func() { d.SetResponseEnabled("pair", strings.TrimSpace(code.Text()) != "") })
	d.ConnectResponse(func(id string) {
		if id == "pair" {
			w.pair(target, nearby, code.Text())
		}
	})
	d.Present(w.win)
}

// pairPrompt is the Pair With Code dialog's body as Pango markup: which
// computer it pairs, if a row opened it, and where the line comes from.
// Names and addresses come from the network or the user, so they are
// escaped.
func pairPrompt(target *client.Profile, nearby *client.Nearby) string {
	const where = "On the computer, run <tt>desklinkd pair</tt> and paste the line below the QR code."
	switch {
	case target != nil && target.Paired():
		return fmt.Sprintf("Pair with %s again. %s", glibv2.MarkupEscapeText(target.Name), where)
	case target != nil:
		return fmt.Sprintf("Pair with %s. %s", glibv2.MarkupEscapeText(target.Name), where)
	case nearby != nil:
		return fmt.Sprintf("Pair with %s, found at %s. %s", glibv2.MarkupEscapeText(nearby.Host()), glibv2.MarkupEscapeText(nearby.IP), where)
	default:
		return where
	}
}

// alert is a dialog whose heading and body are plain text, whatever the
// libadwaita default.
func alert(heading, body string) *adw.AlertDialog {
	d := adw.NewAlertDialog("", "")
	d.SetHeadingUseMarkup(false)
	d.SetBodyUseMarkup(false)
	d.SetHeading(heading)
	d.SetBody(body)
	return d
}

// pairingStart is the message shown when pairing starts. When the code's
// certificate is not the one the host was announced with (nearby) or saved
// with (target), it says so: the code wins.
func pairingStart(code client.PairingCode, target *client.Profile, nearby *client.Nearby) string {
	where := fmt.Sprintf("%s:%d", code.IP, code.Port)
	var other string
	switch {
	case nearby != nil && !strings.EqualFold(nearby.Fingerprint, code.Fingerprint):
		other = nearby.Host() + " announced on this network"
	case target != nil && target.Paired() && !strings.EqualFold(target.Fingerprint, code.Fingerprint):
		other = target.Name + " was paired with"
	}
	if other == "" {
		return "Pairing with " + where + "…"
	}
	return fmt.Sprintf("The code's certificate (%s) is not the one %s (%s). Pairing with %s using the code's certificate…",
		shortFingerprint(code.Fingerprint), other, shortFingerprint(fingerprintOf(target, nearby)), where)
}

func fingerprintOf(target *client.Profile, nearby *client.Nearby) string {
	if nearby != nil {
		return nearby.Fingerprint
	}
	if target != nil {
		return target.Fingerprint
	}
	return ""
}

func (w *window) pair(target *client.Profile, nearby *client.Nearby, text string) {
	code, err := client.ParsePairingCode(text)
	if err != nil {
		w.toast("%v", err)
		return
	}
	hostID := ""
	if target != nil {
		hostID = target.ID
	}
	w.toast("%s", pairingStart(code, target, nearby))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pairTimeout)
		defer cancel()
		saved, err := client.PairWithCode(ctx, w.opts.Store, code, hostID, w.opts.DeviceName)
		onMain(func() {
			if err != nil {
				slog.Warn("pairing failed", "host", code.IP, "err", err)
				w.toast("Could not pair with %s:%d: %v", code.IP, code.Port, err)
				return
			}
			if _, ok := w.nearby[saved.Fingerprint]; ok {
				delete(w.nearby, saved.Fingerprint)
				w.refreshNearby()
			}
			w.toast("Paired with %s. Certificate fingerprint starts with %s", saved.Name, shortFingerprint(saved.Fingerprint))
			w.refreshHosts()
		})
	}()
}

// confirmForget asks before removing a host. For a paired host it also asks
// the host to forget this laptop, so its pairing cannot be used again.
func (w *window) confirmForget(p client.Profile) {
	body := "Remove " + p.Name + " from this list?"
	if p.Paired() {
		body = "Remove " + p.Name + " and its pairing? " + p.Name + " also forgets this laptop, so connecting again needs a new pairing code."
	}
	d := alert("Forget "+p.Name+"?", body)
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("forget", "Forget")
	d.SetResponseAppearance("forget", adw.ResponseDestructive)
	d.SetDefaultResponse("cancel")
	d.ConnectResponse(func(id string) {
		if id == "forget" {
			w.forget(p)
		}
	})
	d.Present(w.win)
}

// forget asks the host to drop this laptop's pairing, then deletes the host
// here whether or not the host answered. Only a confirmed removal is
// reported as done; otherwise the toast gives the command to run on the host.
func (w *window) forget(p client.Profile) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), forgetTimeout)
		defer cancel()
		err := client.Forget(ctx, p)
		onMain(func() {
			if derr := w.opts.Store.Delete(p.ID); derr != nil {
				w.toast("Could not delete the host: %v", derr)
				return
			}
			switch {
			case !p.Paired():
				w.toast("Removed %s", p.Name)
			case errors.Is(err, client.ErrForgetUnconfirmed):
				// A 401 also answers a token the host could not check, so
				// the pairing may still be there.
				slog.Warn("forget this laptop on the host", "host", p.Name, "err", err)
				w.toast("Removed %s here, but %s could not confirm that it forgot this laptop. On %s, run: desklinkd peers remove %q", p.Name, p.Name, p.Name, w.opts.DeviceName)
			case err != nil:
				slog.Warn("forget this laptop on the host", "host", p.Name, "err", err)
				w.toast("Removed %s here, but it could not be told (%v). On %s, run: desklinkd peers remove %q", p.Name, err, p.Name, w.opts.DeviceName)
			default:
				w.toast("Removed %s; it no longer accepts this laptop", p.Name)
			}
			w.refreshHosts()
		})
	}()
}

func (w *window) connect(id string) {
	p, ok := w.opts.Store.Host(id)
	if !ok {
		return
	}
	w.stack.SetVisibleChildName(pageStream)
	w.stream.start(p)
}

// showHosts returns to the host list, with an optional message.
func (w *window) showHosts(message string) {
	if w.win.IsFullscreen() {
		w.win.Unfullscreen()
	}
	w.stack.SetVisibleChildName(pageHosts)
	w.refreshHosts()
	if message != "" {
		w.toast("%s", message)
	}
}
