package client

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

// TestProbeUDP runs a probe against the host's real media server on
// loopback: a paired device gets an answer, a wrong key gets none.
func TestProbeUDP(t *testing.T) {
	key := make([]byte, 32)
	srv, err := dlnet.ListenMedia("127.0.0.1:0", func(netip.Addr) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	srv.ProbeKey = func(id string) ([]byte, bool) { return key, id == "tablet" }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() { _ = srv.Close() })

	pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
	defer pcancel()
	if rtt, err := ProbeUDP(pctx, "127.0.0.1", srv.Port(), "tablet", key); err != nil || rtt <= 0 {
		t.Fatalf("ProbeUDP = %v, %v; want an answer", rtt, err)
	}
	qctx, qcancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer qcancel()
	if _, err := ProbeUDP(qctx, "127.0.0.1", srv.Port(), "tablet", bytes.Repeat([]byte{1}, 32)); !errors.Is(err, ErrNoUDPReply) {
		t.Errorf("a probe with the wrong key got %v, want ErrNoUDPReply", err)
	}
}
