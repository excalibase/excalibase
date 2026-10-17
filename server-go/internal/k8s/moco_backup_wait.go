package k8s

import (
	_ "embed" // go:embed carries the wait script
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EXC-586: on k3s, kube-router admits a new pod to its peers' NetworkPolicies
// a moment after the pod starts, and moco-backup dials the database and the
// store once, at start. MOCO's jobConfig has no init containers, so the
// platform adds one to the CronJob MOCO renders; MOCO applies the CronJob
// server-side and leaves a field it does not own in place.
const (
	// MySQLEgressWaitContainerName holds the backup back until it can reach what it dials.
	MySQLEgressWaitContainerName = "wait-for-egress"

	mysqlEgressWaitStoreEnv    = "EGRESS_WAIT_STORE"
	mysqlEgressWaitDatabaseEnv = "EGRESS_WAIT_DATABASE"
	// mysqlEgressWaitSecondsEnv overrides the 60 s deadline; the rendered container sets none.
	mysqlEgressWaitSecondsEnv = "EGRESS_WAIT_SECONDS"
	mysqlEgressWaitCPU        = "100m"
	mysqlEgressWaitMemory     = "64Mi"
	// mocoJobUser is the user MOCO's job image and containers run as.
	mocoJobUser = int64(10000)

	mocoBackupContainerName = "backup"
	mocoEndpointFlag        = "--endpoint="
)

// mysqlEgressWaitScript runs with the bash, timeout and getent of MOCO's own
// backup image, so nothing else is pulled.
//
//go:embed moco_backup_wait.sh
var mysqlEgressWaitScript string

// mocoJobPodSpecPath is a field of the pod spec in a backup CronJob.
func mocoJobPodSpecPath(field string) []string {
	return []string{"spec", "jobTemplate", "spec", "template", "spec", field}
}

// WithMySQLBackupEgressWait is a copy of MOCO's backup CronJob whose runs
// first wait until the object store and the database answer, and whether
// that changed anything.
func WithMySQLBackupEgressWait(cronJob *unstructured.Unstructured) (*unstructured.Unstructured, bool, error) {
	want, err := mysqlEgressWaitContainer(cronJob)
	if err != nil {
		return nil, false, err
	}
	out := cronJob.DeepCopy()
	inits, _, _ := unstructured.NestedSlice(out.Object, mocoJobPodSpecPath("initContainers")...)
	kept := make([]interface{}, 0, len(inits)+1)
	for _, raw := range inits {
		container, _ := raw.(map[string]interface{})
		if container["name"] != MySQLEgressWaitContainerName {
			kept = append(kept, raw)
			continue
		}
		if sameEgressWait(container, want) {
			return out, false, nil
		}
	}
	// The wait goes first: nothing else in the pod may run before egress answers.
	kept = append([]interface{}{want}, kept...)
	if err := unstructured.SetNestedSlice(out.Object, kept, mocoJobPodSpecPath("initContainers")...); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// sameEgressWait compares what tracks MOCO and the store; the API server fills
// in defaults elsewhere, so a change to the wait's resources or securityContext
// alone does not reach CronJobs that already wait.
func sameEgressWait(got, want map[string]interface{}) bool {
	for _, field := range []string{"image", "command", "env"} {
		if !reflect.DeepEqual(got[field], want[field]) {
			return false
		}
	}
	return true
}

func mysqlEgressWaitContainer(cronJob *unstructured.Unstructured) (map[string]interface{}, error) {
	backup, err := mocoBackupContainer(cronJob)
	if err != nil {
		return nil, err
	}
	image, _ := backup["image"].(string)
	store, err := mocoBackupStoreAddress(backup)
	if err != nil {
		return nil, fmt.Errorf("backup CronJob %s: %w", cronJob.GetName(), err)
	}
	labels, _, _ := unstructured.NestedStringMap(cronJob.Object, "spec", "jobTemplate", "spec", "template", "metadata", "labels")
	cluster := labels[mocoInstanceLabel]
	if image == "" || cluster == "" {
		return nil, fmt.Errorf("backup CronJob %s names no image or cluster", cronJob.GetName())
	}
	// MOCO's headless Service lists every instance: moco-<cluster>.<namespace>.svc.
	database := fmt.Sprintf("moco-%s.%s.svc:%d", cluster, cronJob.GetNamespace(), mysqlPortNumber)
	bound := map[string]interface{}{"cpu": mysqlEgressWaitCPU, "memory": mysqlEgressWaitMemory}
	return map[string]interface{}{
		"name":            MySQLEgressWaitContainerName,
		"image":           image,
		"imagePullPolicy": "IfNotPresent",
		"command":         []interface{}{"bash", "-c", mysqlEgressWaitScript},
		"env": []interface{}{
			map[string]interface{}{"name": mysqlEgressWaitStoreEnv, "value": store},
			map[string]interface{}{"name": mysqlEgressWaitDatabaseEnv, "value": database},
		},
		"resources": map[string]interface{}{"requests": bound, "limits": bound},
		"securityContext": map[string]interface{}{
			"allowPrivilegeEscalation": false,
			"readOnlyRootFilesystem":   true,
			"runAsNonRoot":             true,
			"runAsUser":                mocoJobUser,
			"runAsGroup":               mocoJobUser,
			"capabilities":             map[string]interface{}{"drop": []interface{}{"ALL"}},
			"seccompProfile":           map[string]interface{}{"type": "RuntimeDefault"},
		},
	}, nil
}

func mocoBackupContainer(cronJob *unstructured.Unstructured) (map[string]interface{}, error) {
	containers, _, _ := unstructured.NestedSlice(cronJob.Object, mocoJobPodSpecPath("containers")...)
	for _, raw := range containers {
		if container, ok := raw.(map[string]interface{}); ok && container["name"] == mocoBackupContainerName {
			return container, nil
		}
	}
	return nil, fmt.Errorf("backup CronJob %s has no %q container", cronJob.GetName(), mocoBackupContainerName)
}

// mocoBackupStoreAddress is host:port of the endpoint MOCO passes the job.
func mocoBackupStoreAddress(backup map[string]interface{}) (string, error) {
	args, _ := backup["args"].([]interface{})
	for _, raw := range args {
		arg, _ := raw.(string)
		if !strings.HasPrefix(arg, mocoEndpointFlag) {
			continue
		}
		endpoint, err := url.Parse(strings.TrimPrefix(arg, mocoEndpointFlag))
		if err != nil || endpoint.Hostname() == "" {
			return "", fmt.Errorf("store endpoint %q is not a URL", arg)
		}
		port := endpoint.Port()
		if port == "" {
			port = strconv.Itoa(defaultPort(endpoint.Scheme))
		}
		return endpoint.Hostname() + ":" + port, nil
	}
	return "", errors.New("the backup job names no store endpoint")
}

func defaultPort(scheme string) int {
	if scheme == "http" {
		return 80
	}
	return 443
}
