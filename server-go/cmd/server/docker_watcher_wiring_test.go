package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

const pinnedWatcher = "docker.io/excalibase/excalibase-watcher-go@sha256:7b2f9feb21b2a92b34c3f7b136399c619d460bf5d296215b8662a7a334126d9e"

func TestASingleHostRunsTheWatcherItIsGiven(t *testing.T) {
	got, err := dockerWatcherConfig(config.AppConfig{WatcherImage: pinnedWatcher, WatcherNatsURL: "nats://nats:4222", DockerCDCNetwork: "excalibase-cdc", NatsCDCStream: "CDC"})
	if err != nil {
		t.Fatal(err)
	}
	want := provisioner.DockerWatcherConfig{Image: pinnedWatcher, NatsURL: "nats://nats:4222", Network: "excalibase-cdc", Stream: "CDC"}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestASingleHostWithoutAWatcherImageRunsNone(t *testing.T) {
	got, err := dockerWatcherConfig(config.AppConfig{})
	if err != nil || got.Image != "" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestAWatcherImageWithoutItsBusIsRefused(t *testing.T) {
	for name, cfg := range map[string]config.AppConfig{
		"no bus url":     {WatcherImage: pinnedWatcher, DockerCDCNetwork: "excalibase-cdc"},
		"no bus network": {WatcherImage: pinnedWatcher, WatcherNatsURL: "nats://nats:4222"},
	} {
		if _, err := dockerWatcherConfig(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
