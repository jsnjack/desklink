package viewer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTransferQueueCountsEachBatch(t *testing.T) {
	var q transferQueue
	if !q.add([]string{"a", "b"}) {
		t.Fatal("an idle queue did not ask to start sending")
	}
	if p, ok := q.next(); !ok || p != "a" || q.index != 1 || q.total != 2 {
		t.Fatalf("next = %q %v, file %d of %d; want a, 1 of 2", p, ok, q.index, q.total)
	}
	// Files dropped while one is sent join the batch.
	if q.add([]string{"c"}) {
		t.Error("a busy queue asked to start sending again")
	}
	for _, want := range []string{"b", "c"} {
		if p, ok := q.next(); !ok || p != want || q.total != 3 {
			t.Fatalf("next = %q %v of %d, want %q of 3", p, ok, q.total, want)
		}
	}
	if _, ok := q.next(); ok {
		t.Fatal("a finished batch has more files")
	}
	// The next drop starts a new batch.
	if !q.add([]string{"d"}) {
		t.Fatal("a finished queue did not ask to start sending")
	}
	if _, ok := q.next(); !ok || q.index != 1 || q.total != 1 {
		t.Errorf("new batch at file %d of %d, want 1 of 1", q.index, q.total)
	}
}

func TestTransferStatus(t *testing.T) {
	cases := []struct {
		name         string
		index, total int
		sent, size   int64
		want         string
	}{
		{"one file", 1, 1, 50, 200, "Sending a.pdf · 25%"},
		{"one of several", 2, 3, 0, 10, "Sending a.pdf (2 of 3) · 0%"},
		{"an empty file", 1, 1, 0, 0, "Sending a.pdf"},
		{"a file that grew while it was sent", 1, 1, 300, 200, "Sending a.pdf · 100%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := transferStatus("a.pdf", c.index, c.total, c.sent, c.size); got != c.want {
				t.Errorf("transferStatus = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOpenRegularRefusesFolders(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, size, err := openRegular(file)
	if err != nil || size != 5 {
		t.Fatalf("openRegular(file) = %d bytes, %v; want 5", size, err)
	}
	closeFile(f)
	if _, _, err := openRegular(dir); err == nil {
		t.Error("a folder was opened for sending")
	}
}
