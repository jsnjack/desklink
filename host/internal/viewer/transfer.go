package viewer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"desklink/internal/client"
)

// transferRefresh is how often the transfer card shows the bytes sent.
const transferRefresh = 200 * time.Millisecond

// transferQueue is the files waiting to be sent and how far the current
// batch is, for the status text. It holds no GTK state.
type transferQueue struct {
	pending []string
	// index is the place of the file being sent in its batch, from 1;
	// total counts the batch's files, those still pending included.
	index, total int
}

// add queues paths and reports whether the queue was idle, so sending
// must start.
func (q *transferQueue) add(paths []string) bool {
	idle := q.index == 0
	if idle {
		q.total = 0
	}
	q.pending = append(q.pending, paths...)
	q.total += len(paths)
	return idle
}

// next takes the next file to send; false when the batch is done.
func (q *transferQueue) next() (string, bool) {
	if len(q.pending) == 0 {
		q.index, q.total = 0, 0
		return "", false
	}
	path := q.pending[0]
	q.pending = q.pending[1:]
	q.index++
	return path, true
}

func (q *transferQueue) clear() { *q = transferQueue{} }

// transferStatus is the transfer card's text: which file goes, its place in
// the batch, and the share sent when its size is known.
func transferStatus(name string, index, total int, sent, size int64) string {
	text := "Sending " + name
	if total > 1 {
		text += fmt.Sprintf(" (%d of %d)", index, total)
	}
	if size > 0 {
		text += fmt.Sprintf(" · %d%%", min(100, sent*100/size))
	}
	return text
}

// fileTransfers sends the files dropped on the stream or chosen with Send
// file… to the host, one at a time (the host takes one per device at once),
// and shows the one being sent in a card at the bottom of the stream.
// Disconnecting cancels them. Its fields are used on the GTK main thread.
type fileTransfers struct {
	v     *streamView
	card  *gtk.Box
	label *gtk.Label
	bar   *gtk.ProgressBar
	queue transferQueue
	// cancel stops the file being sent; gen tells a cancelled send's late
	// result from the current one's.
	cancel context.CancelFunc
	gen    uint64
}

func newFileTransfers(v *streamView) *fileTransfers {
	t := &fileTransfers{v: v, label: gtk.NewLabel(""), bar: gtk.NewProgressBar()}
	t.label.SetEllipsize(pango.EllipsizeMiddle)
	t.label.SetMaxWidthChars(48)
	t.card = gtk.NewBox(gtk.OrientationVertical, 6)
	t.card.AddCSSClass("card")
	t.card.AddCSSClass("osd")
	t.card.SetHAlign(gtk.AlignCenter)
	t.card.SetVAlign(gtk.AlignEnd)
	t.card.SetMarginBottom(24)
	for _, w := range []gtk.Widgetter{t.label, t.bar} {
		gtk.BaseWidget(w).SetMarginStart(12)
		gtk.BaseWidget(w).SetMarginEnd(12)
	}
	t.label.SetMarginTop(8)
	t.bar.SetMarginBottom(8)
	t.card.Append(t.label)
	t.card.Append(t.bar)
	t.card.SetVisible(false)
	return t
}

// dropTarget accepts files dragged onto the stream.
func (t *fileTransfers) dropTarget() *gtk.DropTarget {
	drop := gtk.NewDropTarget(gdk.GTypeFileList, gdk.ActionCopy)
	drop.ConnectDrop(func(value *glib.Value, _, _ float64) bool {
		list, ok := value.GoValue().(*gdk.FileList)
		if !ok || t.v.session == nil {
			return false
		}
		t.send(localPaths(list.Files()))
		return true
	})
	return drop
}

// choose asks for files to send with the file chooser.
func (t *fileTransfers) choose() {
	if t.v.session == nil {
		return
	}
	dlg := gtk.NewFileDialog()
	dlg.SetTitle("Send files to " + t.v.profile.Name)
	dlg.SetAcceptLabel("Send")
	dlg.OpenMultiple(context.Background(), &t.v.w.win.Window, func(res gio.AsyncResulter) {
		list, err := dlg.OpenMultipleFinish(res)
		if err != nil {
			slog.Debug("no files chosen", "err", err)
			return
		}
		var files []*gio.File
		for i := range list.NItems() {
			if obj := list.Item(i); obj != nil {
				files = append(files, &gio.File{Object: obj})
			}
		}
		t.send(localPaths(files))
	})
}

// localPaths are the files' paths on this computer; files without one,
// such as those on a remote share GIO has not mounted, are skipped.
func localPaths(files []*gio.File) []string {
	var paths []string
	for _, f := range files {
		if p := f.Path(); p != "" {
			paths = append(paths, p)
		} else {
			slog.Warn("cannot send a file without a local path", "uri", f.URI())
		}
	}
	return paths
}

// send queues paths and starts sending if nothing is being sent.
func (t *fileTransfers) send(paths []string) {
	if len(paths) == 0 || t.v.session == nil {
		return
	}
	if t.queue.add(paths) {
		t.sendNext()
	}
}

// sendNext sends the next queued file, or hides the card when none is left.
func (t *fileTransfers) sendNext() {
	path, ok := t.queue.next()
	if !ok {
		t.card.SetVisible(false)
		return
	}
	name := filepath.Base(path)
	f, size, err := openRegular(path)
	if err != nil {
		t.v.w.toast("Cannot send %s: %v", name, err)
		t.sendNext()
		return
	}
	t.gen++
	gen, index, total := t.gen, t.queue.index, t.queue.total
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	var sent atomic.Int64
	show := func() bool {
		if gen != t.gen {
			return false
		}
		t.label.SetText(transferStatus(name, index, total, sent.Load(), size))
		if size > 0 {
			t.bar.SetFraction(float64(sent.Load()) / float64(size))
		} else {
			t.bar.Pulse()
		}
		return true
	}
	t.bar.SetFraction(0)
	show()
	t.card.SetVisible(true)
	glib.TimeoutAdd(uint(transferRefresh/time.Millisecond), show)
	profile, host := t.v.profile, t.v.profile.Name
	go func() {
		defer cancel()
		defer closeFile(f)
		saved, err := client.SendFile(ctx, profile, name, f, size, sent.Store)
		onMain(func() {
			if gen != t.gen {
				return
			}
			t.gen++ // stops show
			if err != nil {
				t.v.w.toast("Could not send %s: %v", name, err)
			} else {
				t.v.w.toast("Saved %s on %s", saved, host)
			}
			t.sendNext()
		})
	}()
}

// stop cancels the file being sent and forgets the queue.
func (t *fileTransfers) stop() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.gen++
	t.queue.clear()
	t.card.SetVisible(false)
}

// openRegular opens a regular file for sending and returns its size.
func openRegular(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	switch {
	case err != nil:
	case st.IsDir():
		err = errors.New("folders cannot be sent")
	case !st.Mode().IsRegular():
		err = errors.New("not a regular file")
	}
	if err != nil {
		closeFile(f)
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func closeFile(f *os.File) {
	if err := f.Close(); err != nil {
		slog.Debug("close a sent file", "err", err)
	}
}
