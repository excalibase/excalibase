package dockerapps

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/filters"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// Every container, network and volume the runtime makes carries these labels;
// the engine proxy acts only on what carries labelManaged.
const (
	labelManaged       = "excalibase.managed"
	labelComponent     = "excalibase.component"
	labelProject       = "excalibase.project"
	labelApp           = "excalibase.app"
	labelAppName       = "excalibase.app-name"
	labelDeploy        = "excalibase.deploy"
	labelReplica       = "excalibase.replica"
	labelTier          = "excalibase.tier"
	labelCPURequest    = "excalibase.cpu-request-milli"
	labelMemoryRequest = "excalibase.memory-request-bytes"
	labelPort          = "excalibase.port"
	labelProbe         = "excalibase.probe"
	labelDisk          = "excalibase.disk"
	labelDiskMount     = "excalibase.disk-mount"
	labelGeneration    = "excalibase.disk-generation"

	componentApp      = "app"
	componentDiskTool = "app-disk-tool"
)

// shortHash keeps generated names inside a DNS label: the edge dials containers by name.
func shortHash(value string, size int) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:size]
}

// appContainerName is one replica of one deploy: excalibase-app-<app>-<deploy>-<n>.
func appContainerName(projectID, appID, deployID string, replica int) string {
	return "excalibase-app-" + shortHash(projectID+"/"+appID, 12) + "-" + shortHash(deployID, 8) + "-" + strconv.Itoa(replica)
}

func (r *Runtime) networkName(projectID string) string { return r.opts.NetworkPrefix + projectID }

// volumeName is the claim the Kubernetes renderer names, under the volume prefix.
func (r *Runtime) volumeName(claim string) string { return r.opts.VolumePrefix + claim }

func (r *Runtime) diskVolume(appID string, generation int) string {
	return r.volumeName(k8s.AppDiskClaimName(appID, generation))
}

// appObjectAppName reverses k8s.AppObjectName, which the services pass as "name".
func appObjectAppName(name string) string { return strings.TrimPrefix(name, k8s.AppObjectName("")) }

func labelFilter(pairs ...string) filters.Args {
	args := filters.NewArgs(filters.Arg("label", labelManaged+"=true"))
	for i := 0; i+1 < len(pairs); i += 2 {
		args.Add("label", pairs[i]+"="+pairs[i+1])
	}
	return args
}
