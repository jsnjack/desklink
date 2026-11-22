// Command desklinkd streams the GNOME desktop to the DeskLink Android client
// and injects the client's keyboard, mouse and pen input.
package main

import "os"

// Version is set at build time via ldflags.
var Version = "dev"

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
