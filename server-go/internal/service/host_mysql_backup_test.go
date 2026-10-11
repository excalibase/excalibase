package service

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const hostBucket = "excalibase-backups"

// fakeStreamer answers streamed execs: a dump for the dump script, nothing
// for the others, and remembers every script it ran.
type fakeStreamer struct {
	mu      sync.Mutex
	scripts []string
	dump    string
	failOn  string
}

func (f *fakeStreamer) StreamExec(_ context.Context, container string, cmd []string, dst io.Writer) error {
	script := strings.Join(cmd, " ")
	f.mu.Lock()
	f.scripts = append(f.scripts, container+": "+script)
	f.mu.Unlock()
	if f.failOn != "" && strings.Contains(script, f.failOn) {
		return errors.New("exited 2: " + f.failOn + " failed")
	}
	if strings.Contains(script, "mysqldump") {
		_, err := io.WriteString(dst, f.dump)
		return err
	}
	return nil
}

func (f *fakeStreamer) ran(fragment string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matched []string
	for _, script := range f.scripts {
		if strings.Contains(script, fragment) {
			matched = append(matched, script)
		}
	}
	return matched
}

type hostBackupFixture struct {
	adapter  *HostMySQLBackupAdapter
	engine   *hostEngine
	streamer *fakeStreamer
	store    *fakeS3Uploader
	records  *fakeBackupRecordStore
	inst     *domain.DatabaseInstance
	clock    *time.Time
	server   string
	current  int
}

// newHostBackupFixture is a running MySQL project whose server reports
// serverA and binlogs 3..current, current being the one a flush opened.
func newHostBackupFixture(t *testing.T) *hostBackupFixture {
	t.Helper()
	f := &hostBackupFixture{
		engine: newHostEngine(), streamer: &fakeStreamer{dump: "-- dump"}, store: newFakeS3Uploader(),
		records: &fakeBackupRecordStore{}, server: serverA, current: 5,
	}
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	f.clock = &now
	mysql := provisioner.NewDockerMySQLProvisioner(f.engine)
	poll := provisioner.NewPoller(time.Millisecond, 50*time.Millisecond)
	mysql.SetPollers(poll, poll)
	instances := emptyInstanceStore(t)
	f.adapter = NewHostMySQLBackupAdapter(HostMySQLBackupAdapterConfig{
		Docker: f.engine, MySQL: mysql, Streamer: f.streamer, Uploader: f.store, Records: f.records,
		Bucket: hostBucket, KeyPrefix: "backups/", Instances: instances,
	})
	f.adapter.SetClock(func() time.Time { return *f.clock })
	f.engine.answer = f.answer
	port := 3306
	f.inst = &domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "org1", DBType: domain.MySQL, DeploymentMode: domain.ModeDocker, Tier: domain.Free,
		Namespace: "excalibase-p1-mysql", Host: "excalibase-p1-mysql", Port: &port, DatabaseName: "shop",
		MySQLVersion: config.DefaultMySQLVersion(), BackupEnabled: boolPtr(true), BackupRetentionDays: intPtr(7),
		Status: string(domain.StatusActive),
	}
	if err := instances.Create(f.inst); err != nil {
		t.Fatal(err)
	}
	f.engine.containers["excalibase-p1-mysql"] = true
	f.setBinlogs("excalibase-p1-mysql")
	return f
}

func (f *hostBackupFixture) answer(_ string, stdin string) (string, error) {
	switch {
	case strings.Contains(stdin, "FLUSH BINARY LOGS"):
		f.current++
		return f.server + "\t" + binlogName(f.current) + "\n", nil
	case strings.TrimSpace(stdin) == "SELECT @@server_uuid;":
		return f.server + "\n", nil
	case strings.Contains(stdin, "SHOW BINARY LOGS"):
		var lines []string
		for index := 3; index <= f.current; index++ {
			lines = append(lines, binlogName(index)+"\t100\tNo")
		}
		return strings.Join(lines, "\n") + "\n", nil
	}
	return "", nil
}

func (f *hostBackupFixture) setBinlogs(container string) {
	for index := 1; index < 40; index++ {
		f.engine.files[container+":/var/lib/mysql/"+binlogName(index)] = []byte("events of " + binlogName(index))
	}
}

func (f *hostBackupFixture) tick(d time.Duration) { *f.clock = f.clock.Add(d) }

func (f *hostBackupFixture) keys(prefix string) []string {
	objects, _ := f.store.List(context.Background(), hostBucket, prefix)
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Key)
	}
	slices.Sort(keys)
	return keys
}

func TestHostMySQLBackupShipsTheDumpAndTheClosedBinlogs(t *testing.T) {
	f := newHostBackupFixture(t)
	ref, err := f.adapter.TriggerManual(context.Background(), f.inst)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	if ref.ID != "20261011-100000" || ref.Status != backupStatusCompleted || ref.Type != backupTypeManual {
		t.Fatalf("ref %+v", ref)
	}
	run := "backups/p1/mysql/runs/20261011-100000-" + serverA + "/"
	want := []string{
		"backups/p1/mysql/binlogs/" + serverA + "/binlog.000003.gz",
		"backups/p1/mysql/binlogs/" + serverA + "/binlog.000004.gz",
		"backups/p1/mysql/binlogs/" + serverA + "/binlog.000005.gz",
		run + "dump.sql.gz", run + "manifest.json",
	}
	if got := f.keys("backups/p1/"); !slices.Equal(got, want) {
		t.Fatalf("objects\n%v\nwant\n%v", got, want)
	}
	manifest := string(f.store.objects[hostBucket+"/"+run+"manifest.json"])
	if !strings.Contains(manifest, `"firstBinlog":"binlog.000006"`) || !strings.Contains(manifest, serverA) {
		t.Fatalf("manifest %s", manifest)
	}
	dumps := f.streamer.ran("mysqldump")
	if len(dumps) != 1 || !strings.Contains(dumps[0], "--single-transaction") || !strings.Contains(dumps[0], "--set-gtid-purged=ON") ||
		!strings.Contains(dumps[0], "SHOW CREATE USER") {
		t.Fatalf("dump script %v", dumps)
	}
	refs, err := f.adapter.List(context.Background(), f.inst)
	if err != nil || len(refs) != 1 || refs[0].Status != backupStatusCompleted || refs[0].Type != backupTypeManual || refs[0].SizeBytes == 0 {
		t.Fatalf("list %+v %v", refs, err)
	}
}

func TestHostMySQLBackupShipsEachBinlogOnce(t *testing.T) {
	f := newHostBackupFixture(t)
	if _, err := f.adapter.TriggerManual(context.Background(), f.inst); err != nil {
		t.Fatal(err)
	}
	f.tick(time.Hour)
	if _, err := f.adapter.RunScheduled(context.Background(), f.inst); err != nil {
		t.Fatal(err)
	}
	binlogs := f.keys("backups/p1/mysql/binlogs/")
	if len(binlogs) != 4 || !strings.HasSuffix(binlogs[3], "binlog.000006.gz") {
		t.Fatalf("binlogs %v", binlogs)
	}
	refs, _ := f.adapter.List(context.Background(), f.inst)
	if len(refs) != 2 || refs[1].Type != backupTypeScheduled {
		t.Fatalf("refs %+v", refs)
	}
}

func TestHostMySQLBackupOfAStoppedProjectIsRefused(t *testing.T) {
	f := newHostBackupFixture(t)
	f.engine.containers["excalibase-p1-mysql"] = false
	if _, err := f.adapter.TriggerManual(context.Background(), f.inst); err == nil || !errors.Is(err, ErrHostMySQLNotRunning) {
		t.Fatalf("err %v", err)
	}
	if len(f.keys("backups/")) != 0 || len(f.records.records) != 0 {
		t.Fatal("a refused backup left objects or a record")
	}
}

func TestHostMySQLFailedDumpLeavesNoRunAndIsRecordedFailed(t *testing.T) {
	f := newHostBackupFixture(t)
	f.streamer.failOn = "mysqldump"
	if _, err := f.adapter.TriggerManual(context.Background(), f.inst); err == nil {
		t.Fatal("want failure")
	}
	if keys := f.keys("backups/p1/mysql/runs/"); len(keys) != 0 {
		t.Fatalf("a failed run left %v", keys)
	}
	refs, _ := f.adapter.List(context.Background(), f.inst)
	if len(refs) != 1 || refs[0].Status != backupStatusFailed {
		t.Fatalf("refs %+v", refs)
	}
}

func TestHostMySQLBackupIsDueByPlanIntervalAndAfterTheServerChanged(t *testing.T) {
	f := newHostBackupFixture(t)
	ctx := context.Background()
	if due, err := f.adapter.BackupDue(ctx, f.inst); err != nil || !due {
		t.Fatalf("a project without a backup is due: %v %v", due, err)
	}
	if _, err := f.adapter.RunScheduled(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.tick(5 * time.Hour)
	if due, _ := f.adapter.BackupDue(ctx, f.inst); due {
		t.Fatal("a Free project is backed up every 6 hours")
	}
	f.tick(time.Hour)
	if due, _ := f.adapter.BackupDue(ctx, f.inst); !due {
		t.Fatal("6 hours later a Free project is due")
	}
	f.inst.Tier = domain.Standard
	f.tick(-5 * time.Hour)
	if due, _ := f.adapter.BackupDue(ctx, f.inst); !due {
		t.Fatal("a Standard project is backed up every hour")
	}
	f.inst.Tier = domain.Free
	f.server = serverB
	if due, _ := f.adapter.BackupDue(ctx, f.inst); !due {
		t.Fatal("a replaced server has no backup of its own yet")
	}
}

func TestHostMySQLRestoreToANewProjectReplaysToThePoint(t *testing.T) {
	f := newHostBackupFixture(t)
	ctx := context.Background()
	if _, err := f.adapter.TriggerManual(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.tick(time.Hour)
	if _, err := f.adapter.RunScheduled(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	instances := f.adapter.instances
	registrar := &fakeRegistrar{store: instances}
	f.adapter.SetProjectRegistrar(registrar)
	f.adapter.SetDatabaseProbe(alwaysAnswers{})
	plans := &fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: config.TierConfig{Instances: 1, Memory: "512Mi", CPU: "1", StorageSize: "5Gi"},
		Backup: &domain.BackupSettings{Enabled: true, Retention: 7}}}
	f.adapter.SetRestorePlanSource(plans)
	f.setBinlogs("excalibase-copy-mysql")
	target := &domain.ZonedTime{Time: time.Date(2026, 10, 11, 10, 30, 15, 0, time.UTC)}
	resp, err := f.adapter.Restore(ctx, f.inst, domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "copy", TargetTime: target})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if resp.ProjectID != "copy" || resp.Host != "excalibase-copy-mysql" {
		t.Fatalf("resp %+v", resp)
	}
	copied := strings.Join(f.engine.copied["excalibase-copy-mysql"], ",")
	if !strings.Contains(copied, "excalibase-restore/dump.sql.gz") || !strings.Contains(copied, "excalibase-restore/binlog.000006.gz") ||
		strings.Contains(copied, "binlog.000005") || strings.Contains(copied, "binlog.000007") {
		t.Fatalf("copied %s", copied)
	}
	loads := f.streamer.ran("excalibase-copy-mysql: ")
	if len(loads) != 1 || !strings.Contains(loads[0], "RESET BINARY LOGS AND GTIDS") ||
		!strings.Contains(loads[0], "--stop-datetime='2026-10-11 10:30:15'") {
		t.Fatalf("load %v", loads)
	}
	row, _ := instances.FindByProjectID("copy")
	if row == nil || row.DeploymentMode != domain.ModeDocker || row.DBType != domain.MySQL || row.Namespace != "excalibase-copy-mysql" ||
		row.BackupEnabled == nil || !*row.BackupEnabled || row.BackupSchedule != "0 */6 * * *" || row.RestoredFromProjectID != "p1" {
		t.Fatalf("row %+v", row)
	}
	if len(registrar.calls) != 1 {
		t.Fatalf("registered %d times", len(registrar.calls))
	}
}

func TestHostMySQLRestoreOutsideTheWindowCreatesNothing(t *testing.T) {
	f := newHostBackupFixture(t)
	ctx := context.Background()
	if _, err := f.adapter.TriggerManual(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.adapter.SetProjectRegistrar(&fakeRegistrar{})
	f.adapter.SetDatabaseProbe(alwaysAnswers{})
	f.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: config.TierConfig{Instances: 1, Memory: "1Gi", CPU: "1"}}})
	late := &domain.ZonedTime{Time: f.clock.Add(time.Minute)}
	_, err := f.adapter.Restore(ctx, f.inst, domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "copy", TargetTime: late})
	if !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Fatalf("err %v", err)
	}
	if _, exists := f.engine.containers["excalibase-copy-mysql"]; exists {
		t.Fatal("a container was created for a refused restore")
	}
	if err := f.adapter.CheckRestoreTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: late}); !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Fatalf("check %v", err)
	}
	window, err := f.adapter.RestoreWindow(ctx, f.inst)
	if err != nil || window == nil || !window.Latest.Equal(*f.clock) {
		t.Fatalf("window %+v %v", window, err)
	}
}

func TestHostMySQLInPlaceRestoreRebuildsTheContainerUnderItsName(t *testing.T) {
	f := newHostBackupFixture(t)
	ctx := context.Background()
	if _, err := f.adapter.TriggerManual(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: config.TierConfig{Instances: 1, Memory: "1Gi", CPU: "1"}}})
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{BackupID: "20261011-100000"})
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	f.tick(time.Minute)
	safety, err := f.adapter.TakeSafetyBackup(ctx, f.inst)
	if err != nil || safety != "20261011-100100" {
		t.Fatalf("safety %q %v", safety, err)
	}
	f.engine.copied = map[string][]string{}
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); err != nil {
		t.Fatalf("ReplaceDatabase: %v", err)
	}
	if !f.engine.containers["excalibase-p1-mysql"] {
		t.Fatal("the rebuilt container is not running")
	}
	copied := strings.Join(f.engine.copied["excalibase-p1-mysql"], ",")
	if !strings.Contains(copied, "dump.sql.gz") || strings.Contains(copied, "binlog") {
		t.Fatalf("a backup-time restore loads the dump alone: %s", copied)
	}
}

func TestHostMySQLRetentionDeletesOldRunsAndTheirBinlogs(t *testing.T) {
	f := newHostBackupFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := f.adapter.RunScheduled(ctx, f.inst); err != nil {
			t.Fatal(err)
		}
		f.tick(4 * 24 * time.Hour)
	}
	// Runs at day 0, 4 and 8; now day 12 with a 7-day retention: the day-0
	// run goes, the day-4 run stays because it reaches back to the cutoff.
	deleted, err := f.adapter.PurgeExpired(ctx, f.inst)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	runs := f.keys("backups/p1/mysql/runs/")
	if len(runs) != 4 || deleted == 0 {
		t.Fatalf("runs %v deleted %d", runs, deleted)
	}
	binlogs := f.keys("backups/p1/mysql/binlogs/")
	for _, key := range binlogs {
		if strings.HasSuffix(key, "binlog.000003.gz") || strings.HasSuffix(key, "binlog.000005.gz") {
			t.Fatalf("binlogs before the oldest kept run's dump stayed: %v", binlogs)
		}
	}
}

var _ storage.BackupRecordStore = (*fakeBackupRecordStore)(nil)

func TestHostMySQLRestoreOfARunWithoutItsDumpFails(t *testing.T) {
	f := newHostBackupFixture(t)
	ctx := context.Background()
	if _, err := f.adapter.TriggerManual(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	delete(f.store.objects, hostBucket+"/backups/p1/mysql/runs/20261011-100000-"+serverA+"/dump.sql.gz")
	if _, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{BackupID: "20261011-100000"}); err == nil {
		t.Fatal("a run without its dump was accepted")
	}
}
