package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"desklink/internal/config"
	"desklink/internal/daemon"
	"desklink/internal/logging"
)

var (
	flagVersion bool
	flagDebug   bool
	flagTrace   bool
	flagConfig  string

	logger *logging.Logger
)

const tracePath = "/tmp/desklinkd.log"

var rootCmd = &cobra.Command{
	Use:   "desklinkd",
	Short: "DeskLink remote desktop host",
	Long: `desklinkd streams the GNOME desktop to the DeskLink Android client over
TLS (control, port 9443) and encrypted UDP (video and input, port 9444).
Run "desklinkd pair" at the desk to pair a tablet.`,
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
		cfg, paths, err := loadConfig()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return daemon.Run(ctx, daemon.Options{
			Config:  cfg,
			Paths:   paths,
			Version: Version,
			OnListen: func(network, addr string) {
				logger.Announce("Listening on %s (%s)", addr, network)
			},
		})
	},
}

func init() {
	rootCmd.Flags().BoolVar(&flagVersion, "version", false, "print the version and exit")
	rootCmd.PersistentFlags().BoolVarP(&flagDebug, "debug", "d", false, "verbose logs to stderr")
	rootCmd.PersistentFlags().BoolVar(&flagTrace, "trace", false, "maximum detail to "+tracePath)
	rootCmd.PersistentFlags().StringVarP(&flagConfig, "config", "c", "", "config file (default ~/.config/desklink/config.json)")
}

// loadConfig reads the config file. Peers, state and the certificate live in
// the directory of the config file.
func loadConfig() (config.Config, config.Paths, error) {
	path := flagConfig
	if path == "" {
		dir, err := config.Dir()
		if err != nil {
			return config.Config{}, config.Paths{}, err
		}
		path = filepath.Join(dir, "config.json")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, config.Paths{}, err
	}
	return cfg, config.Paths{Dir: filepath.Dir(path)}, nil
}
