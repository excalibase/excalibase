package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/go-chi/chi/v5"
)

const autoDeployProject = "proj_autodeploy"

func createAutoDeployApp(t *testing.T, r chi.Router, image string) apphost.App {
	t.Helper()
	body := validAppBody()
	body["image"] = image
	delete(body, "env")
	rec := doAppRequest(t, r, http.MethodPost, "/api/projects/"+autoDeployProject+"/apps/", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var app apphost.App
	_ = json.Unmarshal(rec.Body.Bytes(), &app)
	return app
}

func TestAutoDeploy_IsSwitchedOnAndOffWithAnEdit(t *testing.T) {
	r, _ := setupAppRouter(t)
	app := createAutoDeployApp(t, r, "ghcr.io/acme/storefront:main")
	path := "/api/projects/" + autoDeployProject + "/apps/" + app.ID

	rec := doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"autoDeploy": true}, app.Version)
	if rec.Code != http.StatusOK {
		t.Fatalf("switch on: %d %s", rec.Code, rec.Body.String())
	}
	var got apphost.App
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.AutoDeploy || got.Image != app.Image {
		t.Fatalf("after switching on: %+v", got)
	}

	rec = doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"autoDeploy": false}, got.Version)
	var off apphost.App
	_ = json.Unmarshal(rec.Body.Bytes(), &off)
	if rec.Code != http.StatusOK || off.AutoDeploy {
		t.Fatalf("switch off: %d %+v", rec.Code, off)
	}
}

func TestAutoDeploy_RefusesAnImagePinnedByDigest(t *testing.T) {
	r, _ := setupAppRouter(t)
	app := createAutoDeployApp(t, r, "ghcr.io/acme/storefront@sha256:"+strings.Repeat("ab", 32))
	path := "/api/projects/" + autoDeployProject + "/apps/" + app.ID

	rec := doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"autoDeploy": true}, app.Version)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "tag") {
		t.Fatalf("a digest never moves, so there is nothing to watch: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAutoDeploy_ANewImageStartsANewWatch(t *testing.T) {
	r, store := setupAppRouter(t)
	app := createAutoDeployApp(t, r, "ghcr.io/acme/storefront:main")
	stored, _ := store.Get(autoDeployProject, app.ID)
	stored.AutoDeploy = true
	stored.ImageWatch = &apphost.ImageWatch{Digest: "sha256:" + strings.Repeat("ab", 32), CheckedAt: time.Now()}
	if err := store.Update(stored, stored.Version); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + autoDeployProject + "/apps/" + app.ID

	rec := doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"image": "ghcr.io/acme/storefront:next"}, stored.Version)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	var got apphost.App
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ImageWatch != nil || !got.AutoDeploy {
		t.Fatalf("what was seen of the old tag says nothing of the new one: %+v", got)
	}
}
