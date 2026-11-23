package daemon

import (
	"testing"
	"time"
)

func TestMediaLost(t *testing.T) {
	described := time.Unix(1000, 0)
	at := func(d time.Duration) time.Time { return described.Add(d) }
	cases := []struct {
		name        string
		now         time.Time
		lastReceive time.Time
		want        bool
	}{
		{"a client still starting up has time", at(mediaTimeout - time.Millisecond), time.Time{}, false},
		{"a client that never sent anything is lost", at(mediaTimeout), time.Time{}, true},
		{"recent telemetry keeps the session", at(time.Minute), at(time.Minute - 250*time.Millisecond), false},
		{"a gap shorter than the timeout keeps it", at(time.Minute), at(time.Minute - mediaTimeout + time.Second), false},
		{"silence for the timeout ends it", at(time.Minute), at(time.Minute - mediaTimeout), true},
		{"a datagram from before the description does not count", at(mediaTimeout), described.Add(-time.Second), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mediaLost(c.now, described, c.lastReceive); got != c.want {
				t.Errorf("mediaLost = %v, want %v", got, c.want)
			}
		})
	}
}
