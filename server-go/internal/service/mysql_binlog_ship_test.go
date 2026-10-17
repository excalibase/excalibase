package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// fakeShipStore is a bucket that also stores what is written to it.
type fakeShipStore struct {
	*fakeObjectDeleter
	data map[string][]byte
}

func (s *fakeShipStore) Upload(_ context.Context, _, key string, body io.Reader) (int64, error) {
	content, err := io.ReadAll(body)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	if s.sizes == nil {
		s.sizes = map[string]int64{}
	}
	if _, seen := s.data[key]; !seen && !containsKey(s.keys, key) {
		s.keys = append(s.keys, key)
		sort.Strings(s.keys)
	}
	s.data[key] = content
	s.sizes[key] = int64(len(content))
	return int64(len(content)), nil
}

func (s *fakeShipStore) Download(_ context.Context, _, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

// fakeMOCOPods stands in for the mysqld containers: the primary (index 0)
// answers the commit state, the backup source (index 1) waits, flushes and
// serves its binlog files; a restored cluster's primary takes the replay.
type fakeMOCOPods struct {
	mu       sync.Mutex
	clock    *time.Time
	gtid     string
	source   string
	lagging  bool
	oldest   int
	basename string
	failOn   string
	current  int
	flushes  int
	replays  []string
	replayed []string
}

func (p *fakeMOCOPods) StreamExecInPod(_ context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout io.Writer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if container != "mysqld" {
		return fmt.Errorf("exec in container %s", container)
	}
	script := strings.Join(cmd, " ")
	var input string
	if stdin != nil && !strings.Contains(script, "mysqlbinlog") {
		content, _ := io.ReadAll(stdin)
		input = string(content)
	}
	if p.failOn != "" && strings.Contains(script, p.failOn) {
		return errors.New("command terminated with exit code 1")
	}
	switch {
	case strings.Contains(script, "mysqlbinlog"):
		return p.replay(namespace+"/"+pod, script, stdin)
	case strings.HasPrefix(script, "cat "):
		file := script[strings.LastIndex(script, "/")+1:]
		_, err := io.WriteString(stdout, "events of "+file)
		return err
	case strings.Contains(input, "gtid_executed"):
		_, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", serverA, p.clock.UTC().Format(time.RFC3339), p.gtid)
		return err
	case strings.Contains(input, "WAIT_FOR_EXECUTED_GTID_SET"):
		wait := 0
		if p.lagging {
			wait = 1
		}
		_, err := fmt.Fprintf(stdout, "%s\t%d\n", p.source, wait)
		return err
	case strings.Contains(input, "FLUSH BINARY LOGS"):
		p.flushes++
		p.current++
		basename := p.basename
		if basename == "" {
			basename = "/var/lib/mysql/data/binlog"
		}
		_, err := fmt.Fprintf(stdout, "%s\t%s\n", basename, binlogName(p.current))
		return err
	case strings.Contains(input, "SHOW BINARY LOGS"):
		oldest := p.oldest
		if oldest == 0 {
			oldest = 2
		}
		for index := oldest; index <= p.current; index++ {
			fmt.Fprintf(stdout, "%s\t100\tNo\n", binlogName(index))
		}
		return nil
	}
	return fmt.Errorf("unexpected exec in %s: %s %q", pod, script, input)
}

func (p *fakeMOCOPods) replay(pod, script string, stdin io.Reader) error {
	p.replays = append(p.replays, pod+": "+script)
	entries := tar.NewReader(stdin)
	for {
		header, err := entries.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p.replayed = append(p.replayed, header.Name)
	}
}

type mysqlShipLab struct {
	*mysqlAdapterLab
	store *fakeShipStore
	pods  *fakeMOCOPods
}

// newMySQLShipLab is a Standard project backed up at 00:00, 06:00 and 12:00
// (MOCO wrote the binlogs of the first two); the last backup read replica 1
// while it wrote binlog.000004, and it is 13:00.
func newMySQLShipLab(t *testing.T) *mysqlShipLab {
	t.Helper()
	lab := &mysqlShipLab{mysqlAdapterLab: newMySQLAdapterLab(t, sourceKeys()...)}
	lab.store = &fakeShipStore{fakeObjectDeleter: lab.objects}
	lab.adapter.openStore = func(context.Context, *domain.S3Credentials) (ObjectDeleter, error) { return lab.store, nil }
	lab.pods = &fakeMOCOPods{clock: &lab.now, gtid: serverA + ":1-100", source: serverB, current: 5}
	lab.adapter.SetPodStreamer(lab.pods)
	lab.liveSourceCluster(t)
	cluster := lab.mock.CRDs[sourceClusterKey]
	_ = unstructured.SetNestedField(cluster.Object, int64(3), "spec", "replicas")
	_ = unstructured.SetNestedField(cluster.Object, int64(0), "status", "currentPrimaryIndex")
	_ = unstructured.SetNestedMap(cluster.Object, map[string]interface{}{
		"time": "2026-10-11T12:00:00Z", "sourceIndex": int64(1), "sourceUUID": serverB, "binlogFilename": "binlog.000004",
	}, "status", "backup")
	return lab
}

func (lab *mysqlShipLab) shipped() string {
	return k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject) + "excalibase-binlogs/20261011-120000/"
}

func (lab *mysqlShipLab) ship(t *testing.T) {
	t.Helper()
	if err := lab.adapter.ShipBinlogs(context.Background(), lab.source); err != nil {
		t.Fatalf("ShipBinlogs: %v", err)
	}
}

func TestMySQLShipBinlogsCarriesTheWindowPastTheLastBackup(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	var shipped []string
	for _, key := range lab.store.remaining() {
		if strings.HasPrefix(key, lab.shipped()) {
			shipped = append(shipped, strings.TrimPrefix(key, lab.shipped()))
		}
	}
	if strings.Join(shipped, ",") != "binlog.000004.gz,binlog.000005.gz,shipped.json" {
		t.Fatalf("shipped %v", shipped)
	}
	body, _ := gzip.NewReader(bytes.NewReader(lab.store.data[lab.shipped()+"binlog.000005.gz"]))
	if content, _ := io.ReadAll(body); string(content) != "events of binlog.000005" {
		t.Fatalf("binlog.000005 holds %q", content)
	}
	window, err := lab.adapter.RestoreWindow(context.Background(), lab.source)
	if err != nil || window == nil || !window.Latest.Equal(at(13, 0)) {
		t.Fatalf("window %+v %v, want it to reach the ship at 13:00", window, err)
	}
	if err := lab.adapter.CheckRestoreTarget(context.Background(), lab.source, domain.RestoreRequest{TargetTime: zoned(at(12, 45))}); err != nil {
		t.Fatalf("a shipped point was refused: %v", err)
	}
	late := zoned(at(13, 0).Add(time.Second))
	if err := lab.adapter.CheckRestoreTarget(context.Background(), lab.source, domain.RestoreRequest{TargetTime: late}); !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Fatalf("a point after the last ship: %v", err)
	}
}

func TestMySQLShipOfAnIdleClusterMovesTheWindowWithoutAFlush(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	lab.now = lab.now.Add(time.Minute)
	lab.ship(t)
	if lab.pods.flushes != 1 {
		t.Fatalf("an idle cluster was flushed %d times", lab.pods.flushes)
	}
	window, _ := lab.adapter.RestoreWindow(context.Background(), lab.source)
	if window == nil || !window.Latest.Equal(at(13, 1)) {
		t.Fatalf("window %+v", window)
	}
	lab.pods.gtid = serverA + ":1-101"
	lab.now = lab.now.Add(time.Minute)
	lab.ship(t)
	if lab.pods.flushes != 2 || !containsKey(lab.store.remaining(), lab.shipped()+"binlog.000006.gz") {
		t.Fatalf("a commit was not shipped: %d flushes, %v", lab.pods.flushes, lab.store.remaining())
	}
}

func TestMySQLShipRefusesALaggingReplicaOrARebuiltSource(t *testing.T) {
	for name, breakIt := range map[string]func(*fakeMOCOPods){
		"lagging": func(p *fakeMOCOPods) { p.lagging = true },
		"rebuilt": func(p *fakeMOCOPods) { p.source = "33333333-3333-3333-3333-333333333333" },
	} {
		t.Run(name, func(t *testing.T) {
			lab := newMySQLShipLab(t)
			breakIt(lab.pods)
			if err := lab.adapter.ShipBinlogs(context.Background(), lab.source); err == nil {
				t.Fatal("want an error")
			}
			if lab.pods.flushes != 0 || containsKey(lab.store.remaining(), lab.shipped()+"shipped.json") {
				t.Fatalf("flushed %d, objects %v", lab.pods.flushes, lab.store.remaining())
			}
		})
	}
}

func TestMySQLShipSkipsAClusterWithoutABackupOrOffline(t *testing.T) {
	for name, change := range map[string]func(*unstructured.Unstructured){
		"no backup yet": func(c *unstructured.Unstructured) { unstructured.RemoveNestedField(c.Object, "status", "backup") },
		"offline":       func(c *unstructured.Unstructured) { _ = unstructured.SetNestedField(c.Object, true, "spec", "offline") },
	} {
		t.Run(name, func(t *testing.T) {
			lab := newMySQLShipLab(t)
			change(lab.mock.CRDs[sourceClusterKey])
			lab.ship(t)
			if lab.pods.flushes != 0 || containsKey(lab.store.remaining(), lab.shipped()+"shipped.json") {
				t.Fatalf("flushed %d, objects %v", lab.pods.flushes, lab.store.remaining())
			}
		})
	}
}

func TestMySQLRestorePastTheLastBackupReplaysTheShippedBinlogs(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	lab.restoreReady(t)
	if _, err := lab.adapter.Restore(context.Background(), lab.source, restoreRequest(domain.RestoreRequest{TargetTime: zoned(at(12, 45))})); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	cluster := lab.mock.CRDs[restoredNS+"/"+k8s.MySQLClusterName(restoredProject)]
	restore, _, _ := unstructured.NestedMap(cluster.Object, "spec", "restore")
	if restore["restorePoint"] != "2026-10-11T12:00:00Z" {
		t.Fatalf("MOCO restores the last backup itself, got %v", restore["restorePoint"])
	}
	if len(lab.pods.replays) != 1 || !strings.HasPrefix(lab.pods.replays[0], restoredNS+"/moco-dst-mysql-0: ") ||
		!strings.Contains(lab.pods.replays[0], "--stop-datetime='2026-10-11 12:45:00'") {
		t.Fatalf("replays %v", lab.pods.replays)
	}
	if strings.Join(lab.pods.replayed, ",") != "binlog.000004.gz,binlog.000005.gz" {
		t.Fatalf("replayed %v", lab.pods.replayed)
	}
}

func TestMySQLRestoreMOCOReachesItselfReplaysNothing(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	lab.restoreReady(t)
	if _, err := lab.adapter.Restore(context.Background(), lab.source, restoreRequest(domain.RestoreRequest{TargetTime: zoned(at(7, 30))})); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(lab.pods.replays) != 0 {
		t.Fatalf("replays %v", lab.pods.replays)
	}
}

func TestMySQLInPlaceRestorePastTheLastBackupReplaysTheShippedBinlogs(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	target, err := lab.adapter.InPlaceRecoveryTarget(context.Background(), lab.source, domain.RestoreRequest{TargetTime: zoned(at(12, 30))})
	if err != nil {
		t.Fatalf("InPlaceRecoveryTarget: %v", err)
	}
	if err := lab.adapter.ReplaceDatabase(context.Background(), lab.source, target); err != nil {
		t.Fatalf("ReplaceDatabase: %v", err)
	}
	restore, _, _ := unstructured.NestedMap(lab.mock.CRDs[sourceClusterKey].Object, "spec", "restore")
	if restore["restorePoint"] != "2026-10-11T12:00:00Z" {
		t.Fatalf("restore point %v", restore["restorePoint"])
	}
	if len(lab.pods.replays) != 1 || !strings.HasPrefix(lab.pods.replays[0], mysqlSourceNS+"/moco-proj1-mysql-") ||
		!strings.Contains(lab.pods.replays[0], "--stop-datetime='2026-10-11 12:30:00'") {
		t.Fatalf("replays %v", lab.pods.replays)
	}
}

func TestMySQLRetentionDeletesTheShippedBinlogsOfExpiredRuns(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	prefix := k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject)
	old := prefix + "excalibase-binlogs/20261011-000000/binlog.000001.gz"
	lab.store.keys = append(lab.store.keys, old)
	sort.Strings(lab.store.keys)
	days := 1
	lab.source.BackupRetentionDays = &days
	lab.now = at(7, 0).Add(24 * time.Hour)
	if _, err := lab.adapter.PurgeExpired(context.Background(), lab.source); err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	remaining := lab.store.remaining()
	if containsKey(remaining, old) || !containsKey(remaining, lab.shipped()+"shipped.json") {
		t.Fatalf("remaining %v", remaining)
	}
}

func TestMySQLBinlogSweepShipsActiveBackedUpProjectsWhileLeading(t *testing.T) {
	lab := newMySQLShipLab(t)
	store := emptyInstanceStore(t)
	paused := *lab.source
	paused.ProjectID, paused.Status = "proj2", string(domain.StatusPaused)
	for _, inst := range []*domain.DatabaseInstance{lab.source, &paused} {
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
	}
	follower := NewMySQLBinlogSweep(lab.adapter, store, NewLeadership(neverLeader{}))
	follower.Sweep(context.Background())
	if lab.pods.flushes != 0 {
		t.Fatal("a replica that does not lead shipped")
	}
	NewMySQLBinlogSweep(lab.adapter, store, NewLeadership(AlwaysLeader{})).Sweep(context.Background())
	if !containsKey(lab.store.remaining(), lab.shipped()+"shipped.json") {
		t.Fatalf("the active project was not shipped: %v", lab.store.remaining())
	}
}
