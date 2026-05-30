package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/gateway"
	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/logger"
)

// MetricRecorder is the minimal surface the metrics handler needs from the worker.
type MetricRecorder interface {
	RecordMetric(flagKey, variant, eventType string, value float64) error
}

// Handlers groups all HTTP handler methods.
type Handlers struct {
	IAM     *iam.Service
	Feature *feature.Engine
	Metrics MetricRecorder
	Gateway *gateway.ProxyHandler
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
// Response includes both the boolean value and the resolved variant key.
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

	variant := h.Feature.EvaluateVariant(key, evalCtx)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"value":   variant != "false" && variant != "",
		"variant": variant,
	})
}

// TransitionFlagStatus moves a flag through its lifecycle.
func (h *Handlers) TransitionFlagStatus(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var req struct {
		Status feature.FlagStatus `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.Feature.TransitionStatus(key, req.Status); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, _ := h.Feature.GetFlag(key)
	writeJSON(w, http.StatusOK, f)
}

// --- Flag variants ---

func (h *Handlers) ListFlagVariants(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	writeJSON(w, http.StatusOK, h.Feature.ListVariants(key))
}

func (h *Handlers) CreateFlagVariant(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var v feature.Variant
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if v.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	v.FlagKey = key
	if err := h.Feature.CreateVariant(&v); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (h *Handlers) DeleteFlagVariant(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	variantKey := chi.URLParam(r, "variantKey")
	if err := h.Feature.DeleteVariant(key, variantKey); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Flag overrides ---

func (h *Handlers) ListFlagOverrides(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	writeJSON(w, http.StatusOK, h.Feature.ListOverrides(key))
}

// CreateFlagOverride pins a user to a variant, bypassing normal rule evaluation.
// Send {"user_id": "...", "variant_key": "true|false|<named-variant>"}.
func (h *Handlers) CreateFlagOverride(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var ov feature.Override
	if err := json.NewDecoder(r.Body).Decode(&ov); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if ov.UserID == "" || ov.VariantKey == "" {
		writeError(w, http.StatusBadRequest, "user_id and variant_key are required")
		return
	}
	ov.FlagKey = key
	if err := h.Feature.CreateOverride(&ov); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, ov)
}

func (h *Handlers) DeleteFlagOverride(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	userID := chi.URLParam(r, "userId")
	if err := h.Feature.DeleteOverride(key, userID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Health returns a simple liveness probe.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Metrics ---

// RecordMetric accepts a metric event from an SDK client and persists it.
func (h *Handlers) RecordMetric(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FlagKey   string  `json:"flag_key"`
		Variant   string  `json:"variant"`
		EventType string  `json:"event_type"`
		Value     float64 `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.FlagKey == "" || req.Variant == "" || req.EventType == "" {
		writeError(w, http.StatusBadRequest, "flag_key, variant and event_type are required")
		return
	}
	if err := h.Metrics.RecordMetric(req.FlagKey, req.Variant, req.EventType, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Gateway route management ---

// ListGatewayRoutes returns all currently registered gateway routes.
func (h *Handlers) ListGatewayRoutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.Gateway.ListRoutes())
}

// RegisterGatewayRoute adds or replaces a gateway route.
func (h *Handlers) RegisterGatewayRoute(w http.ResponseWriter, r *http.Request) {
	var route gateway.Route
	if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if route.Pattern == "" || route.BackendURL == "" {
		writeError(w, http.StatusBadRequest, "pattern and backend_url are required")
		return
	}
	h.Gateway.RegisterRoute(&route)
	writeJSON(w, http.StatusCreated, route)
}

// DeleteGatewayRoute removes a route by its ID.
func (h *Handlers) DeleteGatewayRoute(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.Gateway.RemoveRoute(id) {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

// writeJSON encodes v as JSON into a buffer before writing so that encoding
// failures can be reported as a proper 500 rather than a truncated response.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	buf, err := json.Marshal(v)
	if err != nil {
		logger.Error("writeJSON: marshal: %v", err)
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err = w.Write(buf); err != nil {
		logger.Error("writeJSON: write: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
