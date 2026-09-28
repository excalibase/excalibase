package k8s

import (
	"errors"
	"slices"

	"github.com/excalibase/provisioning-poc/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A DocumentDB project's own Mongo users each get a pg_ident line (EXC-427).
// DocumentDB connects back over the unix socket as the acting user, which the
// postgres OS user may only do for roles pg_ident maps. The "+group" form would
// need Postgres 16; one line per user works on every major DocumentDB runs on.
// CloudNativePG applies pg_ident changes with a reload, not a restart.

// ErrClusterHasNoPeerMap refuses a cluster that was not built for DocumentDB.
var ErrClusterHasNoPeerMap = errors.New("cluster has no DocumentDB peer map")

// ErrNotAMongoUserLine refuses to add or remove a platform role's mapping.
var ErrNotAMongoUserLine = errors.New("not a Mongo user's peer mapping")

var platformPeerRoles = []string{"documentdb_bg_worker_role", config.DocumentDBGatewayRole, appRoleName, "postgres", ""}

// WithMongoUserIdent returns a copy of the cluster with the user's peer line
// present or absent, and whether that differs from the input.
func WithMongoUserIdent(cluster *unstructured.Unstructured, username string, present bool) (*unstructured.Unstructured, bool, error) {
	if slices.Contains(platformPeerRoles, username) || username == bootstrapOwner(cluster) {
		return nil, false, ErrNotAMongoUserLine
	}
	lines, found, err := unstructuredStrings(cluster.Object, "spec", "postgresql", "pg_ident")
	if err != nil || !found || len(lines) == 0 {
		return nil, false, ErrClusterHasNoPeerMap
	}
	line := "local postgres " + username
	has := slices.Contains(lines, line)
	if has == present {
		return cluster.DeepCopy(), false, nil
	}
	next := make([]interface{}, 0, len(lines)+1)
	for _, existing := range lines {
		if existing != line {
			next = append(next, existing)
		}
	}
	if present {
		next = append(next, line)
	}
	changed := cluster.DeepCopy()
	if err := unstructured.SetNestedSlice(changed.Object, next, "spec", "postgresql", "pg_ident"); err != nil {
		return nil, false, err
	}
	return changed, true, nil
}

// bootstrapOwner is the project owner the cluster was created or recovered for.
func bootstrapOwner(cluster *unstructured.Unstructured) string {
	for _, method := range []string{"initdb", "recovery"} {
		if owner, found, _ := unstructured.NestedString(cluster.Object, "spec", "bootstrap", method, "owner"); found {
			return owner
		}
	}
	return ""
}

func unstructuredStrings(obj map[string]interface{}, fields ...string) ([]string, bool, error) {
	return unstructured.NestedStringSlice(obj, fields...)
}
