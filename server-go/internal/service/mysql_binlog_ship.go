package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// MOCO uploads a run's binlogs only at the next run, so without more a
// restore stops at the newest backup. Between runs the platform ships the
// binlogs of the newest run's source instance into that run's
// excalibase-binlogs/<run>/ directory (EXC-587). MOCO's restore ignores the
// directory: a restore past the newest run is MOCO's restore of that run
// followed by the platform replaying the shipped files to the point.
//
// A ship reads the time and the committed GTIDs on the primary, waits until
// the source instance has applied them, flushes it, uploads the closed files
// and records in the ship mark that every transaction committed before that
// time is shipped. An idle cluster is not flushed: only the time moves.

const (
	mysqlShippedDir   = "excalibase-binlogs"
	mysqlShipMarkFile = "shipped.json"
	// mysqlSourceCatchUp bounds the wait for the source to apply what the
	// primary committed; a replica further behind is retried next pass.
	mysqlSourceCatchUp = 30
)

// mysqlGTIDSet is what a GTID set printed by mysqld looks like (tags included).
var mysqlGTIDSet = regexp.MustCompile(`^[0-9A-Za-z_:,\-]*$`)

// mysqlBinlogDir is an absolute directory without shell metacharacters.
var mysqlBinlogDir = regexp.MustCompile(`^/[A-Za-z0-9/_.\-]+$`)

// PodStreamer runs a command in a pod's container with its input and output
// streamed; *k8s.Client is the production one.
type PodStreamer interface {
	StreamExecInPod(ctx context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout io.Writer) error
}

// objectReadWriter is a store the platform writes shipped binlogs to and
// reads them back from; AWSS3Uploader is one.
type objectReadWriter interface {
	Upload(ctx context.Context, bucket, key string, body io.Reader) (int64, error)
	Download(ctx context.Context, bucket, key string) (io.ReadCloser, error)
}

// mysqlShipMark is how far the binlogs shipped after a run reach: every
// transaction committed before Through is in FirstBinlog..NextBinlog-1 of
// Server, the run's backup source.
type mysqlShipMark struct {
	Run         string    `json:"run"`
	Server      string    `json:"serverUuid"`
	FirstBinlog string    `json:"firstBinlog"`
	NextBinlog  string    `json:"nextBinlog"`
	Through     time.Time `json:"through"`
	GTIDs       string    `json:"gtidExecuted"`
}

// mysqlBackupSource is MOCO's record of the newest run.
type mysqlBackupSource struct {
	Run    string
	Index  int64
	Server string
	Binlog string
}

// SetPodStreamer wires the exec the ship and the replay run through.
func (a *MySQLBackupAdapter) SetPodStreamer(s PodStreamer) { a.streamer = s }

// ShipBinlogs ships what the newest run's source instance closed since the
// last ship. A cluster that is offline or has no backup yet is skipped.
func (a *MySQLBackupAdapter) ShipBinlogs(ctx context.Context, inst *domain.DatabaseInstance) error {
	if a.streamer == nil {
		return errors.New("binlog shipping needs pod exec, which is not wired")
	}
	cluster, err := a.kube.GetCRD(ctx, k8s.MOCOClusterGVR, inst.Namespace, k8s.MySQLClusterName(inst.ProjectID))
	if err != nil {
		return fmt.Errorf("read the MySQL cluster of %s: %w", inst.ProjectID, err)
	}
	source, ok := mysqlBackupSourceOf(cluster)
	if !ok {
		return nil
	}
	store, objects, err := a.openReadWriter(ctx)
	if err != nil {
		return err
	}
	dir := mysqlShippedPrefix(inst, source.Run)
	shipped, mark, err := a.shippedSoFar(ctx, objects, store.Bucket, dir)
	if err != nil {
		return err
	}
	state, err := a.commitStateOn(ctx, inst.Namespace, k8s.MySQLPrimaryPod(cluster))
	if err != nil {
		return err
	}
	if mark != nil && mark.GTIDs == state.GTIDs {
		mark.Through = state.At
		return writeMySQLShipMark(ctx, objects, store.Bucket, dir, *mark)
	}
	target := mysqlShipTarget{namespace: inst.Namespace, pod: k8s.MySQLPodName(inst.ProjectID, source.Index),
		objects: objects, bucket: store.Bucket, dir: dir}
	next, err := a.flushSource(ctx, target.namespace, target.pod, source, state.GTIDs)
	if err != nil {
		return err
	}
	if err := a.shipClosed(ctx, target, source, next, shipped); err != nil {
		return err
	}
	return writeMySQLShipMark(ctx, objects, store.Bucket, dir, mysqlShipMark{
		Run: source.Run, Server: source.Server, FirstBinlog: source.Binlog, NextBinlog: next.file,
		Through: state.At, GTIDs: state.GTIDs,
	})
}

// mysqlBackupSourceOf reads status.backup; an offline cluster or one never
// backed up has nothing to ship.
func mysqlBackupSourceOf(cluster *unstructured.Unstructured) (mysqlBackupSource, bool) {
	if offline, _, _ := unstructured.NestedBool(cluster.Object, "spec", "offline"); offline {
		return mysqlBackupSource{}, false
	}
	raw, _, _ := unstructured.NestedString(cluster.Object, "status", "backup", "time")
	when, err := time.Parse(time.RFC3339, raw)
	if err != nil || when.IsZero() {
		return mysqlBackupSource{}, false
	}
	index, _, _ := unstructured.NestedInt64(cluster.Object, "status", "backup", "sourceIndex")
	server, _, _ := unstructured.NestedString(cluster.Object, "status", "backup", "sourceUUID")
	binlog, _, _ := unstructured.NestedString(cluster.Object, "status", "backup", "binlogFilename")
	if _, err := binlogIndex(binlog); err != nil || len(server) != hostMySQLServerIDLength {
		return mysqlBackupSource{}, false
	}
	return mysqlBackupSource{Run: when.UTC().Format(k8s.MOCOBackupTimeFormat), Index: index, Server: server, Binlog: binlog}, true
}

func mysqlShippedPrefix(inst *domain.DatabaseInstance, run string) string {
	return k8s.MOCOBackupPrefix(inst.Namespace, inst.ProjectID) + mysqlShippedDir + "/" + run + "/"
}

func (a *MySQLBackupAdapter) openReadWriter(ctx context.Context) (*domain.S3Credentials, mysqlObjectStore, error) {
	store, objects, err := a.openBucket(ctx)
	if err != nil {
		return nil, nil, err
	}
	readWriter, ok := objects.(mysqlObjectStore)
	if !ok {
		return nil, nil, errors.New("the backup store cannot write shipped binlogs")
	}
	return store, readWriter, nil
}

// mysqlObjectStore lists, writes and reads the bucket.
type mysqlObjectStore interface {
	ObjectDeleter
	objectReadWriter
}

// shippedSoFar is the files already under dir and the run's ship mark.
func (a *MySQLBackupAdapter) shippedSoFar(ctx context.Context, objects mysqlObjectStore, bucket, dir string) (map[string]bool, *mysqlShipMark, error) {
	shipped := map[string]bool{}
	var mark *mysqlShipMark
	token := ""
	for {
		keys, next, err := objects.ListKeys(ctx, bucket, dir, token, maxDeleteBatch)
		if err != nil {
			return nil, nil, fmt.Errorf("list the shipped binlogs: %w", err)
		}
		for _, key := range keys {
			file := strings.TrimPrefix(key, dir)
			if file != mysqlShipMarkFile {
				shipped[file] = true
				continue
			}
			read, err := readMySQLShipMark(ctx, objects, bucket, key)
			if err != nil {
				return nil, nil, err
			}
			mark = &read
		}
		if next == "" {
			return shipped, mark, nil
		}
		token = next
	}
}

// commitStateOn reads the server, its UTC clock and what it committed.
func (a *MySQLBackupAdapter) commitStateOn(ctx context.Context, namespace, pod string) (hostMySQLCommitState, error) {
	out, err := a.mysqlBatch(ctx, namespace, pod, hostMySQLCommitStateSQL)
	if err != nil {
		return hostMySQLCommitState{}, fmt.Errorf("read the committed transactions on %s: %w", pod, err)
	}
	state, err := parseMySQLCommitState(out)
	if err != nil {
		return hostMySQLCommitState{}, err
	}
	if !mysqlGTIDSet.MatchString(state.GTIDs) {
		return hostMySQLCommitState{}, fmt.Errorf("unexpected GTID set %q", state.GTIDs)
	}
	return state, nil
}

// mysqlShipTarget is the source pod files are read from and where they go.
type mysqlShipTarget struct {
	namespace string
	pod       string
	objects   mysqlObjectStore
	bucket    string
	dir       string
}

// flushedSource is the source after its flush: where its binlogs are and
// the file the flush opened.
type flushedSource struct {
	dir  string
	file string
}

// flushSource waits until the source has applied gtids, then closes its
// binlog. The source must still be the server MOCO backed up.
func (a *MySQLBackupAdapter) flushSource(ctx context.Context, namespace, pod string, source mysqlBackupSource, gtids string) (flushedSource, error) {
	out, err := a.mysqlBatch(ctx, namespace, pod,
		fmt.Sprintf("SELECT @@server_uuid, WAIT_FOR_EXECUTED_GTID_SET('%s', %d);\n", gtids, mysqlSourceCatchUp))
	if err != nil {
		return flushedSource{}, fmt.Errorf("wait for %s to apply the committed transactions: %w", pod, err)
	}
	fields := strings.Fields(out)
	switch {
	case len(fields) != 2:
		return flushedSource{}, fmt.Errorf("unexpected answer from %s: %q", pod, strings.TrimSpace(out))
	case fields[0] != source.Server:
		return flushedSource{}, fmt.Errorf("%s is no longer the server backup %s was taken from; shipping resumes after the next backup", pod, source.Run)
	case fields[1] != "0":
		return flushedSource{}, fmt.Errorf("%s did not apply the committed transactions within %ds", pod, mysqlSourceCatchUp)
	}
	out, err = a.mysqlBatch(ctx, namespace, pod, "FLUSH BINARY LOGS;\n"+
		"SELECT @@log_bin_basename, JSON_UNQUOTE(JSON_EXTRACT(LOCAL, '$.binary_log_file')) FROM performance_schema.log_status;\n")
	if err != nil {
		return flushedSource{}, fmt.Errorf("flush the binlogs on %s: %w", pod, err)
	}
	fields = strings.Fields(out)
	if len(fields) != 2 || !mysqlBinlogDir.MatchString(fields[0]) {
		return flushedSource{}, fmt.Errorf("unexpected binlog state on %s: %q", pod, strings.TrimSpace(out))
	}
	if _, err := binlogIndex(fields[1]); err != nil {
		return flushedSource{}, err
	}
	return flushedSource{dir: path.Dir(fields[0]), file: fields[1]}, nil
}

// shipClosed uploads, gzipped, the source's files from the run's binlog up
// to the one the flush opened that are not shipped yet.
func (a *MySQLBackupAdapter) shipClosed(ctx context.Context, target mysqlShipTarget, source mysqlBackupSource, flushed flushedSource,
	shipped map[string]bool) error {
	out, err := a.mysqlBatch(ctx, target.namespace, target.pod, "SHOW BINARY LOGS;\n")
	if err != nil {
		return fmt.Errorf("list the binlogs on %s: %w", target.pod, err)
	}
	present := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		file, _, _ := strings.Cut(line, "\t")
		present[file] = true
	}
	files, err := binlogRange(source.Binlog, flushed.file)
	if err != nil {
		return err
	}
	for _, file := range files {
		if shipped[file+".gz"] {
			continue
		}
		if !present[file] {
			return fmt.Errorf("%s no longer holds %s; shipping resumes after the next backup", target.pod, file)
		}
		if err := a.shipFile(ctx, target, flushed.dir+"/"+file, target.dir+file+".gz"); err != nil {
			return err
		}
	}
	return nil
}

// shipFile streams one file out of the pod, gzipped, into key.
func (a *MySQLBackupAdapter) shipFile(ctx context.Context, target mysqlShipTarget, file, key string) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		compressed := gzip.NewWriter(writer)
		err := a.streamer.StreamExecInPod(ctx, target.namespace, target.pod, provisioner.MOCOMySQLContainer, []string{"cat", file}, nil, compressed)
		err = errors.Join(err, compressed.Close())
		writer.CloseWithError(err)
		done <- err
	}()
	_, uploadErr := target.objects.Upload(ctx, target.bucket, key, reader)
	_ = reader.CloseWithError(uploadErr)
	if err := <-done; err != nil {
		return fmt.Errorf("read %s on %s: %w", file, target.pod, err)
	}
	if uploadErr != nil {
		return fmt.Errorf("upload %s: %w", key, uploadErr)
	}
	return nil
}

// mysqlBatch runs statements on a pod's mysqld as MOCO's admin over the socket.
func (a *MySQLBackupAdapter) mysqlBatch(ctx context.Context, namespace, pod, statements string) (string, error) {
	var out bytes.Buffer
	cmd := []string{"mysql", "--defaults-extra-file=" + provisioner.MOCOAdminDefaultsFile, "--batch", "--skip-column-names"}
	if err := a.streamer.StreamExecInPod(ctx, namespace, pod, provisioner.MOCOMySQLContainer, cmd, strings.NewReader(statements), &out); err != nil {
		return "", err
	}
	return out.String(), nil
}

func readMySQLShipMark(ctx context.Context, objects objectReadWriter, bucket, key string) (mysqlShipMark, error) {
	body, err := objects.Download(ctx, bucket, key)
	if err != nil {
		return mysqlShipMark{}, fmt.Errorf("read ship mark %s: %w", key, err)
	}
	defer body.Close()
	var mark mysqlShipMark
	if err := json.NewDecoder(io.LimitReader(body, 1<<16)).Decode(&mark); err != nil {
		return mysqlShipMark{}, fmt.Errorf("read ship mark %s: %w", key, err)
	}
	if _, err := binlogRange(mark.FirstBinlog, mark.NextBinlog); err != nil || mark.Through.IsZero() {
		return mysqlShipMark{}, fmt.Errorf("ship mark %s is incomplete", key)
	}
	return mark, nil
}

func writeMySQLShipMark(ctx context.Context, objects objectReadWriter, bucket, dir string, mark mysqlShipMark) error {
	body, err := json.Marshal(mark)
	if err != nil {
		return err
	}
	if _, err := objects.Upload(ctx, bucket, dir+mysqlShipMarkFile, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("write the ship mark of run %s: %w", mark.Run, err)
	}
	return nil
}
