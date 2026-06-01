package tracing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type traceIDKey struct{}

// NewTraceID generates a random 16-byte hex identifier.
func NewTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

// WithTraceID injects a trace ID into the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceIDFromCtx extracts the trace ID from context, returning "" if absent.
func TraceIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(traceIDKey{}).(string)
	return v
}

// Recorder writes distributed trace data to SQLite.
type Recorder struct {
	db *sql.DB
}

// NewRecorder creates a Recorder backed by the given database.
func NewRecorder(db *sql.DB) *Recorder {
	return &Recorder{db: db}
}

// StartTrace creates a new trace record and returns a context enriched with
// the trace ID. The trace starts in "running" status.
func (r *Recorder) StartTrace(ctx context.Context, traceID, rootType, rootRef, userID, tenantID string) (context.Context, error) {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO traces (id, root_type, root_ref, user_id, tenant_id, status)
		VALUES (?, ?, ?, ?, ?, 'running')`,
		traceID, rootType, rootRef, userID, tenantID)
	if err != nil {
		return ctx, fmt.Errorf("tracing: start trace: %w", err)
	}
	return WithTraceID(ctx, traceID), nil
}

// CompleteTrace marks the trace finished with the given status ("success" or "error").
func (r *Recorder) CompleteTrace(traceID, status string) {
	_, _ = r.db.Exec(`
		UPDATE traces SET status = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, traceID)
}

// RecordSpan inserts a single trace_event row and returns its generated ID.
// durationMs is the span duration; metadata is marshalled to JSON.
func (r *Recorder) RecordSpan(
	traceID, parentSpanID, eventType, status string,
	startedAt time.Time, durationMs int64, seq int,
	metadata map[string]interface{},
) (string, error) {
	id := NewTraceID()
	metaJSON := "{}"
	if len(metadata) > 0 {
		if b, err := json.Marshal(metadata); err == nil {
			metaJSON = string(b)
		}
	}
	completedAt := startedAt.Add(time.Duration(durationMs) * time.Millisecond)
	_, err := r.db.Exec(`
		INSERT INTO trace_events
			(id, trace_id, parent_event_id, event_type, sequence, started_at, completed_at, duration_ms, status, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, traceID, nullStr(parentSpanID), eventType, seq,
		startedAt.UTC().Format(time.RFC3339Nano),
		completedAt.UTC().Format(time.RFC3339Nano),
		durationMs, status, metaJSON)
	if err != nil {
		return "", fmt.Errorf("tracing: record span: %w", err)
	}
	return id, nil
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
