package engineproxy

import (
	"net/http"
	"testing"
)

// EXC-668: a major upgrade renames a project's database container; only a
// managed container, and only to a name a create could take.
func TestHandlerRenamesAManagedContainerToAManagedName(t *testing.T) {
	engine, proxy := newProxy(t)
	for _, ok := range []string{
		"/v1.47/containers/excalibase-p1-postgres/rename?name=excalibase-p1-postgres-pre-upgrade-mu-1a2b",
		"/v1.47/containers/excalibase-p1-postgres/rename?name=excalibase-p1-postgres",
	} {
		if resp := call(t, proxy.URL, http.MethodPost, ok, ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d", ok, resp.StatusCode)
		}
	}
	if !engine.reached("POST /v1.47/containers/excalibase-p1-postgres/rename") {
		t.Fatal("the rename never reached the engine")
	}
	for _, refused := range []string{
		"/v1.47/containers/excalibase-provisioning/rename?name=excalibase-x",
		"/v1.47/containers/excalibase-p1-postgres/rename?name=provisioning",
		"/v1.47/containers/excalibase-p1-postgres/rename?name=" + testPolicy().ContainerPrefix,
		"/v1.47/containers/excalibase-p1-postgres/rename",
		"/v1.47/containers/excalibase-p1-postgres/rename?name=excalibase-a&name=excalibase-b",
		"/v1.47/containers/excalibase-p1-postgres/rename?name=excalibase-postgres",
		"/v1.47/containers/excalibase-p1-postgres/rename?name=excalibase-provisioning",
		"/v1.47/containers/excalibase-p1-postgres/rename?name=excalibase-edge",
	} {
		if resp := call(t, proxy.URL, http.MethodPost, refused, ""); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want refused", refused, resp.StatusCode)
		}
	}
}
