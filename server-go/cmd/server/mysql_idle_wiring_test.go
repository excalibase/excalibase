package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-588: MySQL client work feeds the idle sweep only behind the mysql
// feature, and only where MySQL is served.
func TestMySQLIdleActivityFollowsTheMySQLFeature(t *testing.T) {
	on := config.AppConfig{DarkFeatures: []features.Feature{features.MySQL}}
	mysql := provisioner.NewMySQLProvisioner(k8s.NewMockClient())
	if mysqlIdleActivity(on, mysql) == nil {
		t.Error("no MySQL activity reader with the feature on")
	}
	if mysqlIdleActivity(config.AppConfig{}, mysql) != nil {
		t.Error("a MySQL activity reader with the feature off")
	}
	if mysqlIdleActivity(on, nil) != nil {
		t.Error("a MySQL activity reader where MySQL is not served")
	}
}
