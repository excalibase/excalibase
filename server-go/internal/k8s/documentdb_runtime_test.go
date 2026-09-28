package k8s

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func clusterSpec(opts PostgreSQLClusterOpts) map[string]interface{} {
	return BuildPostgreSQLCluster(opts).Object["spec"].(map[string]interface{})
}

func altNames(spec map[string]interface{}) []string {
	certificates, _ := spec["certificates"].(map[string]interface{})
	raw, _ := certificates["serverAltDNSNames"].([]interface{})
	names := make([]string, 0, len(raw))
	for _, name := range raw {
		names = append(names, name.(string))
	}
	return names
}

func TestDocumentDBCertificateNamesTheGatewayService(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.ServerAltDNSNames = []string{"proj-hba00001.db.example.com"}

	got := altNames(clusterSpec(opts))
	want := []string{
		"proj-hba00001.db.example.com",
		"proj-hba00001-documentdb",
		"proj-hba00001-documentdb.org-a-proj-hba00001",
		"proj-hba00001-documentdb.org-a-proj-hba00001.svc",
		"proj-hba00001-documentdb.org-a-proj-hba00001.svc.cluster.local",
	}
	if !slices.Equal(got, want) {
		t.Errorf("serverAltDNSNames:\n got %q\nwant %q", got, want)
	}
}

func TestAPlainProjectCertificateGetsNoGatewayNames(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.DocumentDB = false
	if got := altNames(clusterSpec(opts)); len(got) != 0 {
		t.Errorf("serverAltDNSNames: %q", got)
	}
}

// Background work connects back over the unix socket, where CNPG's peer mapping applies.
func TestDocumentDBBackgroundWorkUsesTheUnixSocket(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.Parameters = map[string]string{"cron.host": "localhost", documentDBLocalhostSetting: "host=localhost"}
	params := postgresqlSection(t, opts)["parameters"].(map[string]interface{})

	if got := params["cron.host"]; got != cnpgSocketDirectory {
		t.Errorf("cron.host: got %v", got)
	}
	if got := params[documentDBLocalhostSetting]; got != "host="+cnpgSocketDirectory {
		t.Errorf("%s: got %v", documentDBLocalhostSetting, got)
	}
}

func TestDocumentDBPeerMapsOnlyTheGatewayRoles(t *testing.T) {
	raw, _ := postgresqlSection(t, documentDBOpts("owner_doc"))["pg_ident"].([]interface{})
	got := make([]string, 0, len(raw))
	for _, line := range raw {
		got = append(got, line.(string))
	}
	want := []string{
		"local postgres documentdb_bg_worker_role",
		"local postgres documentdb",
		"local postgres owner_doc",
		"local postgres excalibase_app",
	}
	if !slices.Equal(got, want) {
		t.Errorf("pg_ident:\n got %q\nwant %q", got, want)
	}
}

func TestAPlainProjectHasNoDocumentDBRuntimeSettings(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.DocumentDB = false
	postgresql := postgresqlSection(t, opts)
	params := postgresql["parameters"].(map[string]interface{})
	for _, key := range []string{"cron.host", documentDBLocalhostSetting} {
		if _, set := params[key]; set {
			t.Errorf("a plain project sets %s", key)
		}
	}
	if _, set := postgresql["pg_ident"]; set {
		t.Error("a plain project maps peer identities")
	}
	spec := clusterSpec(opts)
	if _, set := spec["stopDelay"]; set {
		t.Error("a plain project changes the stop delay")
	}
}

// The gateway sidecar ignores SIGTERM, so a pod otherwise waits out the whole grace period.
func TestDocumentDBPodsStopWithinAMinute(t *testing.T) {
	spec := clusterSpec(documentDBOpts("owner_doc"))
	if spec["stopDelay"] != int64(documentDBStopDelaySeconds) || spec["smartShutdownTimeout"] != int64(documentDBSmartShutdownSeconds) {
		t.Errorf("stopDelay=%v smartShutdownTimeout=%v", spec["stopDelay"], spec["smartShutdownTimeout"])
	}
	if documentDBSmartShutdownSeconds >= documentDBStopDelaySeconds || documentDBStopDelaySeconds > 60 {
		t.Errorf("shutdown budget: smart %d, stop %d", documentDBSmartShutdownSeconds, documentDBStopDelaySeconds)
	}
}

func TestForceDeleteClusterPodsRemovesOnlyThatClustersInstances(t *testing.T) {
	pod := func(name, cluster string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: map[string]string{"cnpg.io/cluster": cluster}}}
	}
	client := &Client{clientset: fake.NewSimpleClientset(pod("a-1", "a-postgres"), pod("b-1", "b-postgres"))}

	if err := client.ForceDeleteClusterPods(context.Background(), "ns", "a-postgres"); err != nil {
		t.Fatalf("ForceDeleteClusterPods: %v", err)
	}
	pods, _ := client.clientset.CoreV1().Pods("ns").List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 1 || pods.Items[0].Name != "b-1" {
		t.Errorf("pods left: %v", pods.Items)
	}
}

func TestMockRecordsForcedPodDeletion(t *testing.T) {
	mock := NewMockClient()
	if err := mock.ForceDeleteClusterPods(context.Background(), "ns", "a-postgres"); err != nil {
		t.Fatalf("force delete: %v", err)
	}
	if !slices.Contains(mock.Calls, "ForceDeleteClusterPods:ns/a-postgres") {
		t.Errorf("calls: %v", mock.Calls)
	}
	mock.ForceDeletePodsError = context.Canceled
	if err := mock.ForceDeleteClusterPods(context.Background(), "ns", "a-postgres"); err != context.Canceled {
		t.Errorf("error: %v", err)
	}
}

func TestForceDeleteClusterPodsReportsFailures(t *testing.T) {
	for _, verb := range []string{"list", "delete"} {
		t.Run(verb, func(t *testing.T) {
			clientset := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "a-1", Namespace: "ns", Labels: map[string]string{"cnpg.io/cluster": "a-postgres"}}})
			clientset.PrependReactor(verb, "pods", failReactor("boom"))
			client := &Client{clientset: clientset}
			if err := client.ForceDeleteClusterPods(context.Background(), "ns", "a-postgres"); err == nil {
				t.Fatal("the failure was swallowed")
			}
		})
	}
}
