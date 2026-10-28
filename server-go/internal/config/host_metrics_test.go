package config

import (
	"reflect"
	"testing"
)

func TestLoadHostMetricsReadsTheMountAndThePlatformContainers(t *testing.T) {
	t.Setenv("HOST_METRICS_NET_DEV", " /host/proc-net-dev ")
	t.Setenv("HOST_METRICS_CONTAINERS", "excalibase-postgres, excalibase-nats,,")
	got := loadHostMetrics()
	if got.NetDev != "/host/proc-net-dev" || !reflect.DeepEqual(got.Containers, []string{"excalibase-postgres", "excalibase-nats"}) {
		t.Errorf("host metrics = %+v", got)
	}
	t.Setenv("HOST_METRICS_NET_DEV", "")
	t.Setenv("HOST_METRICS_CONTAINERS", "")
	if got := loadHostMetrics(); got.NetDev != "" || got.Containers != nil {
		t.Errorf("unset = %+v", got)
	}
}
