package k8s

import (
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

func initdbSection(t *testing.T, opts PostgreSQLClusterOpts) map[string]interface{} {
	t.Helper()
	spec := BuildPostgreSQLCluster(opts).Object["spec"].(map[string]interface{})
	bootstrap, _ := spec["bootstrap"].(map[string]interface{})
	initdb, _ := bootstrap["initdb"].(map[string]interface{})
	return initdb
}

func postInitSQL(initdb map[string]interface{}) []string {
	raw, _ := initdb["postInitSQL"].([]interface{})
	statements := make([]string, 0, len(raw))
	for _, statement := range raw {
		statements = append(statements, statement.(string))
	}
	return statements
}

// CNPG runs postInitSQL in the postgres database from its initdb job, before
// the first instance pod, and with it the gateway, is created.
func TestDocumentDBClusterCreatesTheGatewaysRoleBeforeAnyPodStarts(t *testing.T) {
	initdb := initdbSection(t, documentDBOpts("owner_doc"))
	if got := postInitSQL(initdb); !reflect.DeepEqual(got, config.DocumentDBBootstrapSQL()) {
		t.Fatalf("postInitSQL = %v, want %v", got, config.DocumentDBBootstrapSQL())
	}
	if initdb["database"] != "appdb" || initdb["owner"] != "owner_doc" {
		t.Errorf("the project's database and owner must survive: %v", initdb)
	}
}

func TestDocumentDBClusterWithCNPGsDefaultDatabaseStillBootstrapsTheGateway(t *testing.T) {
	opts := documentDBOpts("app")
	opts.DatabaseName = "app"
	initdb := initdbSection(t, opts)
	if got := postInitSQL(initdb); !reflect.DeepEqual(got, config.DocumentDBBootstrapSQL()) {
		t.Fatalf("postInitSQL = %v, want %v", got, config.DocumentDBBootstrapSQL())
	}
}

func TestAnOrdinaryClusterRunsNoPostInitSQL(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.DocumentDB = false
	if got := postInitSQL(initdbSection(t, opts)); len(got) != 0 {
		t.Fatalf("a project without DocumentDB ran bootstrap SQL: %v", got)
	}
}
