package desktop

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// shownNotice is one Notify call the fake notification server received.
type shownNotice struct {
	app, icon, summary, body string
	actions                  []string
}

// fakeNotifications is a notification server and a file manager on a
// private bus.
type fakeNotifications struct {
	conn *dbus.Conn

	mu     sync.Mutex
	shown  []shownNotice
	nextID uint32
	// opened receives the URIs of each ShowItems call.
	opened chan []string
}

type fakeNotifyServer struct{ f *fakeNotifications }

func (s fakeNotifyServer) Notify(app string, replaces uint32, icon, summary, body string, actions []string,
	hints map[string]dbus.Variant, expire int32) (uint32, *dbus.Error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.nextID++
	s.f.shown = append(s.f.shown, shownNotice{app: app, icon: icon, summary: summary, body: body, actions: actions})
	return s.f.nextID, nil
}

type fakeFileManager struct{ f *fakeNotifications }

func (m fakeFileManager) ShowItems(uris []string, startupID string) *dbus.Error {
	m.f.opened <- uris
	return nil
}

func serveNotifications(t *testing.T, addr string) *fakeNotifications {
	t.Helper()
	f := &fakeNotifications{conn: connect(t, addr), opened: make(chan []string, 4)}
	if err := f.conn.Export(fakeNotifyServer{f}, notificationsPath, notificationsBus); err != nil {
		t.Fatal(err)
	}
	if err := f.conn.Export(fakeFileManager{f}, fileManagerPath, fileManagerBus); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{notificationsBus, fileManagerBus} {
		if reply, err := f.conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
			t.Fatalf("own %s: %v, %v", name, reply, err)
		}
	}
	return f
}

// emit sends a notification server signal.
func (f *fakeNotifications) emit(t *testing.T, member string, args ...any) {
	t.Helper()
	if err := f.conn.Emit(notificationsPath, notificationsBus+"."+member, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeNotifications) last(t *testing.T) shownNotice {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.shown) == 0 {
		t.Fatal("no notification was shown")
	}
	return f.shown[len(f.shown)-1]
}

func TestNotifyShowsTheNotice(t *testing.T) {
	cases := []struct {
		name   string
		notice Notice
		want   shownNotice
	}{
		{"a session notice", Notice{Summary: "Tab S9 connected and unlocked this computer", Icon: "computer"},
			shownNotice{app: "DeskLink", icon: "computer", summary: "Tab S9 connected and unlocked this computer", actions: []string{}}},
		{"a body is not read as markup", Notice{Summary: "Saved <b>.txt", Body: "In ~/R&D <Downloads>"},
			shownNotice{app: "DeskLink", summary: "Saved <b>.txt", body: "In ~/R&amp;D &lt;Downloads&gt;", actions: []string{}}},
		{"a file notice offers to show the file", Notice{Summary: "Saved a.txt from Tab", File: "/home/me/Downloads/a.txt"},
			shownNotice{app: "DeskLink", summary: "Saved a.txt from Tab", actions: []string{"default", "Show in Files", "show-file", "Show in Files"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr := privateBus(t)
			f := serveNotifications(t, addr)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := NewNotifier(connect(t, addr)).Notify(ctx, c.notice); err != nil {
				t.Fatal(err)
			}
			got := f.last(t)
			if got.app != c.want.app || got.icon != c.want.icon || got.summary != c.want.summary || got.body != c.want.body ||
				!slices.Equal(got.actions, c.want.actions) {
				t.Errorf("shown %+v, want %+v", got, c.want)
			}
		})
	}
}

// opened waits briefly for a ShowItems call; nil when none came.
func (f *fakeNotifications) waitOpened(within time.Duration) []string {
	select {
	case uris := <-f.opened:
		return uris
	case <-time.After(within):
		return nil
	}
}

func TestClickingAFileNoticeShowsTheFile(t *testing.T) {
	addr := privateBus(t)
	f := serveNotifications(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n := NewNotifier(connect(t, addr))
	if err := n.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	fileID, err := n.Notify(ctx, Notice{Summary: "Saved my report.pdf from Tab", File: "/home/me/Downloads/my report.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	plainID, err := n.Notify(ctx, Notice{Summary: "Tab connected"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "file:///home/me/Downloads/my%20report.pdf"
	for _, action := range []string{"default", "show-file"} {
		f.emit(t, "ActionInvoked", fileID, action)
		if got := f.waitOpened(3 * time.Second); !slices.Equal(got, []string{want}) {
			t.Errorf("action %q opened %q, want %q", action, got, want)
		}
	}
	// A click on a notice without a file, or on another app's notification,
	// opens nothing; nor does a click after the notice closed.
	f.emit(t, "ActionInvoked", plainID, "default")
	f.emit(t, "ActionInvoked", uint32(999), "default")
	f.emit(t, "NotificationClosed", fileID, uint32(2))
	f.emit(t, "ActionInvoked", fileID, "default")
	if got := f.waitOpened(300 * time.Millisecond); got != nil {
		t.Errorf("opened %q for a click that names no file", got)
	}
}

func TestNotifierForgetsTheOldestFiles(t *testing.T) {
	n := NewNotifier(nil)
	for id := uint32(1); id <= maxShownFiles+1; id++ {
		n.remember(id, "/f")
	}
	if _, ok := n.file(1); ok {
		t.Error("the oldest notice still has its file")
	}
	if _, ok := n.file(maxShownFiles + 1); !ok {
		t.Error("the newest notice lost its file")
	}
}
