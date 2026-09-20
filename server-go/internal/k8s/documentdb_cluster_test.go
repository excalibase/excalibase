package k8s

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// documentDBTier is the smallest usable tier; none of these tests care which.
func documentDBTier() config.TierConfig {
	return config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"}
}

// postgresqlSection returns the built cluster's postgresql block.
func postgresqlSection(t *testing.T, opts PostgreSQLClusterOpts) map[string]interface{} {
	t.Helper()
	spec := BuildPostgreSQLCluster(opts).Object["spec"].(map[string]interface{})
	postgresql, ok := spec["postgresql"].(map[string]interface{})
	if !ok {
		t.Fatalf("cluster has no postgresql section: %v", spec["postgresql"])
	}
	return postgresql
}

// preloadedLibraries reads the cluster's shared_preload_libraries as strings.
func preloadedLibraries(t *testing.T, postgresql map[string]interface{}) []string {
	t.Helper()
	raw, ok := postgresql["shared_preload_libraries"].([]interface{})
	if !ok {
		return nil
	}
	libraries := make([]string, 0, len(raw))
	for _, entry := range raw {
		libraries = append(libraries, entry.(string))
	}
	return libraries
}

func listHas(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// A DocumentDB project's cluster preloads exactly what the extension needs.
// Without these libraries the extension does not load at all, so this is the
// difference between a DocumentDB project and a project that merely runs an
// image which could have carried one.
func TestDocumentDBClusterPreloadsTheExtensionsLibraries(t *testing.T) {
	postgresql := postgresqlSection(t, PostgreSQLClusterOpts{
		ProjectID:    "proj-doc000001",
		Namespace:    "org-a-proj-doc000001",
		Tier:         documentDBTier(),
		DatabaseName: "appdb",
		DocumentDB:   true,
	})

	libraries := preloadedLibraries(t, postgresql)
	for _, want := range config.DocumentDBPreloadLibraries() {
		if !listHas(libraries, want) {
			t.Errorf("shared_preload_libraries %v is missing %q", libraries, want)
		}
	}
}

// pg_cron serves one database per cluster and DocumentDB's DDL path goes
// through it, so the setting has to name the project's own application
// database — the one the extension is created in.
func TestDocumentDBClusterPointsPgCronAtTheProjectsDatabase(t *testing.T) {
	postgresql := postgresqlSection(t, PostgreSQLClusterOpts{
		ProjectID:    "proj-doc000002",
		Namespace:    "org-a-proj-doc000002",
		Tier:         documentDBTier(),
		DatabaseName: "appdb",
		DocumentDB:   true,
	})

	params := postgresql["parameters"].(map[string]interface{})
	if got := params[config.DocumentDBCronDatabaseSetting]; got != "appdb" {
		t.Errorf("%s: got %v, want the project's database", config.DocumentDBCronDatabaseSetting, got)
	}
}

// A project that did not ask for DocumentDB gets none of it, even on a major
// whose image carries the files. The choice is the project's, not the image's.
func TestAProjectWithoutDocumentDBGetsNoneOfItsConfiguration(t *testing.T) {
	postgresql := postgresqlSection(t, PostgreSQLClusterOpts{
		ProjectID:    "proj-plain0001",
		Namespace:    "org-a-proj-plain0001",
		Tier:         documentDBTier(),
		DatabaseName: "appdb",
	})

	for _, library := range preloadedLibraries(t, postgresql) {
		if listHas(config.DocumentDBPreloadLibraries(), library) {
			t.Errorf("a non-DocumentDB project preloads %q", library)
		}
	}
	params := postgresql["parameters"].(map[string]interface{})
	if _, set := params[config.DocumentDBCronDatabaseSetting]; set {
		t.Errorf("a non-DocumentDB project sets %s", config.DocumentDBCronDatabaseSetting)
	}
}

// Tenant parameters are merged into the cluster, so a tenant could otherwise
// replace shared_preload_libraries with a list of their own and leave the
// extension unable to load on the next restart — a project reported as
// DocumentDB that silently is not. DocumentDB's libraries survive, and the
// tenant's own additions survive alongside them.
func TestATenantParameterCannotDropDocumentDBsLibraries(t *testing.T) {
	postgresql := postgresqlSection(t, PostgreSQLClusterOpts{
		ProjectID:    "proj-doc000003",
		Namespace:    "org-a-proj-doc000003",
		Tier:         documentDBTier(),
		DatabaseName: "appdb",
		DocumentDB:   true,
		Parameters:   map[string]string{"shared_preload_libraries": "pg_stat_statements"},
	})

	libraries := preloadedLibraries(t, postgresql)
	for _, want := range config.DocumentDBPreloadLibraries() {
		if !listHas(libraries, want) {
			t.Errorf("a tenant parameter dropped %q: got %v", want, libraries)
		}
	}
	if !listHas(libraries, "pg_stat_statements") {
		t.Errorf("the tenant's own library was dropped: got %v", libraries)
	}
}

// Nor may a tenant repoint pg_cron at another database, which would leave
// DocumentDB's DDL path pointed away from the database it lives in.
func TestATenantParameterCannotRepointPgCron(t *testing.T) {
	postgresql := postgresqlSection(t, PostgreSQLClusterOpts{
		ProjectID:    "proj-doc000004",
		Namespace:    "org-a-proj-doc000004",
		Tier:         documentDBTier(),
		DatabaseName: "appdb",
		DocumentDB:   true,
		Parameters:   map[string]string{config.DocumentDBCronDatabaseSetting: "postgres"},
	})

	params := postgresql["parameters"].(map[string]interface{})
	if got := params[config.DocumentDBCronDatabaseSetting]; got != "appdb" {
		t.Errorf("%s: got %v, want the project's database", config.DocumentDBCronDatabaseSetting, got)
	}
}

// The extension is created in the database CNPG bootstraps, so when the
// request names none, pg_cron must follow CNPG's default rather than a name
// the builder invented.
func TestDocumentDBFollowsTheDefaultDatabaseNameWhenTheRequestNamesNone(t *testing.T) {
	postgresql := postgresqlSection(t, PostgreSQLClusterOpts{
		ProjectID:  "proj-doc000005",
		Namespace:  "org-a-proj-doc000005",
		Tier:       documentDBTier(),
		DocumentDB: true,
	})

	params := postgresql["parameters"].(map[string]interface{})
	if got := params[config.DocumentDBCronDatabaseSetting]; got != defaultClusterDatabase {
		t.Errorf("%s: got %v, want %q", config.DocumentDBCronDatabaseSetting, got, defaultClusterDatabase)
	}
}
