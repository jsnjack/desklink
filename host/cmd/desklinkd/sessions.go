package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"desklink/internal/config"
)

var flagSessionCount int

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List the recent sessions",
	Long: `sessions lists the most recent sessions, newest first: the device, its
address, when it connected and for how long, what the session showed, the
lowest quality level the stream went down to, and why the session ended.
desklinkd keeps the last 100 in sessions.json next to config.json.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		_, paths, err := loadConfig()
		if err != nil {
			return err
		}
		recs, err := config.ReadSessions(paths.Sessions())
		if err != nil {
			return err
		}
		return writeSessions(cmd.OutOrStdout(), recs, flagSessionCount)
	},
}

func init() {
	sessionsCmd.Flags().IntVarP(&flagSessionCount, "count", "n", 20, "how many sessions to list")
	rootCmd.AddCommand(sessionsCmd)
}

// writeSessions prints the newest n records of recs (oldest first) as a table.
func writeSessions(out io.Writer, recs []config.SessionRecord, n int) error {
	if len(recs) == 0 {
		_, err := fmt.Fprintln(out, "No sessions yet.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "STARTED\tDEVICE\tFROM\tFOR\tSCREEN\tLOWEST LEVEL\tENDED"); err != nil {
		return err
	}
	for i := len(recs) - 1; i >= 0 && i >= len(recs)-n; i-- {
		r := recs[i]
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Start.Local().Format("2006-01-02 15:04"), r.Device, r.From,
			r.End.Sub(r.Start).Round(time.Second), orDash(r.Display), orDash(r.WorstLevel), r.Ended); err != nil {
			return err
		}
	}
	return w.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
