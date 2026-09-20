package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/go-chi/chi/v5"
)

// --- Row Data ---

func (h *SchemaHandler) GetRows(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}

	opts, err := buildRowQueryOpts(r.URL.Query())
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}

	tableName := chi.URLParam(r, "tableName")
	result, err := h.introspector.GetRows(r.Context(), db, schemaParam(r), tableName, opts)
	if err != nil {
		schemaError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

// buildRowQueryOpts parses pagination, sort, and filter query parameters.
func buildRowQueryOpts(q url.Values) (schema.RowQueryOpts, error) {
	opts := schema.RowQueryOpts{
		Sort:  q.Get("sort"),
		Order: q.Get("order"),
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return opts, errors.New("invalid limit: must be an integer")
		}
		opts.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return opts, errors.New("invalid offset: must be an integer")
		}
		opts.Offset = n
	}

	opts.Filters = parseRowFilters(q)
	return opts, nil
}

// parseRowFilters extracts filter[column]=operator:value params.
func parseRowFilters(q url.Values) []schema.RowFilter {
	var filters []schema.RowFilter
	for key, values := range q {
		if len(key) <= 7 || key[:7] != "filter[" || key[len(key)-1] != ']' {
			continue
		}
		col := key[7 : len(key)-1]
		for _, v := range values {
			f := schema.RowFilter{Column: col}
			if idx := indexOfByte(v, ':'); idx >= 0 {
				f.Operator = v[:idx]
				f.Value = v[idx+1:]
			} else {
				f.Operator = v
			}
			filters = append(filters, f)
		}
	}
	return filters
}

func (h *SchemaHandler) InsertRow(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}

	var body struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if len(body.Data) == 0 {
		httpError(w, "data is required", http.StatusBadRequest)
		return
	}

	tableName := chi.URLParam(r, "tableName")
	result, err := h.introspector.InsertRow(r.Context(), db, schemaParam(r), tableName, body.Data)
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(result)
}

func (h *SchemaHandler) UpdateRow(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}

	var body struct {
		PK struct {
			Column string `json:"column"`
			Value  string `json:"value"`
		} `json:"pk"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if body.PK.Column == "" || body.PK.Value == "" {
		httpError(w, "pk.column and pk.value are required", http.StatusBadRequest)
		return
	}
	if len(body.Data) == 0 {
		httpError(w, "data is required", http.StatusBadRequest)
		return
	}

	tableName := chi.URLParam(r, "tableName")
	if err := h.introspector.UpdateRow(r.Context(), db, schemaParam(r), tableName, body.PK.Column, body.PK.Value, body.Data); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *SchemaHandler) DeleteRow(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}

	var body struct {
		PK struct {
			Column string `json:"column"`
			Value  string `json:"value"`
		} `json:"pk"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if body.PK.Column == "" || body.PK.Value == "" {
		httpError(w, "pk.column and pk.value are required", http.StatusBadRequest)
		return
	}

	tableName := chi.URLParam(r, "tableName")
	if err := h.introspector.DeleteRow(r.Context(), db, schemaParam(r), tableName, body.PK.Column, body.PK.Value); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}
