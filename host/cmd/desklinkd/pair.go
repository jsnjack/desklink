package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"desklink/internal/crypto"
	"desklink/internal/desktop"
	"desklink/internal/doctor"
	"desklink/internal/input"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

// pairPollInterval is how often pair asks the daemon whether a device has
// paired; tests shorten it.
var pairPollInterval = time.Second

// pairPayload is the pairing code: the JSON in the QR code that the tablet
// scans, also printed for pasting into the laptop client.
type pairPayload struct {
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	PIN         string `json:"pin"`
	Fingerprint string `json:"fingerprint"`
	// Addresses are the host's other addresses (other networks, VPNs).
	Addresses []string `json:"addresses,omitempty"`
}

var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Pair a tablet or laptop: show a one-time pairing code",
	Long: `pair asks the running desklinkd for a one-time PIN, valid for five
minutes, and prints the pairing code (address, PIN and certificate
fingerprint) as a QR code that the DeskLink app scans and as a line of JSON
to paste into the laptop client. The clients pair only from this code, never
from the PIN alone. A paired device can then connect from anywhere the host
accepts, with nobody at the desk.

Below the code, pair checks what a device needs from this computer: the
encoder, input, Wake-on-LAN and automatic login, each with the command that
fixes what is missing. Then it waits until a device has paired.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := localClient()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		ticket, err := client.BeginPairing(ctx)
		if err != nil {
			return err
		}
		host, err := os.Hostname()
		if err != nil {
			host = "this computer"
		}
		out := cmd.OutOrStdout()
		p := paletteFor(out)
		text, err := renderPairing(ticket, host, pairChecks(ticket.IP), terminalWidth(out), p)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(out, text); err != nil {
			return err
		}
		device, err := waitForPairing(ctx, client, ticket.Expires)
		if _, werr := io.WriteString(out, pairingResult(p, device, err)); werr != nil {
			return werr
		}
		if err != nil {
			// The line above said what went wrong.
			cmd.SilenceErrors = true
		}
		return err
	},
}

func init() {
	rootCmd.AddCommand(pairCmd)
}

// pairChecks are the checks pair shows: what a paired device needs from
// this computer. ip is the address in the pairing code.
func pairChecks(ip string) []doctor.Check {
	video.Init()
	checks := []doctor.Check{
		doctor.Encoders(video.ProbeEncoder(video.CodecAV1), video.ProbeEncoder(video.CodecHEVC)),
		doctor.Input(input.DevicePath, input.CheckAccess(input.DevicePath), doctor.UdevRuleInstalled()),
	}
	var wake desktop.Wake
	addr, err := netip.ParseAddr(ip)
	if err == nil {
		wake = desktop.WakeFor(addr)
	}
	checks = append(checks, doctor.WakeOnLAN(wake, err))
	return append(checks, doctor.Autologin(readAutologin()))
}

const (
	// pairGap separates the QR code from the text beside it.
	pairGap = "   "
	// pairTextRow is the QR row beside which the text starts: below the
	// code's quiet zone, level with its finder patterns.
	pairTextRow = 2
	// fingerprintGroup and fingerprintLine split the fingerprint into groups
	// of hex digits, so it can be compared group by group.
	fingerprintGroup = 8
	fingerprintLine  = 4
)

// renderPairing returns what pair shows before it waits: a title, the QR
// code with how to use it, the PIN and the fingerprint beside it, the
// pairing line of JSON on a line of its own so that it can be copied whole,
// and checks. The text goes beside the code when the terminal is at least
// width columns wide, and below it otherwise (width 0: not a terminal).
func renderPairing(t dlnet.PairingTicket, host string, checks []doctor.Check, width int, p palette) (string, error) {
	var others []string
	for _, a := range t.Addresses {
		if a != t.IP {
			others = append(others, a)
		}
	}
	payload, err := json.Marshal(pairPayload{IP: t.IP, Port: t.Port, PIN: t.PIN, Fingerprint: t.Fingerprint, Addresses: others})
	if err != nil {
		return "", fmt.Errorf("encode pairing payload: %w", err)
	}
	qr, err := qrcode.New(string(payload), qrcode.Medium)
	if err != nil {
		return "", fmt.Errorf("render QR code: %w", err)
	}
	code := strings.Split(strings.TrimSuffix(qr.ToSmallString(false), "\n"), "\n")
	side := []string{
		p.dim("Scan the code with the tablet,"),
		p.dim("or paste the line below into the laptop client."),
		"",
		"PIN          " + groupPIN(t.PIN) + ", valid until " + t.Expires.Local().Format("15:04"),
	}
	for i, line := range fingerprintLines(t.Fingerprint) {
		label := "Fingerprint  "
		if i > 0 {
			label = strings.Repeat(" ", len(label))
		}
		side = append(side, label+line)
	}

	var b strings.Builder
	b.WriteString(p.bold("Pair a device with "+host) + "\n\n")
	b.WriteString(besideOrBelow(code, side, width))
	b.WriteString("\n" + string(payload) + "\n\n")
	if err := writeChecks(&b, p, "", checks, nameWidth(checks)); err != nil {
		return "", err
	}
	b.WriteString("\n" + p.dim("Waiting for the device… Ctrl+C stops.") + "\n")
	return b.String(), nil
}

// besideOrBelow puts side beside code, starting at pairTextRow, when both
// fit in width columns; otherwise below it after an empty line.
func besideOrBelow(code, side []string, width int) string {
	codeWidth, sideWidth := 0, 0
	for _, l := range code {
		codeWidth = max(codeWidth, visibleWidth(l))
	}
	for _, l := range side {
		sideWidth = max(sideWidth, visibleWidth(l))
	}
	var b strings.Builder
	if width < codeWidth+len(pairGap)+sideWidth || len(code) < pairTextRow+len(side) {
		for _, l := range code {
			b.WriteString(l + "\n")
		}
		b.WriteString("\n")
		for _, l := range side {
			b.WriteString(l + "\n")
		}
		return b.String()
	}
	for i, l := range code {
		if j := i - pairTextRow; j >= 0 && j < len(side) && side[j] != "" {
			l = padRight(l, codeWidth) + pairGap + side[j]
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// groupPIN writes a six-digit PIN as two groups of three, "482 913".
func groupPIN(pin string) string {
	if len(pin) != 6 {
		return pin
	}
	return pin[:3] + " " + pin[3:]
}

// fingerprintLines splits a hex fingerprint into lines of groups.
func fingerprintLines(fp string) []string {
	var groups []string
	for len(fp) > fingerprintGroup {
		groups = append(groups, fp[:fingerprintGroup])
		fp = fp[fingerprintGroup:]
	}
	groups = append(groups, fp)
	var lines []string
	for len(groups) > fingerprintLine {
		lines = append(lines, strings.Join(groups[:fingerprintLine], " "))
		groups = groups[fingerprintLine:]
	}
	return append(lines, strings.Join(groups, " "))
}

// pairingError ends the wait without a paired device; fix is the command
// that starts over, if one helps.
type pairingError struct{ msg, fix string }

func (e *pairingError) Error() string { return e.msg }

// pairingResult is the line that ends pair: the device that paired, or why
// none did.
func pairingResult(p palette, device string, err error) string {
	if err == nil {
		return p.symbol(doctor.OK) + " Paired with " + p.bold(device) + "\n"
	}
	line := p.symbol(doctor.Problem) + " " + err.Error()
	var pe *pairingError
	if errors.As(err, &pe) && pe.fix != "" {
		line += " " + p.dim("→ "+pe.fix)
	}
	return line + "\n"
}

// waitForPairing polls the daemon until a device paired and returns its
// name.
func waitForPairing(ctx context.Context, client *dlnet.LocalClient, expires time.Time) (string, error) {
	t := time.NewTicker(pairPollInterval)
	defer t.Stop()
	stopped := &pairingError{msg: "Stopped before a device paired"}
	for {
		select {
		case <-ctx.Done():
			return "", stopped
		case <-t.C:
		}
		st, err := client.Status(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return "", stopped
			}
			return "", err
		}
		switch crypto.PairingState(st.State) {
		case crypto.PairingDone:
			return st.Device, nil
		case crypto.PairingExpired:
			return "", &pairingError{msg: "The PIN expired at " + expires.Local().Format("15:04"), fix: "desklinkd pair"}
		case crypto.PairingLocked:
			return "", &pairingError{msg: "Too many wrong PINs; the PIN no longer works", fix: "desklinkd pair"}
		}
	}
}
