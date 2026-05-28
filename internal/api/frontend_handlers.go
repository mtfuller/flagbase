package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/frontend"
)

// FrontendHandlers handles frontend management and static file serving.
type FrontendHandlers struct {
	Frontends *frontend.Service
}

// ListFrontends returns all frontends.
func (h *FrontendHandlers) ListFrontends(w http.ResponseWriter, r *http.Request) {
	items, err := h.Frontends.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// CreateFrontend creates a new frontend.
func (h *FrontendHandlers) CreateFrontend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Slug == "" {
		writeError(w, http.StatusBadRequest, "name and slug are required")
		return
	}
	if !isValidSlug(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must contain only lowercase letters, numbers, and hyphens")
		return
	}
	f, err := h.Frontends.Create(r.Context(), req.Name, req.Slug, req.Description)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

// GetFrontend returns a frontend by ID.
func (h *FrontendHandlers) GetFrontend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	f, err := h.Frontends.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if f == nil {
		writeError(w, http.StatusNotFound, "frontend not found")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// UpdateFrontend updates the name and description of a frontend.
func (h *FrontendHandlers) UpdateFrontend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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
	f, err := h.Frontends.Update(r.Context(), id, req.Name, req.Description)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if f == nil {
		writeError(w, http.StatusNotFound, "frontend not found")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// DeleteFrontend removes a frontend and all its versions.
func (h *FrontendHandlers) DeleteFrontend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Frontends.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListVersions returns all versions for a frontend.
func (h *FrontendHandlers) ListVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	versions, err := h.Frontends.ListVersions(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, versions)
}

// CreateVersion accepts a multipart/form-data upload with a ZIP file and creates a new version.
// Form fields: "file" (required, ZIP archive), "label" (required), "description" (optional).
func (h *FrontendHandlers) CreateVersion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	f, err := h.Frontends.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if f == nil {
		writeError(w, http.StatusNotFound, "frontend not found")
		return
	}

	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "parsing multipart form: "+err.Error())
		return
	}

	label := r.FormValue("label")
	if label == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	description := r.FormValue("description")

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file field is required (ZIP archive)")
		return
	}
	defer file.Close()

	zipData, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reading upload: "+err.Error())
		return
	}

	v, err := h.Frontends.CreateVersion(r.Context(), id, label, description, zipData)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// GetVersion returns a single version by ID.
func (h *FrontendHandlers) GetVersion(w http.ResponseWriter, r *http.Request) {
	versionID := chi.URLParam(r, "versionId")
	v, err := h.Frontends.GetVersion(r.Context(), versionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "version not found")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// DeleteVersion removes a version and its files.
func (h *FrontendHandlers) DeleteVersion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	versionID := chi.URLParam(r, "versionId")
	if err := h.Frontends.DeleteVersion(r.Context(), id, versionID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ActivateVersion sets a version as the active one for a frontend.
func (h *FrontendHandlers) ActivateVersion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	versionID := chi.URLParam(r, "versionId")

	v, err := h.Frontends.GetVersion(r.Context(), versionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if v == nil || v.FrontendID != id {
		writeError(w, http.StatusNotFound, "version not found")
		return
	}

	f, err := h.Frontends.ActivateVersion(r.Context(), id, versionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// ServeFrontend serves static files from the active version of a frontend.
// The URL pattern is /frontends/{slug}/* and files are resolved relative to the active version directory.
func (h *FrontendHandlers) ServeFrontend(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	f, err := h.Frontends.GetBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if f == nil {
		http.NotFound(w, r)
		return
	}
	if f.ActiveVersionID == "" {
		http.Error(w, "no active version", http.StatusServiceUnavailable)
		return
	}

	dir := h.Frontends.VersionDir(f.ID, f.ActiveVersionID)
	prefix := "/frontends/" + slug
	http.StripPrefix(prefix, http.FileServer(http.Dir(dir))).ServeHTTP(w, r)
}

// isValidSlug returns true if s contains only lowercase letters, digits, and hyphens,
// and does not start or end with a hyphen.
func isValidSlug(s string) bool {
	if len(s) == 0 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

