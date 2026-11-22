package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"desklink/internal/client"
	"desklink/internal/logging"
	"desklink/internal/viewer"
)

const tracePath = "/tmp/desklink.log"

var (
	flagVersion bool
	flagDebug   bool
	flagTrace   bool
	flagConfig  string

	logger *logging.Logger
)

var rootCmd = &cobra.Command{
	Use:   "desklink",
	Short: "DeskLink laptop client",
	Long: `desklink shows a desktop running desklinkd in a window and sends the
laptop's keyboard and mouse to it. Pair once with "desklink pair" or from the
window, then connect from the host list.`,
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		l, err := logging.Setup(flagDebug, flagTrace, tracePath)
		if err != nil {
			return err
		}
		logger = l
		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if logger != nil {
			logger.Close()
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagVersion {
			fmt.Println(Version)
			return nil
		}
		store, err := openStore()
		if err != nil {
			return err
		}
		if code := viewer.Run(viewer.Options{Store: store, DeviceName: deviceName()}, []string{os.Args[0]}); code != 0 {
			return fmt.Errorf("desklink exited with status %d", code)
		}
		return nil
	},
}

func init() {
	rootCmd.Flags().BoolVar(&flagVersion, "version", false, "print the version and exit")
	rootCmd.PersistentFlags().BoolVarP(&flagDebug, "debug", "d", false, "verbose logs to stderr")
	rootCmd.PersistentFlags().BoolVar(&flagTrace, "trace", false, "maximum detail to "+tracePath)
	rootCmd.PersistentFlags().StringVarP(&flagConfig, "config", "c", "", "hosts file (default ~/.config/desklink-client/hosts.json)")
}

func openStore() (*client.Store, error) {
	path := flagConfig
	if path == "" {
		var err error
		if path, err = client.DefaultStorePath(); err != nil {
			return nil, err
		}
	}
	return client.OpenStore(path)
}

// deviceName labels this laptop in the host's peers.json.
func deviceName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "Laptop"
	}
	return host
}
