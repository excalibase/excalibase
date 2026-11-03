package service

import (
	"context"
	"log"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	postgresDataDir   = "/var/lib/postgresql/data"
	postgresContainer = "postgres"
	mysqlDataDir      = "/var/lib/mysql"
	mysqlContainer    = "mysqld"
	mocoRoleLabel     = "moco.cybozu.com/role"
	bytesPerGiB       = 1024 * 1024 * 1024
)

// databaseVolume is where one engine keeps its data: its instance pods,
// found by label so an instance CNPG or MOCO replaced is still read.
type databaseVolume struct {
	selector  string
	prefix    string
	container string
	dir       string
	// roleLabel is the pod label naming the primary; empty is CNPG's -1 instance.
	roleLabel string
	// containerUsage measures the database container alone, not its pod's sidecars.
	containerUsage bool
}

func postgresVolume(inst *domain.DatabaseInstance) databaseVolume {
	cluster := inst.ProjectID + postgresSuffix
	return databaseVolume{selector: "cnpg.io/cluster=" + cluster, prefix: cluster + "-", container: postgresContainer, dir: postgresDataDir}
}

func mysqlVolume(inst *domain.DatabaseInstance) databaseVolume {
	return databaseVolume{selector: k8s.MySQLPodSelector(inst.ProjectID), prefix: "moco-" + k8s.MySQLClusterName(inst.ProjectID) + "-",
		container: mysqlContainer, dir: mysqlDataDir, roleLabel: mocoRoleLabel, containerUsage: true}
}

func (s *MetricsService) volumePods(ctx context.Context, inst *domain.DatabaseInstance, volume databaseVolume) []string {
	pods, err := s.k8sClient.GetPods(ctx, inst.Namespace, volume.selector)
	if err != nil {
		log.Printf("metrics of %s: database pods: %v", inst.ProjectID, err)
		return nil
	}
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		if strings.HasPrefix(pod.Name, volume.prefix) && pod.DeletionTimestamp == nil {
			names = append(names, pod.Name)
		}
	}
	return names
}

// collectDiskUsage reads each instance's data volume with df inside the
// database container (the exec the exporter read already uses, so no
// monitoring stack or kubelet access is needed) and reports the fullest one:
// that is the instance that stops the database when it fills. An instance that
// cannot be read is skipped; none read leaves the figures unset.
func (s *MetricsService) collectDiskUsage(ctx context.Context, inst *domain.DatabaseInstance, volume databaseVolume, metrics *domain.DatabaseMetrics) {
	var fullest *k8s.AppDiskUsage
	for _, pod := range s.volumePods(ctx, inst, volume) {
		output, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, volume.container, []string{"df", "-P", "-k", volume.dir})
		if err != nil {
			log.Printf("metrics of %s: disk of %s: %v", inst.ProjectID, pod, err)
			continue
		}
		usage, err := k8s.ParseDiskFree(output)
		if err != nil {
			log.Printf("metrics of %s: disk of %s: %v", inst.ProjectID, pod, err)
			continue
		}
		if fullest == nil || share(usage) > share(*fullest) {
			fullest = &usage
		}
	}
	if fullest == nil {
		return
	}
	percent := share(*fullest) * 100
	gigabytes := fullest.UsedBytes / bytesPerGiB
	used, size := fullest.UsedBytes, fullest.SizeBytes
	metrics.DiskUsagePercent = &percent
	metrics.DiskUsageGB = &gigabytes
	metrics.DiskUsedBytes = &used
	metrics.DiskSizeBytes = &size
}

func share(usage k8s.AppDiskUsage) float64 {
	return float64(usage.UsedBytes) / float64(usage.SizeBytes)
}
