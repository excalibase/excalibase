package netmeter

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/enginestats"
)

type fakeCluster struct {
	nodes []Node
	pods  []AppPod
	err   error
}

func (f fakeCluster) Nodes(context.Context) ([]Node, error)     { return f.nodes, f.err }
func (f fakeCluster) AppPods(context.Context) ([]AppPod, error) { return f.pods, f.err }

type fakeFetcher map[string]any // node address -> []PodNet or error

func (f fakeFetcher) Summary(_ context.Context, node string) ([]PodNet, error) {
	switch v := f[node].(type) {
	case error:
		return nil, v
	case []PodNet:
		return v, nil
	}
	return nil, errors.New("unknown node")
}

func TestKubeSource_AttributesPodsAndIgnoresEverythingElse(t *testing.T) {
	cluster := fakeCluster{nodes: []Node{{Name: "n1", Address: "10.0.0.1"}}, pods: []AppPod{{UID: "u1", ProjectID: "proj", AppID: "app"}}}
	fetcher := fakeFetcher{"10.0.0.1": []PodNet{
		{UID: "u1", At: t0, Started: t0.Add(-time.Hour), Rx: 1, Tx: 2},
		{UID: "cnpg-pod", At: t0, Rx: 9, Tx: 9},
	}}
	readings, err := NewKubeSource(cluster, fetcher).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(readings) != 1 || readings[0].Key != "u1" || readings[0].ProjectID != "proj" || readings[0].AppID != "app" || readings[0].Rx != 1 {
		t.Fatalf("only labelled app pods are metered: %+v", readings)
	}
}

func TestKubeSource_AFailingNodeStillReturnsTheOthersAndNamesWhatItDidNotSee(t *testing.T) {
	cluster := fakeCluster{
		nodes: []Node{{Name: "na", Address: "a"}, {Name: "nb", Address: "b"}},
		pods: []AppPod{
			{UID: "u1", ProjectID: "p", AppID: "x", NodeName: "na"},
			{UID: "u2", ProjectID: "p", AppID: "y", NodeName: "nb"},
			{UID: "u3", ProjectID: "p", AppID: "z", NodeName: "na"},
		},
	}
	fetcher := fakeFetcher{"a": errors.New("down"), "b": []PodNet{{UID: "u2", At: t0}}}
	readings, err := NewKubeSource(cluster, fetcher).Read(context.Background())
	var partial *PartialError
	if !errors.As(err, &partial) || len(readings) != 1 || readings[0].Key != "u2" {
		t.Fatalf("partial data plus a PartialError: %+v %v", readings, err)
	}
	got := append([]string(nil), partial.Unobserved...)
	slices.Sort(got)
	if !slices.Equal(got, []string{"u1", "u3"}) {
		t.Fatalf("the pods of the failed node are unobserved: %v", got)
	}
}

func TestKubeSource_NoNodesIsAnError(t *testing.T) {
	if _, err := NewKubeSource(fakeCluster{pods: []AppPod{{UID: "u"}}}, fakeFetcher{}).Read(context.Background()); err == nil {
		t.Fatal("no node to ask is an error, not silence")
	}
}

func TestKubeSource_ClusterErrorIsReturned(t *testing.T) {
	if _, err := NewKubeSource(fakeCluster{err: errors.New("api down")}, fakeFetcher{}).Read(context.Background()); err == nil {
		t.Fatal("cluster read errors surface")
	}
}

type fakeEngine struct {
	containers []enginestats.Container
	stats      map[string]enginestats.Stats
	statsErr   map[string]error
}

func (f fakeEngine) Managed(context.Context) ([]enginestats.Container, error) {
	return f.containers, nil
}
func (f fakeEngine) Stats(_ context.Context, ref string) (enginestats.Stats, error) {
	if err := f.statsErr[ref]; err != nil {
		return enginestats.Stats{}, err
	}
	return f.stats[ref], nil
}

func appContainer(id, project, app string) enginestats.Container {
	return enginestats.Container{ID: id, Name: id, State: "running", Labels: map[string]string{
		"excalibase.component": "app", "excalibase.project": project, "excalibase.app": app}}
}

func TestDockerSource_ReadsRunningAppContainersOnly(t *testing.T) {
	engine := fakeEngine{
		containers: []enginestats.Container{
			appContainer("c1", "proj", "app"),
			{ID: "db", Name: "db", State: "running", Labels: map[string]string{"excalibase.component": "database", "excalibase.project": "proj", "excalibase.app": "app"}},
			{ID: "c2", Name: "c2", State: "exited", Labels: map[string]string{"excalibase.component": "app", "excalibase.project": "proj", "excalibase.app": "other"}},
			appContainer("c3", "", "app"),
		},
		stats: map[string]enginestats.Stats{
			"c1": {Read: t0, NetOK: true, NetRx: 10, NetTx: 20},
			"c2": {Read: t0, NetOK: true, NetRx: 1, NetTx: 1},
			"db": {Read: t0, NetOK: true, NetRx: 1, NetTx: 1},
		},
	}
	readings, err := NewDockerSource(engine).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(readings) != 1 || readings[0].Key != "c1" || readings[0].Rx != 10 || readings[0].Tx != 20 || !readings[0].At.Equal(t0) {
		t.Fatalf("readings = %+v", readings)
	}
}

func TestDockerSource_NotMeasuredIsNotZero(t *testing.T) {
	engine := fakeEngine{
		containers: []enginestats.Container{appContainer("c1", "p", "a")},
		stats:      map[string]enginestats.Stats{"c1": {Read: t0, NetOK: false}},
	}
	readings, err := NewDockerSource(engine).Read(context.Background())
	if err != nil || len(readings) != 0 {
		t.Fatalf("a container whose engine reports no network counters (rootless pasta) is no data, not zero: %+v %v", readings, err)
	}
}

func TestDockerSource_AFailingContainerDoesNotHideTheOthersAndIsNamed(t *testing.T) {
	engine := fakeEngine{
		containers: []enginestats.Container{appContainer("c1", "p", "a"), appContainer("c2", "p", "b")},
		stats:      map[string]enginestats.Stats{"c2": {Read: t0, NetOK: true, NetRx: 1, NetTx: 1}},
		statsErr:   map[string]error{"c1": errors.New("boom")},
	}
	readings, err := NewDockerSource(engine).Read(context.Background())
	var partial *PartialError
	if !errors.As(err, &partial) || len(readings) != 1 || readings[0].Key != "c2" {
		t.Fatalf("partial data plus a PartialError: %+v %v", readings, err)
	}
	if !slices.Equal(partial.Unobserved, []string{"c1"}) {
		t.Fatalf("unobserved = %v", partial.Unobserved)
	}
}

func TestDockerSource_AllZeroCountersOnARunningContainerAreNotMeasured(t *testing.T) {
	engine := fakeEngine{
		containers: []enginestats.Container{appContainer("c1", "p", "a")},
		stats:      map[string]enginestats.Stats{"c1": {Read: t0, NetOK: true, NetRx: 0, NetTx: 0}},
	}
	readings, err := NewDockerSource(engine).Read(context.Background())
	if err != nil || len(readings) != 0 {
		t.Fatalf("a rootless engine that reports zero for everything gave no measurement: %+v %v", readings, err)
	}
	engine.stats["c1"] = enginestats.Stats{Read: t0, NetOK: true, NetRx: 0, NetTx: 7}
	readings, _ = NewDockerSource(engine).Read(context.Background())
	if len(readings) != 1 {
		t.Fatalf("one direction with traffic is a real reading: %+v", readings)
	}
}
