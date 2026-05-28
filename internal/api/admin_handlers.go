package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/admin"
	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/storage"
)

// AdminHandlers groups admin-console–specific HTTP handlers.
type AdminHandlers struct {
	IAM   *iam.Service
	Setup *admin.SetupManager
	Store *storage.LocalAdapter
	DB    *sql.DB
}

// SetupStatus reports whether first-time admin setup is still pending.
func (h *AdminHandlers) SetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": h.Setup.IsActive()})
}

// CompleteSetup creates the first admin account using the one-time setup token.
func (h *AdminHandlers) CompleteSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "token, email and password are required")
		return
	}
	if !h.Setup.Consume(req.Token) {
		writeError(w, http.StatusUnauthorized, "invalid or expired setup token")
		return
	}
	user, err := h.IAM.Register(req.Email, req.Password, "admin", "default")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := h.IAM.IssueToken(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": token})
}

// AdminListUsers returns all registered users.
func (h *AdminHandlers) AdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.IAM.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []*iam.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

// AdminDeleteUser removes a user by ID.
func (h *AdminHandlers) AdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.IAM.DeleteUser(id); err != nil {
		if errors.Is(err, iam.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type metricSummary struct {
	FlagKey   string  `json:"flag_key"`
	EventType string  `json:"event_type"`
	Count     int     `json:"count"`
	Total     float64 `json:"total"`
}

// AdminMetricsSummary returns aggregated metric counts per flag and event type.
func (h *AdminHandlers) AdminMetricsSummary(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(), `
		SELECT flag_key, event_type, COUNT(*) AS count, COALESCE(SUM(value),0) AS total
		FROM metrics
		GROUP BY flag_key, event_type
		ORDER BY flag_key, event_type
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	summaries := []metricSummary{}
	for rows.Next() {
		var s metricSummary
		if err := rows.Scan(&s.FlagKey, &s.EventType, &s.Count, &s.Total); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		summaries = append(summaries, s)
	}
	writeJSON(w, http.StatusOK, summaries)
}

// AdminListBuckets returns all storage bucket names.
func (h *AdminHandlers) AdminListBuckets(w http.ResponseWriter, r *http.Request) {
	buckets, err := h.Store.ListBuckets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buckets)
}

// AdminCreateBucket creates a new storage bucket.
func (h *AdminHandlers) AdminCreateBucket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.Store.CreateBucket(r.Context(), req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": req.Name})
}

// AdminDeleteBucket removes a storage bucket and all its objects.
func (h *AdminHandlers) AdminDeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	if err := h.Store.DeleteBucket(r.Context(), bucket); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
