package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func TestProvision_ClusterSettingsThePlatformDoesNotOfferAnswer400(t *testing.T) {
	cases := map[string]struct{ extra, reason string }{
		"a storage class not on the allowlist": {`,"storageClassName":"local-path"`, "storage class"},
		"a five-field backup schedule":         {`,"backup":{"enabled":true,"schedule":"0 2 * * *","retention":7}`, "backup schedule"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
			orgs := fakestore.NewOrgs()
			orgs.AddOrg("org1", domain.Standard)
			r := provisionRouterWithOrgs(t, store, k8s.NewMockClient(), orgs)

			w := doRequest(r, "POST", testProvisionPath,
				`{"projectName":"p","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17"`+tc.extra+`}`)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.reason) {
				t.Fatalf("got %d %s, want 400 naming the %s", w.Code, w.Body.String(), tc.reason)
			}
			if len(store.insts) != 0 {
				t.Errorf("no project may be recorded: %v", store.insts)
			}
		})
	}
}
