package cmd

import (
	"os"

	"github.com/mtfuller/flagbase/internal/color"
	"github.com/mtfuller/flagbase/internal/logger"
	"github.com/spf13/cobra"
)

var (
	verbose  bool
	logLevel string
)

var rootCmd = &cobra.Command{
	Use:   "flagbase",
	Short: "A context-aware feature flag and PaaS engine",
	Long: color.Bold("flagbase") + ` is an open-source PaaS that treats feature flags,
A/B testing, and deep observability as first-class citizens.

  • Context-aware flags evaluated per-request against JWT identity
  • Embedded SQLite, NATS message bus, and WebAssembly runtime
  • Test in production safely — flags scoped to developer sessions only
  • Single binary with zero external dependencies in local mode`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if verbose {
			logger.SetLevel(logger.DEBUG)
		} else {
			logger.SetLevel(logger.ParseLogLevel(logLevel))
		}
	},
}

// Execute is the main entry point for the CLI.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		color.Error("Error: %v", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output (debug level)")
	rootCmd.PersistentFlags().StringVarP(&logLevel, "log-level", "l", "info", "set log level (debug, info, warn, error)")
}
