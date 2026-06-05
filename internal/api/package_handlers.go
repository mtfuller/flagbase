package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/packages"
)

// PackageHandlers manages the package registry API.
type PackageHandlers struct {
	Packages *packages.Store
}

func (h *PackageHandlers) ListPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := h.Packages.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pkgs)
}

func (h *PackageHandlers) RequestPackage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	claims, _ := r.Context().Value(iam.UserContextKey).(*iam.Claims)
	requesterID := ""
	if claims != nil {
		requesterID = claims.UserID
	}

	pkg, err := h.Packages.Request(r.Context(), req.Name, req.Version, requesterID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, pkg)
}

func (h *PackageHandlers) ApprovePackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	claims, _ := r.Context().Value(iam.UserContextKey).(*iam.Claims)
	approverID := ""
	if claims != nil {
		approverID = claims.UserID
	}

	pkg, err := h.Packages.Approve(r.Context(), id, approverID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (h *PackageHandlers) DeletePackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Packages.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
