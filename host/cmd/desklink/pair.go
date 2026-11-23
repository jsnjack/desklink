package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"desklink/internal/client"
)

const pairTimeout = 30 * time.Second

var (
	flagPairPIN         string
	flagPairFingerprint string
	flagPairPort        int
)

var pairCmd = &cobra.Command{
	Use:   "pair [pairing code | host]",
	Short: "Pair with a desktop running desklinkd",
	Long: `pair pairs this laptop with a desktop. Run "desklinkd pair" on the desktop,
then pass the JSON line it prints (quoted), or the host address with --pin.
A JSON code also pins the host's certificate; with --pin alone, pass
--fingerprint too or compare the printed fingerprint with the desktop's.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openStore()
		if err != nil {
			return err
		}
		ip, port, pin, fp := args[0], flagPairPort, flagPairPIN, flagPairFingerprint
		var addresses []string
		if code, err := client.ParsePairingCode(args[0]); err == nil {
			ip, port, pin, fp, addresses = code.IP, code.Port, code.PIN, code.Fingerprint, code.Addresses
		} else if strings.HasPrefix(strings.TrimSpace(args[0]), "{") {
			return err
		}
		if pin == "" {
			return errors.New("pass the pairing code printed by desklinkd pair, or --pin")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), pairTimeout)
		defer cancel()
		p, ok := store.FindByAddress(ip, port)
		if !ok {
			if p, err = store.Add(ip, ip, port); err != nil {
				return err
			}
		}
		p = p.WithAddresses(ip, addresses)
		if h, err := client.Health(ctx, ip, port, fp); err == nil {
			p.MAC = h.MAC
			p = p.WithAddresses(ip, h.Addresses)
		}
		res, err := client.Pair(ctx, ip, port, pin, fp, deviceName())
		if err != nil {
			return err
		}
		p.PeerID, p.PeerKey, p.Fingerprint = res.PeerID, res.PeerKey, res.Fingerprint
		// Pairing a known machine on a new network folds its old entry in.
		if _, err := store.Absorb(p); err != nil {
			return err
		}
		fmt.Printf("Paired with %s:%d. Certificate fingerprint %s\n", ip, port, res.Fingerprint)
		return nil
	},
}

func init() {
	pairCmd.Flags().StringVar(&flagPairPIN, "pin", "", "the six-digit PIN from desklinkd pair")
	pairCmd.Flags().StringVar(&flagPairFingerprint, "fingerprint", "", "the host certificate fingerprint to pin")
	pairCmd.Flags().IntVar(&flagPairPort, "port", client.DefaultPort, "the host's control port")
	rootCmd.AddCommand(pairCmd)
}
