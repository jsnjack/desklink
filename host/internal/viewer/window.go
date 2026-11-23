// Package viewer is the laptop client's GTK 4 and libadwaita interface: the
// host list, pairing and the stream window.
package viewer

import (
	"context"
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
}

func newWindow(app *adw.Application, opts Options) *window {
	w := &window{opts: opts, win: adw.NewApplicationWindow(&app.Application)}
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
	return w
}

func (w *window) display() *gdk.Display { return gtk.BaseWidget(w.win).Display() }

// toast shows a short message at the bottom of the window.
func (w *window) toast(format string, args ...any) {
	w.toasts.AddToast(adw.NewToast(fmt.Sprintf(format, args...)))
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
	pairCode.ConnectClicked(func() { w.showPair(nil) })
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
	row := adw.NewActionRow()
	row.SetTitle(fmt.Sprintf("%s  %s:%d", p.Name, p.IP, p.Port))
	status := "Not paired"
	if p.Paired() {
		status = "Paired, certificate " + shortFingerprint(p.Fingerprint)
	}
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
				if err := w.opts.Store.Update(reached); err != nil {
					slog.Debug("save the host's addresses", "err", err)
				}
				row.SetTitle(fmt.Sprintf("%s  %s:%d", reached.Name, reached.IP, reached.Port))
			}
			row.SetSubtitle(fmt.Sprintf("%s · online: %s, desklinkd %s", status, h.Hostname, h.Version))
		})
	}()

	connect := gtk.NewButtonWithLabel("Connect")
	connect.AddCSSClass("suggested-action")
	connect.SetVAlign(gtk.AlignCenter)
	connect.SetSensitive(p.Paired())
	connect.ConnectClicked(func() { w.connect(p.ID) })
	pair := gtk.NewButtonWithLabel("Pair")
	pair.SetVAlign(gtk.AlignCenter)
	pair.ConnectClicked(func() { w.showPair(&p) })
	del := gtk.NewButtonWithLabel("Delete")
	del.SetVAlign(gtk.AlignCenter)
	del.AddCSSClass("flat")
	del.ConnectClicked(func() {
		if err := w.opts.Store.Delete(p.ID); err != nil {
			w.toast("Could not delete the host: %v", err)
		}
		w.refreshHosts()
	})
	row.AddSuffix(pair)
	row.AddSuffix(connect)
	row.AddSuffix(del)
	return row
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

	d := adw.NewAlertDialog("Add host", "The desktop's address on the VPN or LAN.")
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

// showPair asks for the pairing code `desklinkd pair` prints, or for a PIN
// for an existing host. A pasted code also carries the certificate
// fingerprint; a bare PIN trusts the first certificate seen.
func (w *window) showPair(target *client.Profile) {
	code := entryRow("Pairing code (JSON) or PIN", "")
	fingerprint := entryRow("Certificate fingerprint (optional with a PIN)", "")
	box := gtk.NewListBox()
	box.AddCSSClass("boxed-list")
	box.Append(code)
	box.Append(fingerprint)
	body := "Run desklinkd pair on the desktop and paste the JSON line it prints, or type its PIN."
	if target != nil {
		body = fmt.Sprintf("Pair with %s. %s", target.IP, body)
	}
	d := adw.NewAlertDialog("Pair", body)
	d.SetExtraChild(box)
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("pair", "Pair")
	d.SetDefaultResponse("pair")
	d.ConnectResponse(func(id string) {
		if id == "pair" {
			w.pair(target, code.Text(), strings.TrimSpace(fingerprint.Text()))
		}
	})
	d.Present(w.win)
}

func (w *window) pair(target *client.Profile, text, fingerprint string) {
	var p client.Profile
	pin := strings.TrimSpace(text)
	var codeAddresses []string
	if pc, err := client.ParsePairingCode(text); err == nil {
		pin, fingerprint, codeAddresses = pc.PIN, pc.Fingerprint, pc.Addresses
		found, ok := w.opts.Store.FindByAddress(pc.IP, pc.Port)
		if !ok {
			if found, err = w.opts.Store.Add(pc.IP, pc.IP, pc.Port); err != nil {
				w.toast("Could not save the host: %v", err)
				return
			}
		}
		p = found
	} else if target != nil {
		p = *target
	} else {
		w.toast("Paste the pairing code, or use Pair on a host to type a PIN")
		return
	}
	w.toast("Pairing with %s…", p.IP)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pairTimeout)
		defer cancel()
		p = p.WithAddresses(p.IP, codeAddresses)
		if h, err := client.Health(ctx, p.IP, p.Port, fingerprint); err == nil {
			if h.MAC != "" {
				p.MAC = h.MAC
			}
			p = p.WithAddresses(p.IP, h.Addresses)
		}
		res, err := client.Pair(ctx, p.IP, p.Port, pin, fingerprint, w.opts.DeviceName)
		onMain(func() {
			if err != nil {
				slog.Warn("pairing failed", "host", p.IP, "err", err)
				w.toast("Pairing failed: %v", err)
				return
			}
			p.PeerID, p.PeerKey, p.Fingerprint = res.PeerID, res.PeerKey, res.Fingerprint
			// Pairing a known machine on a new network folds its old entry in.
			if _, err := w.opts.Store.Absorb(p); err != nil {
				w.toast("Could not save the pairing: %v", err)
				return
			}
			w.toast("Paired. Certificate fingerprint starts with %s", shortFingerprint(res.Fingerprint))
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
