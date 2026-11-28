package main

import (
	"bytes"
	"context"
	"errors"
	stdnet "net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"desklink/internal/crypto"
	"desklink/internal/doctor"
	dlnet "desklink/internal/net"
)

const testFingerprint = "5f2a9c410b6de2f39a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f"

func testTicket() dlnet.PairingTicket {
	return dlnet.PairingTicket{
		IP: "192.168.178.11", Port: 9443, PIN: "482913", Fingerprint: testFingerprint,
		Expires:   time.Date(2026, 11, 28, 11, 47, 30, 0, time.Local),
		Addresses: []string{"192.168.178.11", "10.8.0.2"},
	}
}

var testPairChecks = []doctor.Check{
	{Status: doctor.OK, Name: "Encoder", Detail: "VA-API AV1 and HEVC 10-bit"},
	{Status: doctor.Warning, Name: "Automatic login", Detail: "off; after a reboot someone has to log in", Fix: "desklinkd autologin on"},
}

const testPayload = `{"ip":"192.168.178.11","port":9443,"pin":"482913","fingerprint":"` + testFingerprint + `","addresses":["10.8.0.2"]}`

// fakeDaemon answers on the local socket: for pair, the pairing window
// waits once, then ends in state; for doctor, it lists peers.
type fakeDaemon struct {
	state  crypto.PairingState
	device string
	polls  atomic.Int32
	peers  []dlnet.PeerInfo
}

func (f *fakeDaemon) BeginPairing(context.Context) (dlnet.PairingTicket, error) {
	t := testTicket()
	t.Addresses = nil
	return t, nil
}

func (f *fakeDaemon) PairingStatus() dlnet.PairingStatus {
	if f.polls.Add(1) == 1 {
		return dlnet.PairingStatus{State: string(crypto.PairingWaiting)}
	}
	return dlnet.PairingStatus{State: string(f.state), Device: f.device}
}

func (f *fakeDaemon) ListPeers() []dlnet.PeerInfo { return f.peers }

func (f *fakeDaemon) RemovePeer(string) (dlnet.PeerInfo, error) {
	return dlnet.PeerInfo{}, dlnet.ErrUnknownPeer
}

// serveFakeDaemon serves d on the local socket of a fresh XDG_RUNTIME_DIR
// until the test ends, and returns the context that ends it.
func serveFakeDaemon(t *testing.T, d *fakeDaemon) context.Context {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	socket, err := dlnet.LocalSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	ready := make(chan struct{})
	go func() { served <- dlnet.ServeLocal(ctx, socket, d, func(stdnet.Addr) { close(ready) }) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})
	<-ready
	return ctx
}

// TestPairCommand runs desklinkd pair against a fake daemon on a real local
// socket. The checks it prints read this computer, so only their place is
// tested here.
func TestPairCommand(t *testing.T) {
	interval := pairPollInterval
	pairPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { pairPollInterval = interval })
	cases := []struct {
		name    string
		daemon  *fakeDaemon
		wantErr bool
		last    string
	}{
		{"a device pairs", &fakeDaemon{state: crypto.PairingDone, device: "Galaxy Tab S9"}, false, "✓ Paired with Galaxy Tab S9"},
		{"the PIN expires", &fakeDaemon{state: crypto.PairingExpired}, true, "✗ The PIN expired at 11:47 → desklinkd pair"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := serveFakeDaemon(t, c.daemon)
			var out, errOut bytes.Buffer
			rootCmd.SetArgs([]string{"pair"})
			rootCmd.SetOut(&out)
			rootCmd.SetErr(&errOut)
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				rootCmd.SetOut(nil)
				rootCmd.SetErr(nil)
				pairCmd.SilenceErrors = false
			})
			// Cobra hands a command the root's context only once.
			pairCmd.SetContext(ctx)
			err := rootCmd.ExecuteContext(ctx)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, c.wantErr)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if got := lines[len(lines)-1]; got != c.last {
				t.Errorf("last line %q, want %q", got, c.last)
			}
			if !slices.Contains(lines, strings.Replace(testPayload, `,"addresses":["10.8.0.2"]`, "", 1)) {
				t.Errorf("no pairing line:\n%s", out.String())
			}
			for _, name := range []string{"Encoder", "Input", "Wake-on-LAN", "Automatic login"} {
				if l := lineWith(lines, " "+name+" "); l == "" || !strings.ContainsAny(l[:len("✓")], "✓!✗") {
					t.Errorf("no %s check:\n%s", name, out.String())
				}
			}
			if errOut.Len() != 0 {
				t.Errorf("the failure was said twice; stderr: %q", errOut.String())
			}
		})
	}
}

func TestRenderPairing(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		colour bool
		beside bool
	}{
		{"not a terminal: the text goes below the code", 0, false, false},
		{"a narrow terminal: below the code", 80, false, false},
		{"a wide terminal: beside the code", 200, false, true},
		{"in colour the text still lines up beside the code", 200, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, err := renderPairing(testTicket(), "workstation", testPairChecks, c.width, palette{on: c.colour})
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(text, "\n")
			plain := strings.Split(stripSGR(text), "\n")
			if !c.colour && strings.Contains(text, "\x1b[") {
				t.Errorf("colour without a terminal:\n%s", text)
			}
			if plain[0] != "Pair a device with workstation" {
				t.Errorf("title %q", plain[0])
			}
			if !slices.Contains(lines, testPayload) {
				t.Errorf("the pairing line is not on a line of its own, uncoloured:\n%s", text)
			}
			for _, want := range []string{
				"PIN          482 913, valid until 11:47",
				"Fingerprint  5f2a9c41 0b6de2f3 9a8b7c6d 5e4f3a2b",
				"             1c0d9e8f 7a6b5c4d 3e2f1a0b 9c8d7e6f",
				"✓ Encoder          VA-API AV1 and HEVC 10-bit",
				"! Automatic login  off; after a reboot someone has to log in → desklinkd autologin on",
				"Waiting for the device… Ctrl+C stops.",
			} {
				if !strings.Contains(stripSGR(text), want) {
					t.Errorf("lacks %q:\n%s", want, stripSGR(text))
				}
			}
			for _, l := range plain {
				if strings.Contains(l, testPayload) && l != testPayload {
					t.Errorf("the pairing line shares a line: %q", l)
				}
				if c.width > 0 && visibleWidth(l) > c.width && strings.Contains(l, "█") {
					t.Errorf("%d columns of code and text, wider than the terminal's %d: %q", visibleWidth(l), c.width, l)
				}
			}
			scan := lineWith(plain, "Scan the code")
			beside := strings.Contains(scan, "█")
			if beside != c.beside {
				t.Errorf("text beside the code = %v, want %v: %q", beside, c.beside, scan)
			}
			if c.beside {
				col := column(scan, "Scan")
				for _, w := range []string{"PIN ", "Fingerprint "} {
					if l := lineWith(plain, w); column(l, w) != col {
						t.Errorf("%q starts at column %d, not with the text above at %d", l, column(l, w), col)
					}
				}
			}
		})
	}
}

func TestPairingResult(t *testing.T) {
	cases := []struct {
		name   string
		device string
		err    error
		want   string
	}{
		{"paired", "Galaxy Tab S9", nil, "✓ Paired with Galaxy Tab S9\n"},
		{"expired says how to start over", "", &pairingError{msg: "The PIN expired at 11:47", fix: "desklinkd pair"}, "✗ The PIN expired at 11:47 → desklinkd pair\n"},
		{"stopped", "", &pairingError{msg: "Stopped before a device paired"}, "✗ Stopped before a device paired\n"},
		{"the daemon's error", "", errors.New("reach desklinkd"), "✗ reach desklinkd\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pairingResult(palette{}, c.device, c.err); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// lineWith is the first line containing s.
func lineWith(lines []string, s string) string {
	for _, l := range lines {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

// column is the terminal column at which s starts in line.
func column(line, s string) int {
	return visibleWidth(line[:strings.Index(line, s)])
}

// stripSGR removes a palette's escape sequences.
func stripSGR(s string) string {
	for _, sgr := range []string{sgrReset, sgrBold, sgrDim, sgrRed, sgrGreen, sgrYellow} {
		s = strings.ReplaceAll(s, sgr, "")
	}
	return s
}
