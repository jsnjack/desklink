package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"desklink/internal/config"
	"desklink/internal/daemon"
)

var flagSessionCount int

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List the recent sessions",
	Long: `sessions lists the most recent sessions, newest first: the device, its
address, when it connected and for how long, what the session showed, the
lowest quality level the stream went down to, and why the session ended.
desklinkd keeps the last 100 in sessions.json next to config.json.

On a terminal the reason is green for a normal end, yellow for a lost
connection and red for a failure.`,
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
		out := cmd.OutOrStdout()
		return writeSessions(out, paletteFor(out), recs, flagSessionCount)
	},
}

func init() {
	sessionsCmd.Flags().IntVarP(&flagSessionCount, "count", "n", 20, "how many sessions to list")
	rootCmd.AddCommand(sessionsCmd)
}

// writeSessions prints the newest n records of recs (oldest first) as a
// table, newest first, with why each ended in the colour of how it ended.
func writeSessions(out io.Writer, p palette, recs []config.SessionRecord, n int) error {
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
			r.End.Sub(r.Start).Round(time.Second), orDash(r.Display), orDash(r.WorstLevel), endedColour(p, r.Ended)); err != nil {
			return err
		}
	}
	return w.Flush()
}

// endedColour paints why a session ended: green for a normal end, yellow
// for a lost connection, red for a failure. It is the table's last column,
// so the colour does not upset the alignment.
func endedColour(p palette, ended string) string {
	switch daemon.SessionEndOf(ended) {
	case daemon.EndNormal:
		return p.green(ended)
	case daemon.EndLost:
		return p.yellow(ended)
	case daemon.EndFailed:
		return p.red(ended)
	}
	return ended
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
