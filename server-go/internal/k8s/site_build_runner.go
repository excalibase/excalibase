package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	// siteBuildLogTailLines and siteBuildLogStepBytes bound what one step keeps in the build history.
	siteBuildLogTailLines = 4000
	siteBuildLogStepBytes = 128 << 10
)

// What a failed apply names.
const (
	siteBuildQuotaKind   = "build quota"
	siteBuildScriptsKind = "build scripts"
)

// siteBuildLogSteps are the steps whose output the history keeps, in order.
var siteBuildLogSteps = []buildLogStep{
	{siteBuildFence, "network fence"}, {siteBuildClone, "clone"}, {siteBuildBuild, "build"}, {siteBuildCollect, "collect"},
}

type streamFunc func(ctx context.Context, namespace, pod, container string, command []string, out io.Writer) error

// SiteBuildRunner runs site builds as Jobs in the build namespace (EXC-354).
type SiteBuildRunner struct {
	*buildJobs
	options SiteBuildOptions
}

var _ sitebuilds.Runner = (*SiteBuildRunner)(nil)

func NewSiteBuildRunner(client *Client, options SiteBuildOptions) *SiteBuildRunner {
	return &SiteBuildRunner{options: options, buildJobs: &buildJobs{
		client: client, namespace: options.Namespace, prefix: "site-build-", secretSuffixes: []string{"-env", cloneSecretSuffix},
		steps: siteBuildLogSteps, collectStep: siteBuildCollect, archivePath: siteBuildArchivePath, stream: client.execStream, component: siteBuildComponent, kindOf: stepFailureKind,
	}}
}

// Start puts the fence, quota and scripts in place, creates the Job suspended,
// then its Secrets owned by it, then resumes it.
func (r *SiteBuildRunner) Start(ctx context.Context, spec sitebuilds.RunSpec) error {
	if err := checkSiteBuildSizes(spec); err != nil {
		return err
	}
	if err := r.ensureEnvironment(ctx, spec.LargestPlan); err != nil {
		return err
	}
	env, git := renderSiteBuildSecrets(r.options, spec)
	return r.create(ctx, spec.BuildID, renderSiteBuildJob(r.options, spec), []*corev1.Secret{env, git})
}

// checkSiteBuildSizes refuses sizes the renderers would otherwise panic on.
func checkSiteBuildSizes(spec sitebuilds.RunSpec) error {
	for name, value := range map[string]string{"memory": spec.Memory, "disk": spec.Disk,
		"largest plan's memory": spec.LargestPlan.Memory, "largest plan's disk": spec.LargestPlan.Disk} {
		if _, err := resource.ParseQuantity(value); err != nil {
			return fmt.Errorf("the build's %s %q is not a size: %w", name, value, err)
		}
	}
	return nil
}

// ensureEnvironment converges the namespace's shared objects on every start, so an edit cannot loosen them for long.
func (r *SiteBuildRunner) ensureEnvironment(ctx context.Context, largest sitebuilds.BuildSize) error {
	namespace := r.options.Namespace
	if err := buildNamespaceReady(ctx, r.client, namespace); err != nil {
		return err
	}
	if err := r.client.applyNetworkPolicy(ctx, renderSiteBuildEgressPolicy(r.options)); err != nil {
		return fmt.Errorf("build egress policy: %w", err)
	}
	if err := applyBuildQuota(ctx, r.client, renderSiteBuildQuota(r.options, largest)); err != nil {
		return err
	}
	scripts, err := renderSiteBuildScripts(namespace)
	if err != nil {
		return err
	}
	return applyBuildConfigMap(ctx, r.client, scripts)
}

// cappedWriter refuses a write past its limit instead of truncating silently.
type cappedWriter struct {
	out        io.Writer
	limit      int64
	written    int64
	overflowed bool
}

func (c *cappedWriter) Write(chunk []byte) (int, error) {
	if c.written+int64(len(chunk)) > c.limit {
		c.overflowed = true
		return 0, sitebuilds.ErrOutputTooLarge
	}
	written, err := c.out.Write(chunk)
	c.written += int64(written)
	return written, err
}

// tailWriter keeps at most twice limit bytes while writing, and the last limit when read.
type tailWriter struct {
	limit int
	data  []byte
	cut   bool
}

func (w *tailWriter) Write(chunk []byte) (int, error) {
	w.data = append(w.data, chunk...)
	if len(w.data) > 2*w.limit {
		w.data = append([]byte{}, w.data[len(w.data)-w.limit:]...)
		w.cut = true
	}
	return len(chunk), nil
}

func (w *tailWriter) String() string {
	text := keepTail(string(w.data), w.limit)
	if w.cut && !strings.HasPrefix(text, "…") {
		return "…" + text
	}
	return text
}

// keepTail keeps the last limit bytes, marking the cut.
func keepTail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return "…" + text[len(text)-limit:]
}

// execStream runs a command in a container and streams its stdout into out.
func (c *Client) execStream(ctx context.Context, namespace, pod, container string, command []string, out io.Writer) error {
	request := c.clientset.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").
		Param("container", container).Param("stdout", "true").Param("stderr", "true")
	for _, part := range command {
		request = request.Param("command", part)
	}
	executor, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("create executor: %w", err)
	}
	var stderr strings.Builder
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: out, Stderr: &stderr}); err != nil {
		if errors.Is(err, sitebuilds.ErrOutputTooLarge) {
			return err
		}
		return fmt.Errorf("exec: %w (stderr: %s)", err, keepTail(stderr.String(), 1024))
	}
	return nil
}
