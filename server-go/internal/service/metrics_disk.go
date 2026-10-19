package service

import (
	"context"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	postgresDataDir   = "/var/lib/postgresql/data"
	postgresContainer = "postgres"
	mysqlDataDir      = "/var/lib/mysql"
	mysqlContainer    = "mysqld"
	bytesPerGiB       = 1024 * 1024 * 1024
)

// databaseVolume is where one engine keeps its data, per instance pod.
type databaseVolume struct {
	pods      []string
	container string
	dir       string
}

func postgresVolume(inst *domain.DatabaseInstance, instances int) databaseVolume {
	pods := make([]string, 0, instances)
	for i := 1; i <= instances; i++ {
		pods = append(pods, fmt.Sprintf("%s-postgres-%d", inst.ProjectID, i))
	}
	return databaseVolume{pods: pods, container: postgresContainer, dir: postgresDataDir}
}

func mysqlVolume(inst *domain.DatabaseInstance, instances int) databaseVolume {
	pods := make([]string, 0, instances)
	for i := 0; i < instances; i++ {
		pods = append(pods, k8s.MySQLPodName(inst.ProjectID, int64(i)))
	}
	return databaseVolume{pods: pods, container: mysqlContainer, dir: mysqlDataDir}
}

// collectDiskUsage reads each instance's data volume with df inside the
// database container (the exec the exporter read already uses, so no
// monitoring stack or kubelet access is needed) and reports the fullest one:
// that is the instance that stops the database when it fills. An instance that
// cannot be read is skipped; none read leaves the figures unset.
func (s *MetricsService) collectDiskUsage(ctx context.Context, inst *domain.DatabaseInstance, volume databaseVolume, metrics *domain.DatabaseMetrics) {
	var fullest *k8s.AppDiskUsage
	for _, pod := range volume.pods {
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
