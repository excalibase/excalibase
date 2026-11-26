package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// AuditLogParameter is the pgaudit setting whose presence makes CloudNativePG
// preload the library; the extension cannot be created without the preload.
const AuditLogParameter = "pgaudit.log"

const auditParameterPrefix = "pgaudit."

// AuditLog is the cluster's pgaudit.log setting, empty when audit is off.
func AuditLog(cluster *unstructured.Unstructured) string {
	params, _, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "parameters")
	value, _ := params[AuditLogParameter].(string)
	return value
}

// auditParameters are the pgaudit settings of a parameter map.
func auditParameters(params map[string]interface{}) map[string]interface{} {
	audit := map[string]interface{}{}
	for name, value := range params {
		if strings.HasPrefix(name, auditParameterPrefix) {
			audit[name] = value
		}
	}
	return audit
}
