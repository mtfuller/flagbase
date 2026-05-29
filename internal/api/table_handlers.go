package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/table"
)

// TableHandlers groups HTTP handlers for the Tables resource.
type TableHandlers struct {
	Tables *table.Engine
}

// ---- table schema endpoints ----

func (h *TableHandlers) ListTables(w http.ResponseWriter, r *http.Request) {
	defs, err := h.Tables.ListTables()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, defs)
}

func (h *TableHandlers) CreateTable(w http.ResponseWriter, r *http.Request) {
	var def table.TableDef
	if err := json.NewDecoder(r.Body).Decode(&def); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if def.Key == "" || def.Name == "" {
		writeError(w, http.StatusBadRequest, "key and name are required")
		return
	}
	if err := h.Tables.CreateTable(&def); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, def)
}

func (h *TableHandlers) GetTable(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	def, err := h.Tables.GetTable(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if def == nil {
		writeError(w, http.StatusNotFound, "table not found")
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// AddColumns handles schema evolution: only new columns may be added.
func (h *TableHandlers) AddColumns(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var req struct {
		Columns []table.Column `json:"columns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Columns) == 0 {
		writeError(w, http.StatusBadRequest, "columns array must not be empty")
		return
	}
	if err := h.Tables.AddColumns(key, req.Columns); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	def, err := h.Tables.GetTable(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, def)
}

func (h *TableHandlers) DeleteTable(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if err := h.Tables.DeleteTable(key); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- record endpoints ----

func (h *TableHandlers) ListRecords(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var opts table.QueryOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		// Body is optional for list — use default empty opts on decode error.
		opts = table.QueryOptions{}
	}
	records, err := h.Tables.QueryRecords(key, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (h *TableHandlers) CreateRecord(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rec, err := h.Tables.InsertRecord(key, data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (h *TableHandlers) GetRecord(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	id := chi.URLParam(r, "id")
	rec, err := h.Tables.GetRecord(key, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "record not found")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *TableHandlers) UpdateRecord(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	id := chi.URLParam(r, "id")
	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rec, err := h.Tables.UpdateRecord(key, id, data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "record not found")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *TableHandlers) DeleteRecord(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	id := chi.URLParam(r, "id")
	if err := h.Tables.DeleteRecord(key, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
