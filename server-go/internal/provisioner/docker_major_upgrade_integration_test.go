//go:build integration

package provisioner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// EXC-668: recovery reads the data directory's major through PGDATA, which
// the official image moved in Postgres 18; the script must read it on both.
func TestDataMajorScriptReadsTheMajorOnRealImages(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	for _, major := range []string{"17", "18"} {
		t.Run(major, func(t *testing.T) {
			out, err := dataMajorOn(t, docker, major)
			if err != nil || strings.TrimSpace(out) != major {
				t.Fatalf("data major = %q (%v), want %s", out, err, major)
			}
		})
	}
}

// dataMajorOn runs DataMajorScript in a fresh postgres:major container once
// its server answers.
func dataMajorOn(t *testing.T, docker *RealDockerClient, major string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	name := fmt.Sprintf("it-major-%s-%d", major, time.Now().UnixNano()%1_000_000)
	id, err := docker.CreateContainerSpec(ctx, ContainerSpec{Name: name, Image: "postgres:" + major,
		Env: map[string]string{"POSTGRES_PASSWORD": "it-only"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = docker.RemoveContainer(context.Background(), id) })
	if err := docker.StartContainer(ctx, id); err != nil {
		t.Fatalf("start: %v", err)
	}
	for i := 0; i < 60; i++ {
		if code, _ := docker.ExecInContainer(ctx, id, []string{"pg_isready", "-h", "127.0.0.1", "-U", "postgres"}); code == 0 {
			return docker.ExecInContainerStdin(ctx, id, []string{"sh", "-s"}, DataMajorScript())
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("postgres:%s did not start", major)
}
