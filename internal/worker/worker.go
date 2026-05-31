package worker

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
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
			w.checkFlagAnomalies()
			w.checkFunctionAnomalies()
		}
	}
}

// checkFlagAnomalies auto-disables any flag that generated >10 errors in the last 5 minutes.
func (w *Worker) checkFlagAnomalies() {
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
			logger.Warn("anomaly check: scan row: %v", err)
			continue
		}
		msg := fmt.Sprintf("flag '%s' had %d errors in the last 5 minutes — auto-disabled", flagKey, cnt)
		logger.Warn("Anomaly: %s", msg)
		w.recordAnomaly("flag_error_rate", flagKey, "critical", msg)

		if f, ok := w.engine.GetFlag(flagKey); ok && f.Enabled {
			f.Enabled = false
			if err := w.engine.UpdateFlag(f); err != nil {
				logger.Error("auto-disable flag %s: %v", flagKey, err)
			} else if err := w.bus.Publish("flagbase.flag.disabled", []byte(flagKey)); err != nil {
				logger.Error("publish flag.disabled for %s: %v", flagKey, err)
			}
		}
	}
}

// checkFunctionAnomalies detects high error rates and latency spikes in function invocations.
func (w *Worker) checkFunctionAnomalies() {
	// Error rate: functions with ≥5 invocations and >20% errors in the last 10 minutes.
	rows, err := w.db.Query(`
		SELECT fi.function_id, f.name,
		       COUNT(*) AS total,
		       SUM(CASE WHEN fi.success = 0 THEN 1 ELSE 0 END) AS errors
		FROM function_invocations fi
		JOIN functions f ON f.id = fi.function_id
		WHERE fi.started_at > datetime('now', '-10 minutes')
		  AND fi.completed_at IS NOT NULL
		GROUP BY fi.function_id
		HAVING total >= 5 AND (CAST(errors AS REAL) / total) > 0.20`)
	if err != nil {
		logger.Warn("function anomaly check: %v", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var fnID, fnName string
			var total, errors int
			if err := rows.Scan(&fnID, &fnName, &total, &errors); err != nil {
				continue
			}
			rate := int(float64(errors) / float64(total) * 100)
			msg := fmt.Sprintf("function '%s' has %d%% error rate (%d/%d) in the last 10 minutes", fnName, rate, errors, total)
			logger.Warn("Function anomaly: %s", msg)
			w.recordAnomaly("function_error_rate", fnID, "warning", msg)
		}
	}

	// Latency spike: functions whose recent avg exceeds 3x their 24-hour baseline.
	latRows, err := w.db.Query(`
		WITH recent AS (
			SELECT function_id, AVG(execution_ms) AS recent_avg
			FROM function_invocations
			WHERE started_at > datetime('now', '-10 minutes')
			  AND success = 1
			  AND execution_ms IS NOT NULL
			GROUP BY function_id
			HAVING COUNT(*) >= 3
		),
		baseline AS (
			SELECT function_id, AVG(execution_ms) AS base_avg
			FROM function_invocations
			WHERE started_at > datetime('now', '-24 hours')
			  AND success = 1
			  AND execution_ms IS NOT NULL
			GROUP BY function_id
		)
		SELECT r.function_id, f.name, r.recent_avg, b.base_avg
		FROM recent r
		JOIN baseline b ON r.function_id = b.function_id
		JOIN functions f ON f.id = r.function_id
		WHERE b.base_avg > 0 AND (r.recent_avg / b.base_avg) > 3.0`)
	if err != nil {
		logger.Warn("function latency check: %v", err)
		return
	}
	defer latRows.Close()
	for latRows.Next() {
		var fnID, fnName string
		var recentAvg, baseAvg float64
		if err := latRows.Scan(&fnID, &fnName, &recentAvg, &baseAvg); err != nil {
			continue
		}
		msg := fmt.Sprintf("function '%s' avg latency %.0fms is %.1fx above 24h baseline %.0fms", fnName, recentAvg, recentAvg/baseAvg, baseAvg)
		logger.Warn("Latency spike: %s", msg)
		w.recordAnomaly("function_latency_spike", fnID, "warning", msg)
	}
}

// recordAnomaly inserts an anomaly_event if no unresolved event of the same type
// and ref_id was recorded in the last 30 minutes (deduplication window).
func (w *Worker) recordAnomaly(anomalyType, refID, severity, message string) {
	var existing int
	_ = w.db.QueryRow(`
		SELECT COUNT(*) FROM anomaly_events
		WHERE anomaly_type = ? AND ref_id = ? AND resolved_at IS NULL
		  AND detected_at > datetime('now', '-30 minutes')`,
		anomalyType, refID).Scan(&existing)
	if existing > 0 {
		return
	}
	id := newAnomalyID()
	_, _ = w.db.Exec(`
		INSERT INTO anomaly_events (id, anomaly_type, ref_id, severity, message)
		VALUES (?, ?, ?, ?, ?)`,
		id, anomalyType, refID, severity, message)
}

func newAnomalyID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}

// RecordMetric inserts a single metric event for a feature flag variant.
func (w *Worker) RecordMetric(flagKey, variant, eventType string, value float64) error {
	_, err := w.db.Exec(
		`INSERT INTO metrics (flag_key, variant, event_type, value) VALUES (?, ?, ?, ?)`,
		flagKey, variant, eventType, value,
	)
	return err
}
