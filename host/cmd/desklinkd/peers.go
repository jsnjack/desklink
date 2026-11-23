package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	dlnet "desklink/internal/net"
)

var peersCmd = &cobra.Command{
	Use:   "peers",
	Short: "List the paired devices",
	Long: `peers lists the devices paired with this computer: their ID, name, when
they were paired and when they last connected. Remove one with
"desklinkd peers remove".`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := localClient()
		if err != nil {
			return err
		}
		peers, err := client.Peers(cmd.Context())
		if err != nil {
			return err
		}
		return writePeers(cmd.OutOrStdout(), peers, time.Now())
	},
}

var peersRemoveCmd = &cobra.Command{
	Use:   "remove ID-OR-NAME...",
	Short: "Forget paired devices",
	Long: `remove forgets each named device: it can no longer connect, and a session
it has running ends at once. Name a device by its ID, the start of its ID
(at least 4 characters) or its exact name, as "desklinkd peers" lists them.
Pairing it again needs a new PIN. If the daemon cannot save the removal, the
device still cannot connect, the command fails and "desklinkd peers" marks
the device until removing it again succeeds.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := localClient()
		if err != nil {
			return err
		}
		return removePeers(cmd.Context(), client, args, cmd.OutOrStdout())
	},
}

func init() {
	peersCmd.AddCommand(peersRemoveCmd)
	rootCmd.AddCommand(peersCmd)
}

func localClient() (*dlnet.LocalClient, error) {
	socket, err := dlnet.LocalSocketPath()
	if err != nil {
		return nil, err
	}
	return dlnet.NewLocalClient(socket), nil
}

// peerClient is what removePeers needs from the daemon.
type peerClient interface {
	Peers(ctx context.Context) ([]dlnet.PeerInfo, error)
	RemovePeer(ctx context.Context, id string) (dlnet.PeerInfo, error)
}

func removePeers(ctx context.Context, c peerClient, names []string, out io.Writer) error {
	peers, err := c.Peers(ctx)
	if err != nil {
		return err
	}
	// Resolve every name first, so a typo removes nothing.
	var ids []string
	for _, name := range names {
		p, err := findPeer(peers, name)
		if err != nil {
			return err
		}
		ids = append(ids, p.ID)
	}
	// Each removal stands alone: one that fails, or is not saved, does not
	// keep the others paired.
	var failed []error
	for _, id := range ids {
		p, err := c.RemovePeer(ctx, id)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		note := ""
		if p.Connected {
			note = "; its session ended"
		}
		if _, err := fmt.Fprintf(out, "Forgot %s (%s)%s\n", p.Name, p.ID, note); err != nil {
			return err
		}
	}
	return errors.Join(failed...)
}

// minIDPrefix is the shortest ID start that names a device.
const minIDPrefix = 4

// findPeer finds one device by ID, ID prefix or exact name (ignoring case).
func findPeer(peers []dlnet.PeerInfo, name string) (dlnet.PeerInfo, error) {
	var matches []dlnet.PeerInfo
	for _, p := range peers {
		if p.ID == name {
			return p, nil
		}
		if strings.EqualFold(p.Name, name) || (len(name) >= minIDPrefix && strings.HasPrefix(p.ID, name)) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return dlnet.PeerInfo{}, fmt.Errorf("no paired device is called or has an ID starting with %q (see desklinkd peers)", name)
	}
	var ids []string
	for _, p := range matches {
		ids = append(ids, p.ID)
	}
	return dlnet.PeerInfo{}, errors.New(name + " matches several devices; name one by its ID: " + strings.Join(ids, ", "))
}

func writePeers(out io.Writer, peers []dlnet.PeerInfo, now time.Time) error {
	if len(peers) == 0 {
		_, err := fmt.Fprintln(out, "No paired devices. Pair one with: desklinkd pair")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tPAIRED\tLAST CONNECTED"); err != nil {
		return err
	}
	for _, p := range peers {
		last := "never"
		switch {
		case p.RemovalNotSaved:
			last = "removed, but not saved: remove it again"
		case p.Connected:
			last = "connected now"
		case !p.LastSeen.IsZero():
			last = ago(now.Sub(p.LastSeen))
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Paired.Local().Format("2006-01-02 15:04"), last); err != nil {
			return err
		}
	}
	return w.Flush()
}

// ago says how long ago in the largest whole unit.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
