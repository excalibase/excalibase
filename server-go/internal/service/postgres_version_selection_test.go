package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-408: the major comes from the request. There is no implicit default, so
// a request that names none is refused rather than quietly provisioned on
// whatever the code happened to hardcode.

func provisionRequest(version string) domain.ProvisioningRequest {
	return domain.ProvisioningRequest{
		ProjectName:     "blog",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Free,
		PostgresVersion: version,
	}
}

func TestValidateRefusesAMissingPostgresMajor(t *testing.T) {
	for _, version := range []string{"", "   "} {
		err := validateProvisioningRequest(provisionRequest(version))
		if err == nil {
			t.Fatalf("version %q: expected a refusal, got nil", version)
		}
		if !strings.Contains(err.Error(), "postgres version") {
			t.Errorf("version %q: error should mention the postgres version, got %q", version, err)
		}
	}
}

func TestValidateRefusesAnUnsupportedPostgresMajor(t *testing.T) {
	for _, version := range []string{"9.2", "13", "19", "17.2", "latest"} {
		err := validateProvisioningRequest(provisionRequest(version))
		if err == nil {
			t.Errorf("version %q: expected a refusal, got nil", version)
		}
	}
}

func TestValidateAcceptsEverySupportedMajor(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		if err := validateProvisioningRequest(provisionRequest(major)); err != nil {
			t.Errorf("major %s: expected acceptance, got %v", major, err)
		}
	}
}

func TestValidateRefusalNamesTheSupportedMajors(t *testing.T) {
	err := validateProvisioningRequest(provisionRequest("13"))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, major := range config.PostgresMajors() {
		if !strings.Contains(err.Error(), major) {
			t.Errorf("refusal should list supported major %s, got %q", major, err)
		}
	}
}

// EXC-407 item 5: DocumentDB is refused at the API on a major whose image does
// not carry it, rather than failing when the extension is installed.
func TestValidateRefusesDocumentDBOnAMajorThatCannotOfferIt(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		if config.DocumentDBSupported(major) {
			continue
		}
		req := provisionRequest(major)
		req.DocumentDB = true
		err := validateProvisioningRequest(req)
		if err == nil {
			t.Errorf("major %s: DocumentDB must be refused", major)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), "documentdb") {
			t.Errorf("major %s: refusal should mention DocumentDB, got %q", major, err)
		}
	}
}

func TestValidateAcceptsDocumentDBOnASupportedMajor(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		if !config.DocumentDBSupported(major) {
			continue
		}
		req := provisionRequest(major)
		req.DocumentDB = true
		if err := validateProvisioningRequest(req); err != nil {
			t.Errorf("major %s: DocumentDB should be accepted, got %v", major, err)
		}
	}
}

func TestProvisionRefusesARequestWithNoPostgresMajor(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), provisionRequest(""))
	if err == nil || !strings.Contains(err.Error(), "postgres version") {
		t.Errorf("expected a postgres version refusal, got: %v", err)
	}
}

func TestUpgradeVersionRefusesAnUnsupportedMajor(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	err := svc.UpgradeVersion(context.Background(), "proj-1", "13")
	if err == nil {
		t.Fatal("UpgradeVersion accepted an unsupported major")
	}
	if !strings.Contains(err.Error(), "13") {
		t.Errorf("error should name the rejected major, got %q", err)
	}
}

func TestUpgradeVersionRefusesAnEmptyMajor(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	if err := svc.UpgradeVersion(context.Background(), "proj-1", ""); err == nil {
		t.Fatal("UpgradeVersion accepted an empty major")
	}
}

// The Docker restore path must reuse the source project's major rather than
// falling back to a hardcoded one.
func TestRestoreImageHonoursTheSourceMajor(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		image, err := restoreImage(major)
		if err != nil {
			t.Errorf("major %s: %v", major, err)
			continue
		}
		if image != "postgres:"+major {
			t.Errorf("major %s: got %q", major, image)
		}
	}
}

func TestRestoreImageRefusesAnUnknownMajor(t *testing.T) {
	for _, major := range []string{"", "13", "latest"} {
		if _, err := restoreImage(major); err == nil {
			t.Errorf("major %q: restoreImage must refuse rather than default", major)
		}
	}
}

// The major a project was created on is the only record of what its data is
// on, and restore reads it back to pick the image to recover into. Not
// recording it leaves restore with nothing to resolve; recording the image tag
// instead would make the field mean two different things on the two
// deployment paths. So it is the bare major, in the catalogue's spelling.
func TestProvisioningRecordsTheMajorItWasCreatedOn(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)

	// Padded on purpose: what is stored must be the catalogue's spelling, not
	// whatever the caller typed, or a later exact-match lookup fails.
	req := domain.ProvisioningRequest{
		PostgresVersion: " 16 ",
		ProjectName:     "records-its-major",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Enterprise,
	}
	resp, err := svc.Provision(context.Background(), req)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	inst, err := store.FindByProjectID(resp.ProjectID)
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if inst.PostgresVersion != "16" {
		t.Fatalf("recorded postgres version %q, want %q", inst.PostgresVersion, "16")
	}
	if _, err := config.DockerPostgresImage(inst.PostgresVersion); err != nil {
		t.Errorf("the recorded version must resolve to an image: %v", err)
	}
}

// Unreachable through Provision, which validates first — but it is the guard
// that keeps an unsupported major from ever being written down as if it were
// one, so it has to refuse rather than store the string it was handed.
func TestCanonicalPostgresMajorRefusesAnUnsupportedMajor(t *testing.T) {
	for _, version := range []string{"13", "18", "16-alpine", ""} {
		if _, err := canonicalPostgresMajor(version); err == nil {
			t.Errorf("version %q: expected a refusal", version)
		}
	}
}

func TestCanonicalPostgresMajorReturnsTheCatalogueSpelling(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		got, err := canonicalPostgresMajor("  " + major + "  ")
		if err != nil {
			t.Errorf("major %s: %v", major, err)
			continue
		}
		if got != major {
			t.Errorf("major %s: got %q", major, got)
		}
	}
}

func upgradeFixture(t *testing.T, recordedMajor string) (*ProvisioningService, *k8s.MockClient) {
	t.Helper()
	svc, store, mock := setupOpsTest(t)
	inst, err := store.FindByProjectID(testOpsDB)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	inst.PostgresVersion = recordedMajor
	if err := store.Update(inst); err != nil {
		t.Fatalf("fixture update: %v", err)
	}
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: testOpsDB, Namespace: testOpsDBNS,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	if err := mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, cluster); err != nil {
		t.Fatalf("fixture cluster: %v", err)
	}
	return svc, mock
}

func clusterImage(t *testing.T, mock *k8s.MockClient) interface{} {
	t.Helper()
	got, err := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	return got.Object["spec"].(map[string]interface{})["imageName"]
}

// A major upgrade needs pg_upgrade, which is not verified for our operator
// version, so re-pinning a cluster onto another major is refused before the
// cluster is touched.
func TestUpgradeVersionRefusesAMajorChange(t *testing.T) {
	svc, mock := upgradeFixture(t, "16")
	err := svc.UpgradeVersion(context.Background(), testOpsDB, "17")
	if err == nil {
		t.Fatal("UpgradeVersion moved a 16 project onto 17")
	}
	if !strings.Contains(err.Error(), "16") || !strings.Contains(err.Error(), "17") {
		t.Errorf("error should name both majors, got %q", err)
	}
	if image := clusterImage(t, mock); image != nil {
		t.Errorf("the cluster was patched to %v", image)
	}
}

func TestUpgradeVersionRefusesAProjectWithNoRecordedMajor(t *testing.T) {
	svc, mock := upgradeFixture(t, "")
	if err := svc.UpgradeVersion(context.Background(), testOpsDB, "17"); err == nil {
		t.Fatal("UpgradeVersion guessed the major of a project that records none")
	}
	if image := clusterImage(t, mock); image != nil {
		t.Errorf("the cluster was patched to %v", image)
	}
}

func TestUpgradeVersionRepinsTheSameMajor(t *testing.T) {
	svc, mock := upgradeFixture(t, "16")
	if err := svc.UpgradeVersion(context.Background(), testOpsDB, "16"); err != nil {
		t.Fatalf("UpgradeVersion on the recorded major: %v", err)
	}
	want, err := config.PostgresImage("16")
	if err != nil {
		t.Fatalf("resolve image: %v", err)
	}
	if image := clusterImage(t, mock); image != want {
		t.Errorf("imageName: got %v, want %v", image, want)
	}
}
