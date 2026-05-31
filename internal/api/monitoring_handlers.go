package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// MonitoringHandlers provides endpoints for traces, anomalies, and custom metrics.
type MonitoringHandlers struct {
	DB *sql.DB
}

// ── Traces ────────────────────────────────────────────────────────────────────

type traceRow struct {
	ID          string     `json:"id"`
	RootType    string     `json:"root_type"`
	RootRef     string     `json:"root_ref"`
	UserID      string     `json:"user_id"`
	TenantID    string     `json:"tenant_id"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Status      string     `json:"status"`
	EventCount  int        `json:"event_count"`
	DurationMs  *int64     `json:"duration_ms,omitempty"`
}

type traceEvent struct {
	ID            string     `json:"id"`
	TraceID       string     `json:"trace_id"`
	ParentEventID *string    `json:"parent_event_id,omitempty"`
	EventType     string     `json:"event_type"`
	Sequence      int        `json:"sequence"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	DurationMs    *int64     `json:"duration_ms,omitempty"`
	Status        string     `json:"status"`
	Metadata      string     `json:"metadata"`
}

// ListTraces returns recent traces with optional filters.
// Query params: status (running|success|error), limit (default 50).
func (h *MonitoringHandlers) ListTraces(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	query := `
		SELECT t.id, t.root_type, t.root_ref, t.user_id, t.tenant_id,
		       t.started_at, t.completed_at, t.status,
		       COUNT(te.id) AS event_count,
		       CASE WHEN t.completed_at IS NOT NULL
		            THEN CAST((julianday(t.completed_at) - julianday(t.started_at)) * 86400000 AS INTEGER)
		            ELSE NULL
		       END AS duration_ms
		FROM traces t
		LEFT JOIN trace_events te ON te.trace_id = t.id`
	args := []interface{}{}
	if status != "" {
		query += ` WHERE t.status = ?`
		args = append(args, status)
	}
	query += ` GROUP BY t.id ORDER BY t.started_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := h.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	traces := []traceRow{}
	for rows.Next() {
		var tr traceRow
		var completedAt sql.NullTime
		var durMs sql.NullInt64
		if err := rows.Scan(
			&tr.ID, &tr.RootType, &tr.RootRef, &tr.UserID, &tr.TenantID,
			&tr.StartedAt, &completedAt, &tr.Status,
			&tr.EventCount, &durMs,
		); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if completedAt.Valid {
			tr.CompletedAt = &completedAt.Time
		}
		if durMs.Valid {
			tr.DurationMs = &durMs.Int64
		}
		traces = append(traces, tr)
	}
	writeJSON(w, http.StatusOK, traces)
}

// GetTrace returns a single trace with all its span events.
func (h *MonitoringHandlers) GetTrace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Fetch the trace header.
	var tr traceRow
	var completedAt sql.NullTime
	err := h.DB.QueryRowContext(r.Context(), `
		SELECT id, root_type, root_ref, user_id, tenant_id, started_at, completed_at, status
		FROM traces WHERE id = ?`, id).
		Scan(&tr.ID, &tr.RootType, &tr.RootRef, &tr.UserID, &tr.TenantID,
			&tr.StartedAt, &completedAt, &tr.Status)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "trace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if completedAt.Valid {
		tr.CompletedAt = &completedAt.Time
		if ms := completedAt.Time.Sub(tr.StartedAt).Milliseconds(); ms >= 0 {
			tr.DurationMs = &ms
		}
	}

	// Fetch all trace events ordered by sequence.
	evRows, err := h.DB.QueryContext(r.Context(), `
		SELECT id, trace_id, parent_event_id, event_type, sequence,
		       started_at, completed_at, duration_ms, status, metadata
		FROM trace_events
		WHERE trace_id = ?
		ORDER BY sequence ASC, started_at ASC`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer evRows.Close()

	events := []traceEvent{}
	for evRows.Next() {
		var ev traceEvent
		var parentID sql.NullString
		var compAt sql.NullTime
		var durMs sql.NullInt64
		if err := evRows.Scan(
			&ev.ID, &ev.TraceID, &parentID, &ev.EventType, &ev.Sequence,
			&ev.StartedAt, &compAt, &durMs, &ev.Status, &ev.Metadata,
		); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if parentID.Valid {
			ev.ParentEventID = &parentID.String
		}
		if compAt.Valid {
			ev.CompletedAt = &compAt.Time
		}
		if durMs.Valid {
			ev.DurationMs = &durMs.Int64
		}
		if ev.Metadata == "" {
			ev.Metadata = "{}"
		}
		events = append(events, ev)
	}
	tr.EventCount = len(events)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"trace":  tr,
		"events": events,
	})
}

// ── Custom Metrics ─────────────────────────────────────────────────────────────

type customMetricRow struct {
	ID           string    `json:"id"`
	TraceID      string    `json:"trace_id,omitempty"`
	InvocationID string    `json:"invocation_id,omitempty"`
	MetricName   string    `json:"metric_name"`
	MetricValue  float64   `json:"metric_value"`
	Tags         string    `json:"tags"`
	RecordedAt   time.Time `json:"recorded_at"`
}

type metricTimeSeries struct {
	Name   string             `json:"name"`
	Points []metricDataPoint  `json:"points"`
}

type metricDataPoint struct {
	RecordedAt time.Time `json:"recorded_at"`
	Value      float64   `json:"value"`
	Tags       string    `json:"tags"`
}

// GetFunctionMetrics returns custom metrics published by a function.
// Query params: name (filter by metric name), limit (default 200).
func (h *MonitoringHandlers) GetFunctionMetrics(w http.ResponseWriter, r *http.Request) {
	fnID := chi.URLParam(r, "id")
	name := r.URL.Query().Get("name")
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	query := `
		SELECT id, trace_id, invocation_id, metric_name, metric_value, tags, recorded_at
		FROM function_custom_metrics
		WHERE function_id = ?`
	args := []interface{}{fnID}
	if name != "" {
		query += ` AND metric_name = ?`
		args = append(args, name)
	}
	query += ` ORDER BY recorded_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := h.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	metrics := []customMetricRow{}
	for rows.Next() {
		var m customMetricRow
		if err := rows.Scan(&m.ID, &m.TraceID, &m.InvocationID,
			&m.MetricName, &m.MetricValue, &m.Tags, &m.RecordedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if m.Tags == "" {
			m.Tags = "{}"
		}
		metrics = append(metrics, m)
	}

	// Build per-name time-series for charting.
	seriesMap := map[string]*metricTimeSeries{}
	for _, m := range metrics {
		if _, ok := seriesMap[m.MetricName]; !ok {
			seriesMap[m.MetricName] = &metricTimeSeries{Name: m.MetricName}
		}
		seriesMap[m.MetricName].Points = append(seriesMap[m.MetricName].Points, metricDataPoint{
			RecordedAt: m.RecordedAt,
			Value:      m.MetricValue,
			Tags:       m.Tags,
		})
	}
	series := make([]metricTimeSeries, 0, len(seriesMap))
	for _, s := range seriesMap {
		series = append(series, *s)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"metrics": metrics,
		"series":  series,
	})
}

// ── Anomalies ─────────────────────────────────────────────────────────────────

type anomalyRow struct {
	ID           string     `json:"id"`
	AnomalyType  string     `json:"anomaly_type"`
	RefID        string     `json:"ref_id"`
	Severity     string     `json:"severity"`
	Message      string     `json:"message"`
	DetectedAt   time.Time  `json:"detected_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	AutoResolved bool       `json:"auto_resolved"`
}

// ListAnomalies returns anomaly events.
// Query params: resolved (true|false), severity, limit (default 100).
func (h *MonitoringHandlers) ListAnomalies(w http.ResponseWriter, r *http.Request) {
	resolved := r.URL.Query().Get("resolved")
	severity := r.URL.Query().Get("severity")
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	query := `SELECT id, anomaly_type, ref_id, severity, message, detected_at, resolved_at, auto_resolved
	           FROM anomaly_events WHERE 1=1`
	args := []interface{}{}
	if resolved == "true" {
		query += ` AND resolved_at IS NOT NULL`
	} else if resolved == "false" || resolved == "" {
		query += ` AND resolved_at IS NULL`
	}
	if severity != "" {
		query += ` AND severity = ?`
		args = append(args, severity)
	}
	query += ` ORDER BY detected_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := h.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	anomalies := []anomalyRow{}
	for rows.Next() {
		var a anomalyRow
		var resolvedAt sql.NullTime
		var autoResolved int
		if err := rows.Scan(&a.ID, &a.AnomalyType, &a.RefID, &a.Severity,
			&a.Message, &a.DetectedAt, &resolvedAt, &autoResolved); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if resolvedAt.Valid {
			a.ResolvedAt = &resolvedAt.Time
		}
		a.AutoResolved = autoResolved != 0
		anomalies = append(anomalies, a)
	}
	writeJSON(w, http.StatusOK, anomalies)
}

// ResolveAnomaly marks an anomaly event as manually resolved.
func (h *MonitoringHandlers) ResolveAnomaly(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	res, err := h.DB.ExecContext(r.Context(), `
		UPDATE anomaly_events SET resolved_at = CURRENT_TIMESTAMP WHERE id = ? AND resolved_at IS NULL`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "anomaly not found or already resolved")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Monitoring Summary ─────────────────────────────────────────────────────────

type monitoringSummary struct {
	ActiveAnomalies int                 `json:"active_anomalies"`
	TotalTraces     int                 `json:"total_traces"`
	RecentTraces    int                 `json:"recent_traces_1h"`
	FunctionStats   []functionStatRow   `json:"function_stats"`
}

type functionStatRow struct {
	FunctionID    string   `json:"function_id"`
	FunctionName  string   `json:"function_name"`
	TotalInvocations int   `json:"total_invocations"`
	SuccessRate   float64  `json:"success_rate"`
	AvgExecutionMs *float64 `json:"avg_execution_ms,omitempty"`
}

// GetMonitoringSummary returns a dashboard-level summary for the monitoring page.
func (h *MonitoringHandlers) GetMonitoringSummary(w http.ResponseWriter, r *http.Request) {
	s := monitoringSummary{FunctionStats: []functionStatRow{}}

	_ = h.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM anomaly_events WHERE resolved_at IS NULL`).Scan(&s.ActiveAnomalies)
	_ = h.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM traces`).Scan(&s.TotalTraces)
	_ = h.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM traces WHERE started_at > datetime('now', '-1 hour')`).Scan(&s.RecentTraces)

	rows, err := h.DB.QueryContext(r.Context(), `
		SELECT fi.function_id, f.name,
		       COUNT(*) AS total,
		       SUM(CASE WHEN fi.success = 1 THEN 1 ELSE 0 END) AS successes,
		       AVG(CASE WHEN fi.execution_ms IS NOT NULL THEN fi.execution_ms END) AS avg_ms
		FROM function_invocations fi
		JOIN functions f ON f.id = fi.function_id
		WHERE fi.started_at > datetime('now', '-24 hours')
		GROUP BY fi.function_id
		ORDER BY total DESC
		LIMIT 10`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var fs functionStatRow
			var successes int
			var avgMs sql.NullFloat64
			if err := rows.Scan(&fs.FunctionID, &fs.FunctionName, &fs.TotalInvocations, &successes, &avgMs); err == nil {
				if fs.TotalInvocations > 0 {
					fs.SuccessRate = float64(successes) / float64(fs.TotalInvocations) * 100
				}
				if avgMs.Valid {
					fs.AvgExecutionMs = &avgMs.Float64
				}
				s.FunctionStats = append(s.FunctionStats, fs)
			}
		}
	}

	writeJSON(w, http.StatusOK, s)
}

// writeJSON is declared in handlers.go; this file reuses it.
// (no re-declaration needed — same package)

// ── Invocation detail (compute metrics) ──────────────────────────────────────

// GetInvocationDetail returns a single invocation with full compute metrics and linked trace.
func (h *MonitoringHandlers) GetInvocationDetail(w http.ResponseWriter, r *http.Request) {
	invID := chi.URLParam(r, "invId")

	type detail struct {
		ID              string     `json:"id"`
		FunctionID      string     `json:"function_id"`
		StartedAt       time.Time  `json:"started_at"`
		CompletedAt     *time.Time `json:"completed_at,omitempty"`
		Success         bool       `json:"success"`
		Output          string     `json:"output,omitempty"`
		Error           string     `json:"error,omitempty"`
		ExecutionMs     *int64     `json:"execution_ms,omitempty"`
		PeakMemoryBytes int64      `json:"peak_memory_bytes"`
		HostCalls       int        `json:"host_calls"`
		OutputSizeBytes int        `json:"output_size_bytes"`
		TraceID         string     `json:"trace_id,omitempty"`
		CustomMetrics   []customMetricRow `json:"custom_metrics"`
	}

	var d detail
	var completedAt sql.NullTime
	var execMs sql.NullInt64
	err := h.DB.QueryRowContext(r.Context(), `
		SELECT id, function_id, started_at, completed_at, success, output, error,
		       execution_ms, peak_memory_bytes, host_calls, output_size_bytes, trace_id
		FROM function_invocations WHERE id = ?`, invID).
		Scan(&d.ID, &d.FunctionID, &d.StartedAt, &completedAt,
			&d.Success, &d.Output, &d.Error,
			&execMs, &d.PeakMemoryBytes, &d.HostCalls, &d.OutputSizeBytes, &d.TraceID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "invocation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if completedAt.Valid {
		d.CompletedAt = &completedAt.Time
	}
	if execMs.Valid {
		d.ExecutionMs = &execMs.Int64
	}

	// Fetch custom metrics for this invocation.
	d.CustomMetrics = []customMetricRow{}
	mRows, err := h.DB.QueryContext(r.Context(), `
		SELECT id, trace_id, invocation_id, metric_name, metric_value, tags, recorded_at
		FROM function_custom_metrics WHERE invocation_id = ?
		ORDER BY recorded_at ASC`, invID)
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var m customMetricRow
			if err := mRows.Scan(&m.ID, &m.TraceID, &m.InvocationID,
				&m.MetricName, &m.MetricValue, &m.Tags, &m.RecordedAt); err == nil {
				if m.Tags == "" {
					m.Tags = "{}"
				}
				d.CustomMetrics = append(d.CustomMetrics, m)
			}
		}
	}

	writeJSON(w, http.StatusOK, d)
}

// ── helper: parse JSON safely in handlers ─────────────────────────────────────

func decodeBody(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}
