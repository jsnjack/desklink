package doctor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Sockets as /proc/net lists them: 9443 is 0x24E3, 9444 0x24E4.
const (
	tcpTable = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:24E3 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 4547005 1
   1: 0B00A8C0:24E4 2A00A8C0:D431 01 00000000:00000000 00:00000000 00000000  1000        0 4547006 1
`
	udpTable = `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
 4960: 00000000:24E4 00000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 4547004 2 000000009b68a11b 0
`
	tcp6Table = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 10675 1
`
)

func TestPortInUse(t *testing.T) {
	dir := t.TempDir()
	for name, table := range map[string]string{"tcp": tcpTable, "udp": udpTable, "tcp6": tcp6Table} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(table), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := procNet
	procNet = dir
	t.Cleanup(func() { procNet = old })
	cases := []struct {
		name string
		port Port
		want bool
	}{
		{"a listening TCP socket", Port{9443, "tcp"}, true},
		{"a connected TCP socket does not listen", Port{9444, "tcp"}, false},
		{"a bound UDP socket", Port{9444, "udp"}, true},
		{"an IPv6 listener counts", Port{8080, "tcp"}, true},
		{"a free port, and no udp6 table", Port{9445, "udp"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PortInUse(c.port)
			if err != nil || got != c.want {
				t.Errorf("PortInUse(%v) = %v, %v; want %v", c.port, got, err, c.want)
			}
		})
	}
}

// fakeRun answers commands from a table of command lines; a missing line
// fails the test.
type fakeRun struct {
	t       *testing.T
	answers map[string]answer
	asked   []string
}

type answer struct {
	out    string
	status int
	err    error
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) (string, int, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.asked = append(f.asked, line)
	a, ok := f.answers[line]
	if !ok {
		f.t.Errorf("unexpected command %q", line)
	}
	return a.out, a.status, a.err
}

func TestReadFirewall(t *testing.T) {
	ports := []Port{control, media}
	running := answer{out: "running\n"}
	cases := []struct {
		name    string
		answers map[string]answer
		want    FirewallState
		wantErr bool
	}{
		{"firewalld not running", map[string]answer{"firewall-cmd --state": {out: "not running\n", status: 252}}, FirewallState{}, false},
		{"no firewall-cmd", map[string]answer{"firewall-cmd --state": {err: errNotFound()}}, FirewallState{}, false},
		{"the card's zone opens a range", map[string]answer{
			"firewall-cmd --state":                                           running,
			"firewall-cmd --get-zone-of-interface=enp7s0":                    {out: "FedoraWorkstation\n"},
			"firewall-cmd --permanent --zone=FedoraWorkstation --get-target": {out: "default\n"},
			"firewall-cmd --zone=FedoraWorkstation --query-port=9443/tcp":    {out: "yes\n"},
			"firewall-cmd --zone=FedoraWorkstation --query-port=9444/udp":    {out: "yes\n"},
		}, FirewallState{Running: true, Zone: "FedoraWorkstation", Open: ports}, false},
		{"a card without a zone uses the default, which closes a port", map[string]answer{
			"firewall-cmd --state":                                running,
			"firewall-cmd --get-zone-of-interface=enp7s0":         {out: "no zone\n", status: 2},
			"firewall-cmd --get-default-zone":                     {out: "public\n"},
			"firewall-cmd --permanent --zone=public --get-target": {out: "default\n"},
			"firewall-cmd --zone=public --query-port=9443/tcp":    {out: "yes\n"},
			"firewall-cmd --zone=public --query-port=9444/udp":    {out: "no\n", status: 1},
		}, FirewallState{Running: true, Zone: "public", Open: []Port{control}, Closed: []Port{media}}, false},
		{"a trusted zone opens everything", map[string]answer{
			"firewall-cmd --state":                                 running,
			"firewall-cmd --get-zone-of-interface=enp7s0":          {out: "trusted\n"},
			"firewall-cmd --permanent --zone=trusted --get-target": {out: "ACCEPT\n"},
		}, FirewallState{Running: true, Zone: "trusted", Open: ports}, false},
		{"a query that fails is not taken as closed", map[string]answer{
			"firewall-cmd --state":                                running,
			"firewall-cmd --get-zone-of-interface=enp7s0":         {out: "public\n"},
			"firewall-cmd --permanent --zone=public --get-target": {out: "default\n"},
			"firewall-cmd --zone=public --query-port=9443/tcp":    {status: 13},
		}, FirewallState{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRun{t: t, answers: c.answers}
			got, err := ReadFirewall(context.Background(), f.run, "enp7s0", ports)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, c.wantErr)
			}
			if got.Running != c.want.Running || got.Zone != c.want.Zone || !slices.Equal(got.Open, c.want.Open) || !slices.Equal(got.Closed, c.want.Closed) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
			for _, line := range f.asked {
				if strings.Contains(line, "--add") || strings.Contains(line, "--remove") || strings.Contains(line, "--reload") {
					t.Errorf("doctor changed the firewall: %q", line)
				}
			}
		})
	}
}

func TestReadService(t *testing.T) {
	const query = "systemctl --user show desklinkd.service --timestamp=unix --property=LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestamp"
	cases := []struct {
		name    string
		answer  answer
		want    ServiceState
		wantErr bool
	}{
		{"active", answer{out: "LoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nActiveEnterTimestamp=@1795875762\n"},
			ServiceState{Load: "loaded", Active: "active", Sub: "running", UnitFile: "enabled", Since: time.Unix(1795875762, 0)}, false},
		{"not installed", answer{out: "LoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\nActiveEnterTimestamp=\n"},
			ServiceState{Load: "not-found", Active: "inactive", Sub: "dead"}, false},
		{"no user manager", answer{status: 1}, ServiceState{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRun{t: t, answers: map[string]answer{query: c.answer}}
			got, err := ReadService(context.Background(), f.run)
			if (err != nil) != c.wantErr || got != c.want {
				t.Errorf("got %+v, %v; want %+v (error: %v)", got, err, c.want, c.wantErr)
			}
		})
	}
}

// errNotFound is what exec gives for a command that is not installed.
func errNotFound() error {
	_, _, err := Exec(context.Background(), "desklink-test-no-such-command")
	return err
}
