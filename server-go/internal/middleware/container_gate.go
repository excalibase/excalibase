package middleware

import (
	"net/http"
	"regexp"

	"github.com/excalibase/provisioning-poc/internal/trust"
	"github.com/go-chi/chi/v5"
)

// An app route is a container route unless it is listed here: a route added
// under /apps/ is closed to a limited account until someone decides it
// starts nothing (EXC-899). Reads and DELETE never start a container.
var appsMayWrite = regexp.MustCompile(
	`^/api/projects/[^/]+/apps/[^/]+/(pause|disk|secrets/[^/]+|domains(/.*)?|git/sign-in)/?$`)

// Writes that run tenant code in a container but sit outside /apps/: a
// template deploy makes apps, a site build runs in a build Job, and a function
// deploy creates or rolls the project's own Deno runtime pod.
var containerWrites = regexp.MustCompile(
	`^/api/projects/[^/]+/(app-templates/[^/]+/deploy|sites/[^/]+/(builds|git)|functions)/?$`)

var appsFamily = regexp.MustCompile(`^/api/projects/[^/]+/apps(/|$)`)

// ContainerWrite reports whether a project write starts, builds or scales a
// container, which a limited (unverified) account may not do. path is the raw
// (escaped) path, the one the router matches: a %2F inside a segment must not
// read as a separator here and as part of an id there.
func ContainerWrite(method, path string) bool {
	if isSafeMethod(method) || method == http.MethodDelete {
		return false
	}
	if containerWrites.MatchString(path) {
		return true
	}
	return appsFamily.MatchString(path) && !appsMayWrite.MatchString(path)
}

// RequireVerifiedForContainers answers 403 trust_required to a container
// write on a project whose owner is limited. It sits on every project mount
// beside RefuseSuspendedWrites; a nil gate allows everything. The service
// layer checks again for what no route reaches (webhooks, the image watcher).
func RequireVerifiedForContainers(gate *trust.Gate) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			projectID := chi.URLParam(r, "projectId")
			if projectID == "" || !ContainerWrite(r.Method, r.URL.EscapedPath()) {
				next.ServeHTTP(w, r)
				return
			}
			if err := gate.CheckProject(r.Context(), projectID); err != nil {
				trust.WriteRefusal(w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
