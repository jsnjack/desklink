package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	notificationsBus  = "org.freedesktop.Notifications"
	notificationsPath = dbus.ObjectPath("/org/freedesktop/Notifications")
	fileManagerBus    = "org.freedesktop.FileManager1"
	fileManagerPath   = dbus.ObjectPath("/org/freedesktop/FileManager1")

	notifyAppName = "DeskLink"
	// actionDefault is the action GNOME runs when the notification itself
	// is clicked; actionShowFile is its "Show in Files" button.
	actionDefault  = "default"
	actionShowFile = "show-file"
	showFileLabel  = "Show in Files"
	// notifyExpireDefault lets the notification server choose how long a
	// notification shows.
	notifyExpireDefault = int32(-1)
	// maxShownFiles bounds the notifications whose files are remembered for
	// a click; older ones forget theirs.
	maxShownFiles = 32
	// showFileTimeout bounds opening the file manager after a click.
	showFileTimeout = 10 * time.Second
)

// Notice is one desktop notification. Summary and Body are plain text.
type Notice struct {
	Summary string
	Body    string
	// Icon is a themed icon name, or empty.
	Icon string
	// File is a file the notice is about: clicking the notification, or its
	// Show in Files button, shows it in the file manager.
	File string
}

// Notifier shows desktop notifications through org.freedesktop.Notifications
// on the session bus, and opens the file manager at a notice's file when it
// is clicked.
type Notifier struct {
	conn *dbus.Conn

	mu sync.Mutex
	// files maps a shown notification's ID to its file; order is the IDs,
	// oldest first.
	files map[uint32]string
	order []uint32
}

// NewNotifier returns a Notifier on the session bus conn.
func NewNotifier(conn *dbus.Conn) *Notifier {
	return &Notifier{conn: conn, files: map[uint32]string{}}
}

// Listen subscribes to the clicks on notifications and handles them until
// ctx ends. Notices sent before it get no file manager on a click.
func (n *Notifier) Listen(ctx context.Context) error {
	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(notificationsPath),
		dbus.WithMatchInterface(notificationsBus),
	}
	if err := n.conn.AddMatchSignalContext(ctx, match...); err != nil {
		return fmt.Errorf("subscribe to notification clicks: %w", err)
	}
	signals := make(chan *dbus.Signal, 16)
	n.conn.Signal(signals)
	go func() {
		defer n.conn.RemoveSignal(signals)
		defer func() {
			if err := n.conn.RemoveMatchSignal(match...); err != nil {
				slog.Debug("unsubscribe from notification clicks", "err", err)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-signals:
				n.handle(ctx, sig)
			}
		}
	}()
	return nil
}

// handle acts on one signal: a click on a notification with a file shows the
// file, and a closed notification forgets it. Every signal the connection
// receives arrives here, so others are ignored.
func (n *Notifier) handle(ctx context.Context, sig *dbus.Signal) {
	if sig.Path != notificationsPath || len(sig.Body) < 2 {
		return
	}
	id, ok := sig.Body[0].(uint32)
	if !ok {
		return
	}
	switch sig.Name {
	case notificationsBus + ".ActionInvoked":
		action, _ := sig.Body[1].(string)
		if action != actionDefault && action != actionShowFile {
			return
		}
		if file, ok := n.file(id); ok {
			// Starting Nautilus by D-Bus activation can take a few seconds.
			ctx, cancel := context.WithTimeout(ctx, showFileTimeout)
			defer cancel()
			if err := n.showFile(ctx, file); err != nil {
				slog.Warn("show a received file", "path", file, "err", err)
			}
		}
	case notificationsBus + ".NotificationClosed":
		n.forget(id)
	}
}

// Notify shows note and returns the notification's ID.
func (n *Notifier) Notify(ctx context.Context, note Notice) (uint32, error) {
	actions := []string{}
	if note.File != "" {
		actions = []string{actionDefault, showFileLabel, actionShowFile, showFileLabel}
	}
	hints := map[string]dbus.Variant{}
	var id uint32
	err := n.conn.Object(notificationsBus, notificationsPath).CallWithContext(ctx, notificationsBus+".Notify", 0,
		notifyAppName, uint32(0), note.Icon, note.Summary, escapeMarkup(note.Body), actions, hints, notifyExpireDefault).Store(&id)
	if err != nil {
		return 0, fmt.Errorf("show notification: %w", err)
	}
	if note.File != "" {
		n.remember(id, note.File)
	}
	return id, nil
}

// escapeMarkup keeps a plain-text body from being read as markup: GNOME
// Shell renders the body's markup, so a file name with "<b>" would turn bold
// or, unbalanced, hide the body.
func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func (n *Notifier) remember(id uint32, file string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.files[id]; !ok {
		n.order = append(n.order, id)
	}
	n.files[id] = file
	for len(n.order) > maxShownFiles {
		delete(n.files, n.order[0])
		n.order = n.order[1:]
	}
}

func (n *Notifier) file(id uint32) (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f, ok := n.files[id]
	return f, ok
}

func (n *Notifier) forget(id uint32) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.files[id]; !ok {
		return
	}
	delete(n.files, id)
	for i, o := range n.order {
		if o == id {
			n.order = append(n.order[:i], n.order[i+1:]...)
			break
		}
	}
}

// showFile opens the file manager with the file selected
// (org.freedesktop.FileManager1, which Nautilus implements), so nothing is
// executed.
func (n *Notifier) showFile(ctx context.Context, path string) error {
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	err := n.conn.Object(fileManagerBus, fileManagerPath).CallWithContext(ctx, fileManagerBus+".ShowItems", 0, []string{uri}, "").Err
	if err != nil {
		return fmt.Errorf("open the file manager: %w", err)
	}
	return nil
}
