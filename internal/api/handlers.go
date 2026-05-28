package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/iam"
)

// Handlers groups all HTTP handler methods.
type Handlers struct {
	IAM     *iam.Service
	Feature *feature.Engine
}

// --- Auth ---

func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	user, err := h.IAM.Register(req.Email, req.Password, req.Role, "default")
	if err != nil {
		if errors.Is(err, iam.ErrUserExists) {
			writeError(w, http.StatusConflict, "user already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	token, err := h.IAM.Login(req.Email, req.Password)
	if err != nil {
		if errors.Is(err, iam.ErrUserNotFound) || errors.Is(err, iam.ErrInvalidPassword) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// --- Feature flags ---

func (h *Handlers) ListFlags(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.Feature.ListFlags())
}

func (h *Handlers) GetFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	f, ok := h.Feature.GetFlag(key)
	if !ok {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *Handlers) CreateFlag(w http.ResponseWriter, r *http.Request) {
	var f feature.Flag
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.Feature.CreateFlag(&f); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (h *Handlers) UpdateFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var f feature.Flag
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	f.Key = key
	if err := h.Feature.UpdateFlag(&f); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *Handlers) DeleteFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if err := h.Feature.DeleteFlag(key); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EvaluateFlag resolves the current value of a flag for the authenticated caller.
func (h *Handlers) EvaluateFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	evalCtx := map[string]interface{}{
		"userId": "anonymous",
		"role":   "guest",
	}
	if claims, ok := r.Context().Value(iam.UserContextKey).(*iam.Claims); ok {
		evalCtx["userId"] = claims.UserID
		evalCtx["role"] = claims.Role
	}

	writeJSON(w, http.StatusOK, map[string]bool{"value": h.Feature.EvaluateBool(key, evalCtx)})
}

// Health returns a simple liveness probe.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
