// Package viewer is the laptop client's GTK 4 and libadwaita interface: the
// host list, pairing and the stream window.
package viewer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"desklink/internal/client"
	"desklink/internal/video"
)

// AppID is the application ID of the laptop client.
const AppID = "com.desklink.Client"

const (
	pairTimeout   = 30 * time.Second
	healthTimeout = 5 * time.Second
	// forgetTimeout bounds asking a host to forget this laptop; the host is
	// removed here either way.
	forgetTimeout = 8 * time.Second
	pageHosts     = "hosts"
	pageStream    = "stream"
)

// Codec choices offered in the host list; empty lets the host decide (AV1
// when it can encode it).
var codecChoices = []struct{ id, label string }{{"av1", "AV1"}, {"hevc", "HEVC"}}

// Options configure Run.
type Options struct {
	Store      *client.Store
	DeviceName string
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

	// nearby lists unpaired hosts found with mDNS, by fingerprint.
	nearby      map[string]client.Nearby
	nearbyGroup *adw.PreferencesGroup
	nearbyRows  []gtk.Widgetter
}

func newWindow(app *adw.Application, opts Options) *window {
	w := &window{opts: opts, win: adw.NewApplicationWindow(&app.Application), nearby: map[string]client.Nearby{}}
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
// with its pinned certificate, and an unknown one is listed under "Found on
// this network".
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

// refreshNearby rebuilds the "Found on this network" rows.
func (w *window) refreshNearby() {
	for _, r := range w.nearbyRows {
		w.nearbyGroup.Remove(r)
	}
	w.nearbyRows = nil
	for _, n := range w.nearby {
		row := plainRow()
		row.SetTitle(n.Host())
		row.SetSubtitle(fmt.Sprintf("%s:%d · not paired", n.IP, n.Port))
		pair := gtk.NewButtonWithLabel("Pair")
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
	add := gtk.NewButtonWithLabel("Add host")
	add.ConnectClicked(w.showAddHost)
	header.PackStart(add)
	pairCode := gtk.NewButtonWithLabel("Pair with code")
	pairCode.ConnectClicked(func() { w.showPair(nil, nil) })
	header.PackEnd(pairCode)
	view.AddTopBar(header)

	page := adw.NewPreferencesPage()
	quality := adw.NewPreferencesGroup()
	quality.SetTitle("Stream")
	quality.Add(w.presetRow())
	quality.Add(w.codecRow())
	page.Add(quality)

	w.hosts = adw.NewPreferencesGroup()
	w.hosts.SetTitle("Hosts")
	page.Add(w.hosts)
	w.nearbyGroup = adw.NewPreferencesGroup()
	w.nearbyGroup.SetTitle("Found on this network")
	w.nearbyGroup.SetVisible(false)
	page.Add(w.nearbyGroup)
	view.SetContent(page)
	return view
}

func (w *window) presetRow() *adw.ComboRow {
	labels := make([]string, len(client.Presets))
	current := w.opts.Store.Preset()
	var selected uint
	for i, p := range client.Presets {
		labels[i] = p.Label
		if p.ID == current.ID {
			selected = uint(i)
		}
	}
	row := adw.NewComboRow()
	row.SetTitle("Quality")
	row.SetModel(gtk.NewStringList(labels))
	row.SetSelected(selected)
	row.NotifyProperty("selected", func() {
		if err := w.opts.Store.SetPreset(client.Presets[row.Selected()].ID); err != nil {
			w.toast("Could not save the preset: %v", err)
		}
	})
	return row
}

func (w *window) codecRow() *adw.ComboRow {
	labels := make([]string, len(codecChoices))
	var selected uint
	for i, c := range codecChoices {
		labels[i] = c.label
		if c.id == w.opts.Store.Codec() {
			selected = uint(i)
		}
	}
	row := adw.NewComboRow()
	row.SetTitle("Codec")
	row.SetModel(gtk.NewStringList(labels))
	row.SetSelected(selected)
	row.NotifyProperty("selected", func() {
		if err := w.opts.Store.SetCodec(codecChoices[row.Selected()].id); err != nil {
			w.toast("Could not save the codec: %v", err)
		}
	})
	return row
}

// refreshHosts rebuilds the host rows from the store and checks each host.
func (w *window) refreshHosts() {
	for _, r := range w.rows {
		w.hosts.Remove(r)
	}
	w.rows = nil
	for _, p := range w.opts.Store.Hosts() {
		row := w.hostRow(p)
		w.hosts.Add(row)
		w.rows = append(w.rows, row)
	}
}

func (w *window) hostRow(p client.Profile) *adw.ActionRow {
	row := plainRow()
	row.SetTitle(fmt.Sprintf("%s  %s:%d", p.Name, p.IP, p.Port))
	if p.Paired() {
		w.checkHost(row, p)
	} else {
		// Without a certificate to pin, no answer could be trusted.
		row.SetSubtitle("Not paired · choose Pair and paste the code desklinkd pair prints")
	}

	connect := gtk.NewButtonWithLabel("Connect")
	connect.AddCSSClass("suggested-action")
	connect.SetVAlign(gtk.AlignCenter)
	connect.SetSensitive(p.Paired())
	connect.ConnectClicked(func() { w.connect(p.ID) })
	pair := gtk.NewButtonWithLabel("Pair")
	pair.SetVAlign(gtk.AlignCenter)
	pair.ConnectClicked(func() { w.showPair(&p, nil) })
	del := gtk.NewButtonWithLabel("Forget")
	del.SetVAlign(gtk.AlignCenter)
	del.AddCSSClass("flat")
	del.ConnectClicked(func() { w.confirmForget(p) })
	row.AddSuffix(pair)
	row.AddSuffix(connect)
	row.AddSuffix(del)
	return row
}

// checkHost asks a paired host for /health with its pinned certificate,
// shows the result in row and saves the addresses it answered at and reports.
func (w *window) checkHost(row *adw.ActionRow, p client.Profile) {
	status := "Paired, certificate " + shortFingerprint(p.Fingerprint)
	row.SetSubtitle(status + " · checking…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
		defer cancel()
		reached, h, err := client.Reach(ctx, p)
		onMain(func() {
			if err != nil {
				row.SetSubtitle(status + " · offline: " + err.Error())
				return
			}
			if reached.IP != p.IP || !slices.Equal(reached.Addresses, p.Addresses) {
				if saved, err := w.opts.Store.Learn(reached); err != nil {
					slog.Debug("save the host's addresses", "err", err)
				} else {
					row.SetTitle(fmt.Sprintf("%s  %s:%d", saved.Name, saved.IP, saved.Port))
				}
			}
			row.SetSubtitle(fmt.Sprintf("%s · online: %s, desklinkd %s", status, h.Hostname, h.Version))
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

func (w *window) showAddHost() {
	name := entryRow("Name", "")
	ip := entryRow("IP address", client.DefaultIP)
	port := entryRow("Port", strconv.Itoa(client.DefaultPort))
	box := gtk.NewListBox()
	box.AddCSSClass("boxed-list")
	box.Append(name)
	box.Append(ip)
	box.Append(port)

	d := alert("Add host", "The desktop's address on the VPN or LAN.")
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
			w.toast("Could not save the host: %v", err)
		}
		w.refreshHosts()
	})
	d.Present(w.win)
}

// showPair asks for the pairing code `desklinkd pair` prints, which carries
// the host's address, PIN and certificate fingerprint. There is no PIN-only
// pairing: whoever answers a pairing request can test every PIN offline, so
// the certificate must be known first. target is the saved host whose Pair
// button opened the dialog, nearby the announcement whose Pair button did;
// either may be nil.
func (w *window) showPair(target *client.Profile, nearby *client.Nearby) {
	code := entryRow("Pairing code", "")
	box := gtk.NewListBox()
	box.AddCSSClass("boxed-list")
	box.Append(code)
	body := "Run desklinkd pair on the desktop and paste the line of JSON it prints."
	switch {
	case target != nil:
		body = fmt.Sprintf("Pair with %s again. %s", target.Name, body)
	case nearby != nil:
		body = fmt.Sprintf("Pair with %s, found at %s. %s", nearby.Host(), nearby.IP, body)
	}
	d := alert("Pair", body)
	d.SetExtraChild(box)
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("pair", "Pair")
	d.SetDefaultResponse("pair")
	d.ConnectResponse(func(id string) {
		if id == "pair" {
			w.pair(target, nearby, code.Text())
		}
	})
	d.Present(w.win)
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
				w.toast("Pairing failed: %v", err)
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
