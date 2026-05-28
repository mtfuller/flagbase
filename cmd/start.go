package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mtfuller/flagbase/internal/api"
	"github.com/mtfuller/flagbase/internal/color"
	"github.com/mtfuller/flagbase/internal/config"
	"github.com/mtfuller/flagbase/internal/database"
	"github.com/mtfuller/flagbase/internal/event"
	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/logger"
	"github.com/mtfuller/flagbase/internal/storage"
	"github.com/mtfuller/flagbase/internal/worker"
	"github.com/spf13/cobra"
)

var configFile string

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the flagbase PaaS engine",
	Long: `Start flagbase with all embedded services:
  • SQLite database (WAL mode)
  • NATS message bus
  • Feature flag evaluation engine
  • Context-aware gateway proxy
  • Anomaly-detection background worker`,
	RunE: runStart,
}

func runStart(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger.Info("Starting flagbase on %s:%d", cfg.Server.Host, cfg.Server.Port)

	db, err := database.Connect(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()

	if err := database.Migrate(db); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	logger.Info("Database ready: %s", cfg.Database.Path)

	bus, err := event.Start(cfg.Events.NATSPort)
	if err != nil {
		return fmt.Errorf("event bus: %w", err)
	}
	defer bus.Stop()
	logger.Info("NATS bus ready on port %d", cfg.Events.NATSPort)

	store := storage.NewLocalAdapter(cfg.Storage.BasePath)
	iamSvc := iam.NewService(db, cfg.IAM.JWTSecret, cfg.IAM.TokenTTL)

	featureEng, err := feature.NewEngine(db)
	if err != nil {
		return fmt.Errorf("feature engine: %w", err)
	}

	bgWorker := worker.New(db, featureEng, bus)
	bgWorker.Start()
	defer bgWorker.Stop()

	srv := api.NewServer(cfg, db, iamSvc, featureEng, store, bus)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		color.Success("flagbase is running → http://%s:%d", cfg.Server.Host, cfg.Server.Port)
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			logger.Error("server: %v", err)
			quit <- syscall.SIGTERM
		}
	}()

	<-quit
	logger.Info("Shutting down flagbase...")
	return srv.Stop()
}

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.Flags().StringVarP(&configFile, "config", "c", "", "path to YAML config file")
}
