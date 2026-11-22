// Command desklink is the DeskLink laptop client: it shows a desktop running
// desklinkd in a GTK window and sends the laptop's keyboard and mouse to it.
package main

import "os"

// Version is set at build time via ldflags.
var Version = "dev"

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
