package provisioner

import (
	"archive/tar"
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func archivedRequest(name string) domain.ProvisioningRequest {
	return domain.ProvisioningRequest{ProjectName: name, DBType: domain.PostgreSQL, PostgresVersion: "17",
		Backup: &domain.BackupSettings{Enabled: true, Schedule: "0 2 * * *", Retention: 7}}
}

// archiveDirIn is the /walarchive entry a CopyToContainer at / carried.
func archiveDirIn(t *testing.T, stream []byte) *tar.Header {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(stream))
	for {
		hdr, err := reader.Next()
		if err != nil {
			return nil
		}
		if strings.TrimSuffix(hdr.Name, "/") == "walarchive" {
			return hdr
		}
	}
}

// EXC-593: archive_command copies into /walarchive, which the image does not
// have; the provisioner creates it for the server's user before archiving.
func TestDockerPostgresBackedUpProjectArchivesIntoADirectoryItsServerOwns(t *testing.T) {
	docker := newMockDocker()
	if _, err := NewDockerPostgreSQLProvisioner(docker).Provision(context.Background(), archivedRequest("shop"), testTier, func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	dir := archiveDirIn(t, docker.copied["/"])
	if dir == nil || dir.Typeflag != tar.TypeDir || dir.Uid != 999 || dir.Gid != 999 || dir.Mode&0o777 != 0o700 {
		t.Fatalf("archive directory %+v", dir)
	}
	var alter string
	for _, cmd := range docker.execLog {
		if joined := strings.Join(cmd, " "); strings.Contains(joined, "archive_command") {
			alter = joined
		}
	}
	// A segment is renamed into place, so a sweep never ships half of one.
	if !strings.Contains(alter, "cp %p /walarchive/%f.part && mv /walarchive/%f.part /walarchive/%f") || !strings.Contains(alter, "archive_timeout") {
		t.Fatalf("archive settings: %s", alter)
	}
}

// Backups a project must have are not left half-configured behind a warning.
func TestDockerPostgresRefusesAProjectWhoseArchiveCannotBeConfigured(t *testing.T) {
	docker := newMockDocker()
	docker.execCode = 1
	_, err := NewDockerPostgreSQLProvisioner(docker).Provision(context.Background(), archivedRequest("shop"), testTier, func(domain.ProvisioningStage) {})
	if err == nil || !strings.Contains(err.Error(), "WAL archiving") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerPostgresWithoutBackupsDoesNotArchive(t *testing.T) {
	docker := newMockDocker()
	req := archivedRequest("shop")
	req.Backup = nil
	if _, err := NewDockerPostgreSQLProvisioner(docker).Provision(context.Background(), req, testTier, func(domain.ProvisioningStage) {}); err != nil {
		t.Fatal(err)
	}
	if archiveDirIn(t, docker.copied["/"]) != nil {
		t.Fatal("archive directory created for a project without backups")
	}
}

// ALTER SYSTEM cannot run inside a transaction block, and psql runs one -c
// string as a single transaction: each setting goes in a -c of its own.
func TestDockerPostgresSetsEachArchiveSettingOutsideATransaction(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "running"
	if err := NewDockerPostgreSQLProvisioner(docker).ConfigureArchive(context.Background(), "c1", "cp %p /x/%f", "postgres"); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range docker.execLog {
		statements := 0
		for i, arg := range cmd {
			if arg == "-c" {
				statements++
				if strings.Count(cmd[i+1], "ALTER SYSTEM") > 1 {
					t.Fatalf("settings batched in one transaction: %q", cmd[i+1])
				}
			}
		}
		if strings.Contains(strings.Join(cmd, " "), "ALTER SYSTEM") && statements != 4 {
			t.Fatalf("%d statements: %v", statements, cmd)
		}
	}
}

func TestDockerPostgresArchiveStopsAtTheFirstFailure(t *testing.T) {
	for _, step := range []string{"copy", "stop", "start"} {
		docker := newMockDocker()
		docker.containers["c1"] = "running"
		docker.failOn = step
		p := NewDockerPostgreSQLProvisioner(docker)
		p.SetStartPoller(NewPoller(time.Millisecond, time.Millisecond))
		if err := p.EnableWALArchive(context.Background(), "c1", PostgresImageOwner); err == nil {
			t.Errorf("%s failure swallowed", step)
		}
	}
	if got := quoteSQLString("it's"); got != "'it''s'" {
		t.Errorf("quoted %s", got)
	}
}
