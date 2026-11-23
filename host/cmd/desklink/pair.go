package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"desklink/internal/client"
)

const pairTimeout = 30 * time.Second

var pairCmd = &cobra.Command{
	Use:   "pair PAIRING-CODE",
	Short: "Pair with a desktop running desklinkd",
	Long: `pair pairs this laptop with a desktop. Run "desklinkd pair" on the desktop
and pass the line of JSON it prints, quoted. The code carries the desktop's
address, PIN and certificate fingerprint; the laptop trusts only that
certificate, so only that desktop can answer. A PIN alone is not accepted.`,
	Example: `  desklink pair '{"ip":"192.168.178.11","port":9443,"pin":"123456","fingerprint":"…"}'`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		code, err := client.ParsePairingCode(args[0])
		if err != nil {
			return err
		}
		store, err := openStore()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), pairTimeout)
		defer cancel()
		p, err := client.PairWithCode(ctx, store, code, "", deviceName())
		if err != nil {
			return err
		}
		fmt.Printf("Paired with %s at %s:%d. Certificate fingerprint %s\n", p.Name, p.IP, p.Port, p.Fingerprint)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pairCmd)
}
