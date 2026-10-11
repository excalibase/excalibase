package routepolicy

import (
	"context"
	"net/http"
	"net/url"
	"sync"

	"github.com/go-chi/chi/v5"
)

// lookupRouter matches a request to its row with chi's own matcher, so a
// path resolves to exactly the route the real router mounted it under. Each
// route's handler names its own row: chi's RoutePattern drops a trailing
// slash, so the pattern it reports cannot be used as the key.
var (
	lookupOnce   sync.Once
	lookupRouter *chi.Mux
)

type matchedRowKey struct{}

func buildLookup() {
	index, err := Index()
	if err != nil {
		panic(err)
	}
	router := chi.NewRouter()
	for key, row := range index {
		router.Method(key.Method, key.Pattern, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			if matched, ok := r.Context().Value(matchedRowKey{}).(*Row); ok {
				*matched = row
			}
		}))
	}
	lookupRouter = router
}

// Lookup finds the row of the route a request reaches. Unknown routes are
// reported as such; a caller deciding access treats that as a refusal.
func Lookup(method, path string) (Row, bool) {
	lookupOnce.Do(buildLookup)
	var matched Row
	ctx := context.WithValue(context.Background(), matchedRowKey{}, &matched)
	request := (&http.Request{Method: method, URL: &url.URL{Path: path}, Header: http.Header{}}).WithContext(ctx)
	lookupRouter.ServeHTTP(discardResponse{}, request)
	return matched, matched.Pattern != ""
}

// discardResponse swallows what chi writes for an unmatched route.
type discardResponse struct{}

func (discardResponse) Header() http.Header         { return http.Header{} }
func (discardResponse) Write(b []byte) (int, error) { return len(b), nil }
func (discardResponse) WriteHeader(int)             {}
