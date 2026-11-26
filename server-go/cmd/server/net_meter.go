package main

import (
	"context"
	"log"
	"sync/atomic"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/netmeter"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// netMeterStarted is whether a meter was built, so the admin console switches
// its egress card only once something is being measured.
var netMeterStarted atomic.Bool

func netMeterRunning() bool { return netMeterStarted.Load() }

// netMeterOn is the netmeter flag; it does not depend on host monitoring.
func netMeterOn(cfg config.AppConfig) bool {
	return features.Enabled(context.Background(), cfg.Features(), features.NetMeter)
}

// netMeterSampler builds the per-app network meter for the metrics loop
// (EXC-844), or nil while the flag is off or what it needs is missing. A
// missing piece is logged and leaves the meter off: it never reads through an
// unverified connection. cluster is the Kubernetes client (nil on a single
// host); store must keep the metering tables.
func netMeterSampler(cfg config.AppConfig, store, cluster any) service.LoopSampler {
	if !netMeterOn(cfg) {
		return nil
	}
	tables, ok := store.(netmeter.Store)
	if !ok {
		log.Printf("net meter: the platform store keeps no metering tables; off")
		return nil
	}
	source := netMeterSource(cfg, cluster)
	if source == nil {
		return nil
	}
	log.Printf("net meter: on (%s)", cfg.ProvisionerMode)
	netMeterStarted.Store(true)
	return netmeter.NewMeter(source, tables, metricsSampleInterval)
}

func netMeterSource(cfg config.AppConfig, cluster any) netmeter.Source {
	if cfg.ProvisionerMode == "docker" {
		engine, err := enginestats.New(cfg.DockerHost)
		if err != nil {
			log.Printf("net meter: %v; off", err)
			return nil
		}
		return netmeter.NewDockerSource(engine)
	}
	reader, ok := cluster.(netmeter.ClusterReader)
	if !ok || reader == nil {
		log.Printf("net meter: no Kubernetes client; off")
		return nil
	}
	fetcher, err := netmeter.NewKubeletFetcher(cfg.NetMeter.KubeletCAFile, cfg.NetMeter.KubeletTokenFile, cfg.NetMeter.KubeletPort)
	if err != nil {
		log.Printf("net meter: %v; off", err)
		return nil
	}
	return netmeter.NewKubeSource(reader, fetcher)
}
