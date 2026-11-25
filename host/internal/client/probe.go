package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	stdnet "net"
	"strconv"
	"time"

	dlnet "desklink/internal/net"
)

// ErrNoUDPReply means a UDP probe got no answer: the host is not reachable
// on its media port from here, as when a firewall lets HTTPS through but
// drops UDP.
var ErrNoUDPReply = errors.New("no answer on the UDP media port")

// ProbeUDP sends an authenticated probe to the host's media port at ip and
// returns the round trip, or ErrNoUDPReply when no valid answer comes
// before ctx ends. It needs no session (see host/internal/net/probe.go).
func ProbeUDP(ctx context.Context, ip string, port int, peerID string, key []byte) (time.Duration, error) {
	nonce := make([]byte, dlnet.ProbeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return 0, err
	}
	req, err := dlnet.ProbeRequest(peerID, key, nonce)
	if err != nil {
		return 0, err
	}
	conn, err := stdnet.Dial("udp4", stdnet.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return 0, fmt.Errorf("open UDP socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Second)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	// A lost datagram is likely on a bad link, so the probe is sent up to
	// three times before giving up.
	buf := make([]byte, 256)
	for attempt := range 3 {
		sent := time.Now()
		if _, err := conn.Write(req); err != nil {
			return 0, fmt.Errorf("send UDP probe: %w", err)
		}
		wait := time.Now().Add(time.Until(deadline) / time.Duration(3-attempt))
		if err := conn.SetReadDeadline(wait); err != nil {
			return 0, err
		}
		for {
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			if dlnet.CheckProbeReply(buf[:n], key, nonce) {
				return time.Since(sent), nil
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	return 0, ErrNoUDPReply
}
