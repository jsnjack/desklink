package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

const pairPollInterval = time.Second

// pairPayload is the JSON in the QR code, read by the Android pairing dialog.
type pairPayload struct {
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	PIN         string `json:"pin"`
	Fingerprint string `json:"fingerprint"`
}

var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Pair a tablet: show a one-time PIN and QR code",
	Long: `pair asks the running desklinkd for a one-time PIN, valid for five
minutes, and prints it with a QR code that the DeskLink app scans. GNOME also
asks once for screen-sharing permission, which later sessions reuse.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		socket, err := dlnet.LocalSocketPath()
		if err != nil {
			return err
		}
		client := dlnet.NewLocalClient(socket)
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		ticket, err := client.BeginPairing(ctx)
		if err != nil {
			return err
		}
		text, err := renderPairing(ticket)
		if err != nil {
			return err
		}
		fmt.Print(text)
		return waitForPairing(ctx, client, ticket.Expires)
	},
}

func init() {
	rootCmd.AddCommand(pairCmd)
}

// renderPairing returns the terminal QR code, the pairing JSON it encodes and
// instructions.
func renderPairing(t dlnet.PairingTicket) (string, error) {
	payload, err := json.Marshal(pairPayload{IP: t.IP, Port: t.Port, PIN: t.PIN, Fingerprint: t.Fingerprint})
	if err != nil {
		return "", fmt.Errorf("encode pairing payload: %w", err)
	}
	qr, err := qrcode.New(string(payload), qrcode.Medium)
	if err != nil {
		return "", fmt.Errorf("render QR code: %w", err)
	}
	return qr.ToSmallString(false) + fmt.Sprintf("\n%s\n\nPIN %s, valid until %s\nCertificate fingerprint %s\n"+
		"If GNOME asks to share the screen, choose the monitor and allow it.\n",
		payload, t.PIN, t.Expires.Local().Format("15:04:05"), t.Fingerprint), nil
}

func waitForPairing(ctx context.Context, client *dlnet.LocalClient, expires time.Time) error {
	t := time.NewTicker(pairPollInterval)
	defer t.Stop()
	lastCapture := ""
	paired := false
	for {
		select {
		case <-ctx.Done():
			return errors.New("pairing interrupted")
		case <-t.C:
		}
		st, err := client.Status(ctx)
		if err != nil {
			return err
		}
		if st.Capture != lastCapture {
			lastCapture = st.Capture
			slog.Debug("screen-sharing permission", "status", st.Capture)
			if st.Capture == "granted" {
				fmt.Println("Screen sharing allowed.")
			} else if st.Capture != "pending" && st.Capture != "unknown" {
				fmt.Println("Screen sharing was not allowed:", st.Capture)
			}
		}
		switch crypto.PairingState(st.State) {
		case crypto.PairingDone:
			if !paired {
				paired = true
				fmt.Printf("Paired with %s.\n", st.Device)
			}
			if st.Capture != "pending" {
				return nil
			}
		case crypto.PairingExpired:
			return fmt.Errorf("PIN expired at %s; run desklinkd pair again", expires.Local().Format("15:04:05"))
		case crypto.PairingLocked:
			return errors.New("too many wrong PINs; run desklinkd pair again")
		}
	}
}
