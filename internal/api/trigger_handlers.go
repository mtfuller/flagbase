package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/trigger"
)

// TriggerHandlers handles function trigger management and HTTP trigger invocation.
type TriggerHandlers struct {
	Triggers *trigger.Engine
}

// ListTriggers returns all triggers.
func (h *TriggerHandlers) ListTriggers(w http.ResponseWriter, r *http.Request) {
	triggers, err := h.Triggers.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, triggers)
}

// ListFunctionTriggers returns all triggers for a specific function.
func (h *TriggerHandlers) ListFunctionTriggers(w http.ResponseWriter, r *http.Request) {
	fnID := chi.URLParam(r, "id")
	triggers, err := h.Triggers.ListByFunction(r.Context(), fnID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, triggers)
}

// CreateTrigger creates a new trigger.
func (h *TriggerHandlers) CreateTrigger(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FunctionID string                 `json:"function_id"`
		EventType  string                 `json:"event_type"`
		Config     map[string]interface{} `json:"config"`
		Enabled    bool                   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.FunctionID == "" {
		writeError(w, http.StatusBadRequest, "function_id is required")
		return
	}
	if body.EventType == "" {
		writeError(w, http.StatusBadRequest, "event_type is required")
		return
	}
	if body.Config == nil {
		body.Config = map[string]interface{}{}
	}
	t, err := h.Triggers.Create(r.Context(), body.FunctionID, body.EventType, body.Config, body.Enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// GetTrigger returns a single trigger by ID.
func (h *TriggerHandlers) GetTrigger(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := h.Triggers.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// UpdateTrigger updates the enabled flag and config of an existing trigger.
func (h *TriggerHandlers) UpdateTrigger(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Enabled bool                   `json:"enabled"`
		Config  map[string]interface{} `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Config == nil {
		body.Config = map[string]interface{}{}
	}
	t, err := h.Triggers.Update(r.Context(), id, body.Enabled, body.Config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// DeleteTrigger removes a trigger.
func (h *TriggerHandlers) DeleteTrigger(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Triggers.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// InvokeHTTPTrigger fires an HTTP trigger synchronously and returns the function's output.
// Accepts GET and POST; POST body (if any) is forwarded as the event data payload.
func (h *TriggerHandlers) InvokeHTTPTrigger(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var eventData []byte
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
		if err != nil {
			writeError(w, http.StatusBadRequest, "reading request body: "+err.Error())
			return
		}
		if len(body) > 0 {
			eventData = body
		}
	}

	out, err := h.Triggers.ServeHTTPTrigger(r.Context(), id, eventData)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
