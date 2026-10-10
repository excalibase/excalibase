package dockerapps

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	appLogReadTimeout = 15 * time.Second
	certificateDial   = 5 * time.Second
)

// ErrPrivateNetworkFixed: a project's network on one host is shared by all its apps.
var ErrPrivateNetworkFixed = errors.New("on a single host the apps of a project always reach each other; their network cannot be closed")

// AppLogs reads the app's newest containers' logs, as AppLogs reads pods.
func (r *Runtime) AppLogs(ctx context.Context, namespace, appID string, opts k8s.AppLogOptions) (k8s.AppLogPage, error) {
	ctx, cancel := context.WithTimeout(ctx, appLogReadTimeout)
	defer cancel()
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return k8s.AppLogPage{}, err
	}
	list, err := r.listApps(ctx, labelProject, project, labelApp, appID)
	if err != nil {
		return k8s.AppLogPage{}, err
	}
	slices.SortFunc(list, func(a, b container.Summary) int { return int(b.Created - a.Created) })
	list = list[:min(len(list), k8s.MaxAppLogSources)]
	reads := make([]k8s.AppLogRead, 0, len(list))
	for _, summary := range list {
		read, err := r.containerLog(ctx, summary, opts, int64(k8s.MaxAppLogBytes/max(len(list), 1)))
		if err != nil {
			return k8s.AppLogPage{}, err
		}
		reads = append(reads, read)
	}
	return k8s.PageAppLogLines(reads, int(opts.TailLines), opts.Since != nil), nil
}

// containerLog reads one container. Previous reads what it wrote before its
// current start: the engine keeps one log across a container's restarts.
func (r *Runtime) containerLog(ctx context.Context, summary container.Summary, opts k8s.AppLogOptions, limit int64) (k8s.AppLogRead, error) {
	logOpts := container.LogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true}
	if opts.Previous {
		inspect, err := r.engine.ContainerInspect(ctx, summary.ID)
		if err != nil {
			return k8s.AppLogRead{}, fmt.Errorf("read %s: %w", nameOf(summary), err)
		}
		if inspect.ContainerJSONBase == nil || inspect.RestartCount == 0 || inspect.State == nil {
			return k8s.AppLogRead{}, nil
		}
		logOpts.Until = inspect.State.StartedAt
	}
	if opts.Since != nil {
		logOpts.Since = strconv.FormatInt(opts.Since.Truncate(time.Second).Unix(), 10)
	} else {
		logOpts.Tail = strconv.FormatInt(opts.TailLines, 10)
	}
	stream, err := r.engine.ContainerLogs(ctx, summary.ID, logOpts)
	if err != nil {
		return k8s.AppLogRead{}, fmt.Errorf("read logs of %s: %w", nameOf(summary), err)
	}
	defer stream.Close()
	reader, writer := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(writer, writer, stream)
		writer.CloseWithError(copyErr)
	}()
	defer reader.Close()
	limited := &io.LimitedReader{R: reader, N: limit}
	lines, err := k8s.ParseAppLogLines(nameOf(summary), limited, opts.Since)
	if err != nil {
		return k8s.AppLogRead{}, fmt.Errorf("read logs of %s: %w", nameOf(summary), err)
	}
	return k8s.AppLogRead{Lines: lines, Limited: limited.N <= 0}, nil
}

// AppHostCertificate is the state of the certificate the edge serves for the app's hostname.
func (r *Runtime) AppHostCertificate(ctx context.Context, namespace, appName string) (k8s.CertificateState, error) {
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return k8s.CertificateState{}, err
	}
	records, err := r.projectRecords(project)
	if err != nil {
		return k8s.CertificateState{}, err
	}
	for _, record := range records {
		if record.AppName == appName && record.Host != "" && !record.Withdrawn {
			return r.certificateFor(ctx, record.Host)
		}
	}
	return k8s.CertificateState{}, k8s.ErrNoCertificate
}

func (r *Runtime) AppDomainCertificate(ctx context.Context, namespace, _ string, host string) (k8s.CertificateState, error) {
	if _, err := r.opts.Projects.ProjectOf(namespace); err != nil {
		return k8s.CertificateState{}, err
	}
	return r.certificateFor(ctx, host)
}

// certificateFor is ready once a browser would accept what the edge serves for
// host. With its own CA the edge issues on the spot, so a routed host is ready.
func (r *Runtime) certificateFor(ctx context.Context, host string) (k8s.CertificateState, error) {
	if r.opts.EdgeIssuesLocally {
		return r.routedState(host)
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: certificateDial},
		Config:    &tls.Config{ServerName: host, RootCAs: r.opts.EdgeRoots, MinVersion: tls.VersionTLS12},
	}
	conn, err := dialer.DialContext(ctx, "tcp", r.opts.EdgeTLSAddress)
	if err != nil {
		return k8s.CertificateState{Failure: "the edge serves no valid certificate for " + host + " yet: " + err.Error()}, nil
	}
	_ = conn.Close()
	return k8s.CertificateState{Ready: true}, nil
}

// routedState is ready when a route file serves host.
func (r *Runtime) routedState(host string) (k8s.CertificateState, error) {
	records, err := r.projectRecords("")
	if err != nil {
		return k8s.CertificateState{}, err
	}
	for _, record := range records {
		rendered, err := r.renderRoute(record)
		if err != nil {
			return k8s.CertificateState{}, err
		}
		if rendered != "" && (record.Host == host || slices.Contains(record.Domains, host)) {
			return k8s.CertificateState{Ready: true}, nil
		}
	}
	return k8s.CertificateState{Failure: host + " is not routed yet"}, nil
}

// AttachIssuedAppHostCertificates has nothing to attach: the edge keeps what it issues.
func (r *Runtime) AttachIssuedAppHostCertificates(context.Context) error { return nil }

// DeleteRegistryPullSecrets has nothing to delete: a credential travels with each pull and is not kept.
func (r *Runtime) DeleteRegistryPullSecrets(context.Context, string, string) error { return nil }

func (r *Runtime) SetAppPrivateNetwork(_ context.Context, _ string, open bool) error {
	if !open {
		return ErrPrivateNetworkFixed
	}
	return nil
}

func (r *Runtime) AppPrivateNetworkOpen(context.Context, string) (bool, error) { return true, nil }

// EnsureNamespaceQuota has no object to write: the plan's app count is checked
// before anything runs, and every container carries its plan's limits.
func (r *Runtime) EnsureNamespaceQuota(context.Context, string, k8s.NamespaceQuota) error { return nil }

// RuntimeClassPlacement: one host, and no fixed overhead the engine reports.
func (r *Runtime) RuntimeClassPlacement(context.Context, string) (k8s.RuntimePlacement, error) {
	return k8s.RuntimePlacement{}, nil
}
