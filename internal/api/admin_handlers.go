package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"

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

// AdminUpdateUserRole changes a user's role.
func (h *AdminHandlers) AdminUpdateUserRole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.IAM.UpdateUserRole(id, req.Role); err != nil {
		switch {
		case errors.Is(err, iam.ErrInvalidRole):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, iam.ErrUserNotFound):
			writeError(w, http.StatusNotFound, "user not found")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminListGroups returns all groups with member counts.
func (h *AdminHandlers) AdminListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.IAM.ListGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if groups == nil {
		groups = []*iam.Group{}
	}
	writeJSON(w, http.StatusOK, groups)
}

// AdminCreateGroup creates a new group.
func (h *AdminHandlers) AdminCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	g, err := h.IAM.CreateGroup(req.Name, req.Description)
	if err != nil {
		if errors.Is(err, iam.ErrGroupExists) {
			writeError(w, http.StatusConflict, "group already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// AdminDeleteGroup removes a group.
func (h *AdminHandlers) AdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.IAM.DeleteGroup(id); err != nil {
		if errors.Is(err, iam.ErrGroupNotFound) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminListGroupMembers returns users in a group.
func (h *AdminHandlers) AdminListGroupMembers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	users, err := h.IAM.ListGroupMembers(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []*iam.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

// AdminAddGroupMember adds a user to a group.
func (h *AdminHandlers) AdminAddGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if err := h.IAM.AddUserToGroup(req.UserID, groupID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminRemoveGroupMember removes a user from a group.
func (h *AdminHandlers) AdminRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	userID := chi.URLParam(r, "userId")
	if err := h.IAM.RemoveUserFromGroup(userID, groupID); err != nil {
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

// AdminListObjects returns all object names in a bucket.
func (h *AdminHandlers) AdminListObjects(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	objects, err := h.Store.ListObjects(r.Context(), bucket)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, objects)
}

// AdminUploadObject accepts a multipart/form-data upload and stores it in the bucket.
// The form field must be named "file"; the object is stored under the original filename.
func (h *AdminHandlers) AdminUploadObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "parsing multipart form: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	objectName := filepath.Base(header.Filename)
	if objectName == "" || objectName == "." {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	if err := h.Store.PutObject(r.Context(), bucket, objectName, file); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": objectName, "bucket": bucket})
}

// AdminGetObject streams an object from a bucket to the client as a download.
func (h *AdminHandlers) AdminGetObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	object := chi.URLParam(r, "object")

	rc, err := h.Store.GetObject(r.Context(), bucket, object)
	if err != nil {
		writeError(w, http.StatusNotFound, "object not found")
		return
	}
	defer rc.Close()

	ct := mime.TypeByExtension(filepath.Ext(object))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+object+`"`)
	_, _ = io.Copy(w, rc)
}

// AdminDeleteObject removes a single object from a bucket.
func (h *AdminHandlers) AdminDeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	object := chi.URLParam(r, "object")
	if err := h.Store.DeleteObject(r.Context(), bucket, object); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
