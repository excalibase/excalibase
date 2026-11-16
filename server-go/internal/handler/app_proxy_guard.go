package handler

import (
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// proxySubject describes an app as a request would leave it, for the proxy guard.
func proxySubject(r *http.Request, app *apphost.App, surface string) abuse.AppSubject {
	names := make([]string, 0, len(app.Env))
	for _, variable := range app.Env {
		names = append(names, variable.Name)
	}
	return abuse.AppSubject{
		ProjectID: app.ProjectID, AppID: app.ID, Name: app.Name, Image: app.Image,
		EnvNames: names, Actor: actorID(r), Surface: surface,
	}
}

// refuseProxy answers 403 and reports true when the guard takes the app for a proxy node (EXC-755).
func refuseProxy(w http.ResponseWriter, r *http.Request, guard *abuse.AppGuard, app *apphost.App, surface string) bool {
	err := guard.Check(r.Context(), proxySubject(r, app, surface))
	if err == nil {
		return false
	}
	if !errors.Is(err, abuse.ErrProxyApp) {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return true
	}
	httpError(w, err.Error(), http.StatusForbidden)
	return true
}

// SetProxyGuard refuses apps that are proxy or VPN nodes (EXC-755); nil allows all.
func (h *AppHandler) SetProxyGuard(guard *abuse.AppGuard) { h.proxyGuard = guard }

// SetProxyGuard refuses a secret that would complete a proxy node's environment (EXC-755).
func (h *AppSecretHandler) SetProxyGuard(guard *abuse.AppGuard) { h.proxyGuard = guard }
