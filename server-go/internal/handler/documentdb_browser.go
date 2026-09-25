package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/docbrowser"
	"github.com/go-chi/chi/v5"
)

// maxDocumentBody bounds a document or index body Studio sends.
const maxDocumentBody = 4 << 20

// DocumentBrowserAPI is Studio's document browser for DocumentDB projects.
type DocumentBrowserAPI interface {
	ListDatabases(ctx context.Context, projectID string) ([]string, error)
	ListCollections(ctx context.Context, projectID, database string) ([]docbrowser.Collection, error)
	CreateCollection(ctx context.Context, projectID string, ns docbrowser.Namespace) error
	DropCollection(ctx context.Context, projectID string, ns docbrowser.Namespace) error
	Find(ctx context.Context, projectID string, ns docbrowser.Namespace, req docbrowser.FindRequest) (docbrowser.Page, error)
	Count(ctx context.Context, projectID string, ns docbrowser.Namespace, filter string) (int64, error)
	Insert(ctx context.Context, projectID string, ns docbrowser.Namespace, body []byte) (json.RawMessage, error)
	Replace(ctx context.Context, projectID string, ns docbrowser.Namespace, id string, body []byte) error
	Update(ctx context.Context, projectID string, ns docbrowser.Namespace, id string, body []byte) error
	Delete(ctx context.Context, projectID string, ns docbrowser.Namespace, id string) error
	ListIndexes(ctx context.Context, projectID string, ns docbrowser.Namespace) ([]json.RawMessage, error)
	CreateIndex(ctx context.Context, projectID string, ns docbrowser.Namespace, body []byte) (string, error)
	DropIndex(ctx context.Context, projectID string, ns docbrowser.Namespace, name string) error
	Sample(ctx context.Context, projectID string, ns docbrowser.Namespace) ([]json.RawMessage, error)
}

type DocumentBrowserHandler struct {
	api DocumentBrowserAPI
}

func NewDocumentBrowserHandler(api DocumentBrowserAPI) *DocumentBrowserHandler {
	return &DocumentBrowserHandler{api: api}
}

func (h *DocumentBrowserHandler) Routes(r chi.Router) {
	r.Get("/databases", h.listDatabases)
	r.Get("/databases/{database}/collections", h.listCollections)
	r.Post("/databases/{database}/collections", h.createCollection)
	r.Route("/databases/{database}/collections/{collection}", func(r chi.Router) {
		r.Delete("/", h.dropCollection)
		r.Get("/documents", h.find)
		r.Post("/documents", h.insert)
		r.Put("/documents", h.replace)
		r.Patch("/documents", h.update)
		r.Delete("/documents", h.deleteDocument)
		r.Get("/count", h.count)
		r.Get("/sample", h.sample)
		r.Get("/indexes", h.listIndexes)
		r.Post("/indexes", h.createIndex)
		r.Delete("/indexes/{index}", h.dropIndex)
	})
}

// pathParam reads a route parameter. chi routes on the raw path when the
// request carried one, and then hands the parameter over still escaped.
func pathParam(r *http.Request, name string) (string, bool) {
	value := chi.URLParam(r, name)
	if r.URL.RawPath == "" {
		return value, true
	}
	unescaped, err := url.PathUnescape(value)
	return unescaped, err == nil
}

func browserProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	return projectID, true
}

func browserNamespace(w http.ResponseWriter, r *http.Request) (string, docbrowser.Namespace, bool) {
	projectID, ok := browserProject(w, r)
	if !ok {
		return "", docbrowser.Namespace{}, false
	}
	database, okDB := pathParam(r, "database")
	collection, okColl := pathParam(r, "collection")
	if !okDB || !okColl {
		httpError(w, "invalid database or collection name", http.StatusBadRequest)
		return "", docbrowser.Namespace{}, false
	}
	return projectID, docbrowser.Namespace{Database: database, Collection: collection}, true
}

func readBrowserBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDocumentBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		httpError(w, "the document is too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// writeBrowserError answers with what the caller may know. Anything the
// browser does not classify is logged and reported without detail.
func writeBrowserError(w http.ResponseWriter, err error) {
	status := browserErrorStatus(err)
	if status == http.StatusInternalServerError || status == http.StatusBadGateway {
		log.Printf("document browser: %s", safeLog(err.Error()))
	}
	httpError(w, docbrowser.PublicMessage(err), status)
}

func browserErrorStatus(err error) int {
	var queryErr *docbrowser.QueryError
	if errors.As(err, &queryErr) {
		if queryErr.Conflict {
			return http.StatusConflict
		}
		return http.StatusBadRequest
	}
	statuses := []struct {
		err    error
		status int
	}{
		{docbrowser.ErrInvalid, http.StatusBadRequest},
		{docbrowser.ErrProjectNotFound, http.StatusNotFound},
		{docbrowser.ErrNotDocumentDB, http.StatusNotFound},
		{docbrowser.ErrDocumentNotFound, http.StatusNotFound},
		{docbrowser.ErrNotServable, http.StatusConflict},
		{docbrowser.ErrGatewayNotReady, http.StatusServiceUnavailable},
		{docbrowser.ErrTimeout, http.StatusGatewayTimeout},
		{docbrowser.ErrUnavailable, http.StatusBadGateway},
	}
	for _, candidate := range statuses {
		if errors.Is(err, candidate.err) {
			return candidate.status
		}
	}
	return http.StatusInternalServerError
}

func writeBrowserResult(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		writeBrowserError(w, err)
		return
	}
	if v == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if encodeErr := json.NewEncoder(w).Encode(v); encodeErr != nil {
		log.Printf("document browser: encode response: %v", encodeErr)
	}
}

func (h *DocumentBrowserHandler) listDatabases(w http.ResponseWriter, r *http.Request) {
	projectID, ok := browserProject(w, r)
	if !ok {
		return
	}
	databases, err := h.api.ListDatabases(r.Context(), projectID)
	writeBrowserResult(w, http.StatusOK, map[string]any{"databases": databases}, err)
}

func (h *DocumentBrowserHandler) listCollections(w http.ResponseWriter, r *http.Request) {
	projectID, ok := browserProject(w, r)
	database, okDB := pathParam(r, "database")
	if !ok {
		return
	}
	if !okDB {
		httpError(w, "invalid database name", http.StatusBadRequest)
		return
	}
	collections, err := h.api.ListCollections(r.Context(), projectID, database)
	writeBrowserResult(w, http.StatusOK, map[string]any{"collections": collections}, err)
}

func (h *DocumentBrowserHandler) createCollection(w http.ResponseWriter, r *http.Request) {
	projectID, ok := browserProject(w, r)
	database, okDB := pathParam(r, "database")
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || !okDB {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	ns := docbrowser.Namespace{Database: database, Collection: body.Name}
	err := h.api.CreateCollection(r.Context(), projectID, ns)
	writeBrowserResult(w, http.StatusCreated, map[string]string{"name": body.Name}, err)
}

func (h *DocumentBrowserHandler) dropCollection(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	writeBrowserResult(w, http.StatusNoContent, nil, h.api.DropCollection(r.Context(), projectID, ns))
}

func queryInt(r *http.Request, name string) (int64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil
}

func (h *DocumentBrowserHandler) find(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	limit, okLimit := queryInt(r, "limit")
	skip, okSkip := queryInt(r, "skip")
	if !okLimit || !okSkip {
		httpError(w, "limit and skip must be whole numbers", http.StatusBadRequest)
		return
	}
	query := r.URL.Query()
	page, err := h.api.Find(r.Context(), projectID, ns, docbrowser.FindRequest{
		Filter: query.Get("filter"), Sort: query.Get("sort"), Projection: query.Get("projection"),
		Limit: limit, Skip: skip,
	})
	writeBrowserResult(w, http.StatusOK, page, err)
}

func (h *DocumentBrowserHandler) count(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	count, err := h.api.Count(r.Context(), projectID, ns, r.URL.Query().Get("filter"))
	writeBrowserResult(w, http.StatusOK, map[string]int64{"count": count}, err)
}

func (h *DocumentBrowserHandler) insert(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	body, ok := readBrowserBody(w, r)
	if !ok {
		return
	}
	id, err := h.api.Insert(r.Context(), projectID, ns, body)
	writeBrowserResult(w, http.StatusCreated, map[string]json.RawMessage{"insertedId": id}, err)
}

func (h *DocumentBrowserHandler) replace(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	body, ok := readBrowserBody(w, r)
	if !ok {
		return
	}
	err := h.api.Replace(r.Context(), projectID, ns, r.URL.Query().Get("id"), body)
	writeBrowserResult(w, http.StatusNoContent, nil, err)
}

func (h *DocumentBrowserHandler) update(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	body, ok := readBrowserBody(w, r)
	if !ok {
		return
	}
	err := h.api.Update(r.Context(), projectID, ns, r.URL.Query().Get("id"), body)
	writeBrowserResult(w, http.StatusNoContent, nil, err)
}

func (h *DocumentBrowserHandler) deleteDocument(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	err := h.api.Delete(r.Context(), projectID, ns, r.URL.Query().Get("id"))
	writeBrowserResult(w, http.StatusNoContent, nil, err)
}

func (h *DocumentBrowserHandler) sample(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	docs, err := h.api.Sample(r.Context(), projectID, ns)
	writeBrowserResult(w, http.StatusOK, map[string]any{"documents": docs}, err)
}

func (h *DocumentBrowserHandler) listIndexes(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	indexes, err := h.api.ListIndexes(r.Context(), projectID, ns)
	writeBrowserResult(w, http.StatusOK, map[string]any{"indexes": indexes}, err)
}

func (h *DocumentBrowserHandler) createIndex(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	if !ok {
		return
	}
	body, ok := readBrowserBody(w, r)
	if !ok {
		return
	}
	name, err := h.api.CreateIndex(r.Context(), projectID, ns, body)
	writeBrowserResult(w, http.StatusCreated, map[string]string{"name": name}, err)
}

func (h *DocumentBrowserHandler) dropIndex(w http.ResponseWriter, r *http.Request) {
	projectID, ns, ok := browserNamespace(w, r)
	name, okName := pathParam(r, "index")
	if !ok {
		return
	}
	if !okName {
		httpError(w, "invalid index name", http.StatusBadRequest)
		return
	}
	writeBrowserResult(w, http.StatusNoContent, nil, h.api.DropIndex(r.Context(), projectID, ns, name))
}
