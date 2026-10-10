package routepolicy

import (
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
)

// lookupRouter matches a request to its row with chi's own matcher, so a
// path resolves to exactly the pattern the real router mounted it under.
var (
	lookupOnce   sync.Once
	lookupRouter *chi.Mux
	lookupIndex  map[Key]Row
)

func buildLookup() {
	index, err := Index()
	if err != nil {
		panic(err)
	}
	router := chi.NewRouter()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for key := range index {
		router.Method(key.Method, key.Pattern, noop)
	}
	lookupRouter, lookupIndex = router, index
}

// Lookup finds the row of the route a request reaches. Unknown routes are
// reported as such; a caller deciding access treats that as a refusal.
func Lookup(method, path string) (Row, bool) {
	lookupOnce.Do(buildLookup)
	rctx := chi.NewRouteContext()
	if !lookupRouter.Match(rctx, method, path) {
		return Row{}, false
	}
	row, ok := lookupIndex[Key{Method: method, Pattern: rctx.RoutePattern()}]
	return row, ok
}
