package worker

import (
	"database/sql"
	"time"

	"github.com/mtfuller/flagbase/internal/event"
	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/logger"
)

// Worker runs background tasks: anomaly detection and metric aggregation.
type Worker struct {
	db     *sql.DB
	engine *feature.Engine
	bus    *event.Bus
	stop   chan struct{}
	done   chan struct{}
}

func New(db *sql.DB, engine *feature.Engine, bus *event.Bus) *Worker {
	return &Worker{
		db:     db,
		engine: engine,
		bus:    bus,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start launches the worker goroutine.
func (w *Worker) Start() { go w.run() }

// Stop signals the worker to exit and waits for it to finish.
func (w *Worker) Stop() {
	close(w.stop)
	<-w.done
}

func (w *Worker) run() {
	defer close(w.done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.checkAnomalies()
		}
	}
}

// checkAnomalies auto-disables any flag that generated >10 errors in the last 5 minutes.
func (w *Worker) checkAnomalies() {
	rows, err := w.db.Query(`
		SELECT flag_key, COUNT(*) AS cnt
		FROM   metrics
		WHERE  event_type  = 'error'
		  AND  recorded_at > datetime('now', '-5 minutes')
		GROUP  BY flag_key
		HAVING cnt > 10`)
	if err != nil {
		logger.Warn("anomaly check: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var flagKey string
		var cnt int
		if err := rows.Scan(&flagKey, &cnt); err != nil {
			continue
		}
		logger.Warn("Anomaly: flag=%s errors=%d in 5m — auto-disabling", flagKey, cnt)

		if f, ok := w.engine.GetFlag(flagKey); ok && f.Enabled {
			f.Enabled = false
			if err := w.engine.UpdateFlag(f); err != nil {
				logger.Error("auto-disable flag %s: %v", flagKey, err)
			} else {
				_ = w.bus.Publish("flagbase.flag.disabled", []byte(flagKey))
			}
		}
	}
}

// RecordMetric inserts a single metric event for a feature flag variant.
func (w *Worker) RecordMetric(flagKey, variant, eventType string, value float64) error {
	_, err := w.db.Exec(
		`INSERT INTO metrics (flag_key, variant, event_type, value) VALUES (?, ?, ?, ?)`,
		flagKey, variant, eventType, value,
	)
	return err
}
