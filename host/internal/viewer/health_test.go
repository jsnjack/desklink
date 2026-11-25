package viewer

import (
	"errors"
	"strings"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

func TestHealthSummary(t *testing.T) {
	current := dlnet.Health{Hostname: "desk", Version: "0.29.0", Protocol: dlnet.ControlProtocolVersion, UDPPort: 9444}
	old := dlnet.Health{Hostname: "desk", Version: "0.28.0"}
	newer := current
	newer.Protocol++
	cases := []struct {
		name   string
		h      dlnet.Health
		udp    time.Duration
		udpErr error
		want   []string
		absent []string
	}{
		{"a current host whose UDP answers", current, 3 * time.Millisecond, nil, []string{"desklinkd 0.29.0", "stream (UDP) 3 ms"}, []string{"update"}},
		{"UDP blocked at that address", current, 0, errors.New("no answer"), []string{"blocked"}, nil},
		{"a host from before the protocol number", old, 0, nil, []string{"update desklinkd", "not testable"}, nil},
		{"a host newer than the app", newer, time.Millisecond, nil, []string{"update the app"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := healthSummary(c.h, c.udp, c.udpErr)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q lacks %q", got, w)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(got, a) {
					t.Errorf("%q says %q", got, a)
				}
			}
		})
	}
}
