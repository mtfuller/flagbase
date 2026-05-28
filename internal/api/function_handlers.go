package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/function"
)

// FunctionHandlers handles browser-based function compilation and invocation.
type FunctionHandlers struct {
	Functions *function.Store
}

func (h *FunctionHandlers) ListFunctions(w http.ResponseWriter, r *http.Request) {
	fns, err := h.Functions.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fns)
}

func (h *FunctionHandlers) CreateFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Language    string `json:"language"`
		Source      string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Language == "" || req.Source == "" {
		writeError(w, http.StatusBadRequest, "name, language and source are required")
		return
	}

	fn, err := h.Functions.Create(r.Context(), req.Name, req.Description, req.Language, req.Source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, fn)
}

func (h *FunctionHandlers) GetFunction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	fn, err := h.Functions.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if fn == nil {
		writeError(w, http.StatusNotFound, "function not found")
		return
	}
	writeJSON(w, http.StatusOK, fn)
}

func (h *FunctionHandlers) DeleteFunction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Functions.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FunctionHandlers) InvokeFunction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.TimeoutSeconds <= 0 || req.TimeoutSeconds > 30 {
		req.TimeoutSeconds = 5
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	output, err := h.Functions.Invoke(r.Context(), id, timeout)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": string(output)})
}
