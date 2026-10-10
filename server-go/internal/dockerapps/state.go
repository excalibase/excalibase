package dockerapps

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// appRecord is what the runtime keeps per app beside the engine's own state:
// the route the edge serves and the deploy being rolled out. It sits in the
// routes directory as <project>.<app>.json; the edge reads only the rendered
// <project>.<app>.caddy next to it.
type appRecord struct {
	Project string `json:"project"`
	AppID   string `json:"appId"`
	AppName string `json:"appName"`
	// Port is the HTTP port the edge routes to; 0 for an internal service.
	Port    int      `json:"port"`
	Host    string   `json:"host,omitempty"`
	Domains []string `json:"domains,omitempty"`
	// Serving are the containers the edge routes to.
	Serving []string       `json:"serving,omitempty"`
	Pending *pendingDeploy `json:"pending,omitempty"`
	// Deploy is the last deploy applied, the one a resume brings back.
	Deploy string `json:"deploy,omitempty"`
	// Withdrawn: a project deletion took the routes away until a resume restores them.
	Withdrawn bool `json:"withdrawn,omitempty"`
}

type pendingDeploy struct {
	Deploy   string `json:"deploy"`
	Replicas int    `json:"replicas"`
}

var (
	upstreamName = regexp.MustCompile(`^excalibase-app-[0-9a-f]{12}-[0-9a-f]{8}-[0-9]+$`)
	// recordName bounds the ids and names a route file's comment carries.
	recordName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

func (r *Runtime) recordPath(project, appID, ext string) string {
	return filepath.Join(r.opts.RoutesDir, project+"."+appID+ext)
}

// loadRecord answers nil for an app with no record.
func (r *Runtime) loadRecord(project, appID string) (*appRecord, error) {
	if !recordName.MatchString(project) || !recordName.MatchString(appID) {
		return nil, fmt.Errorf("%q/%q is not an app's record", project, appID)
	}
	raw, err := os.ReadFile(r.recordPath(project, appID, ".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the app's runtime record: %w", err)
	}
	var record appRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("read the app's runtime record: %w", err)
	}
	return &record, nil
}

// updateRecord changes one app's record and its route file together.
func (r *Runtime) updateRecord(project, appID string, change func(*appRecord) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.loadRecord(project, appID)
	if err != nil {
		return err
	}
	if record == nil {
		record = &appRecord{Project: project, AppID: appID}
	}
	if err := change(record); err != nil {
		return err
	}
	if err := r.checkUnshared(record); err != nil {
		return err
	}
	return r.saveRecord(record)
}

// checkUnshared refuses a host another app's route already names: the edge
// rejects a configuration with one host twice, and would not start with it.
func (r *Runtime) checkUnshared(record *appRecord) error {
	for _, domain := range record.Domains {
		if domain == r.opts.Route.Domain || strings.HasSuffix(domain, "."+r.opts.Route.Domain) {
			return fmt.Errorf("%q is under the app domain; only an app's own hostname is", domain)
		}
	}
	if renderable, _ := r.renderRoute(record); renderable == "" {
		return nil
	}
	others, err := r.projectRecords("")
	if err != nil {
		return err
	}
	mine := append(nonEmpty(record.Host), record.Domains...)
	for _, other := range others {
		if other.Project == record.Project && other.AppID == record.AppID {
			continue
		}
		for _, host := range append(nonEmpty(other.Host), other.Domains...) {
			if slices.Contains(mine, host) {
				return fmt.Errorf("%q is already routed to another app", host)
			}
		}
	}
	return nil
}

// saveRecord renders the route before writing anything: a value that could
// not be served is refused, never written for the edge to choke on.
func (r *Runtime) saveRecord(record *appRecord) error {
	route, err := r.renderRoute(record)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the app's runtime record: %w", err)
	}
	if err := writeAtomically(r.recordPath(record.Project, record.AppID, ".json"), raw); err != nil {
		return err
	}
	routePath := r.recordPath(record.Project, record.AppID, ".caddy")
	if route == "" {
		return removeIfPresent(routePath)
	}
	return writeAtomically(routePath, []byte(route))
}

func (r *Runtime) removeRecord(project, appID string) error {
	if !recordName.MatchString(project) || !recordName.MatchString(appID) {
		return fmt.Errorf("%q/%q is not an app's record", project, appID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(removeIfPresent(r.recordPath(project, appID, ".caddy")),
		removeIfPresent(r.recordPath(project, appID, ".json")))
}

// projectRecords lists the records of one project, or of every project for "".
func (r *Runtime) projectRecords(project string) ([]*appRecord, error) {
	pattern := "*.json"
	if project != "" && !recordName.MatchString(project) {
		return nil, fmt.Errorf("%q is not a project id", project)
	}
	if project != "" {
		pattern = project + ".*.json"
	}
	paths, err := filepath.Glob(filepath.Join(r.opts.RoutesDir, pattern))
	if err != nil {
		return nil, err
	}
	records := make([]*appRecord, 0, len(paths))
	for _, path := range paths {
		project, appID, ok := strings.Cut(strings.TrimSuffix(filepath.Base(path), ".json"), ".")
		if !ok {
			continue
		}
		record, err := r.loadRecord(project, appID)
		if err != nil {
			return nil, err
		}
		if record != nil {
			records = append(records, record)
		}
	}
	return records, nil
}

// renderRoute is the edge's site block for the app's hostname and its routed
// custom domains; empty when nothing of the app is public.
func (r *Runtime) renderRoute(record *appRecord) (string, error) {
	if record.Withdrawn || record.Port == 0 || (record.Host == "" && len(record.Domains) == 0) {
		return "", nil
	}
	upstreams, err := routeUpstreams(record)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# App %s (%s) of %s. Written by provisioning; edits are overwritten.\n", record.AppID, record.AppName, record.Project)
	for _, hosts := range [][]string{nonEmpty(record.Host), record.Domains} {
		if len(hosts) == 0 {
			continue
		}
		if err := r.checkHosts(hosts); err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "%s {\n\ttls {$EXCALIBASE_TLS}\n", strings.Join(hosts, ", "))
		if len(upstreams) == 0 {
			out.WriteString("\trespond \"This app is not running.\" 503\n}\n")
			continue
		}
		fmt.Fprintf(&out, "\treverse_proxy %s\n}\n", strings.Join(upstreams, " "))
	}
	return out.String(), nil
}

// routeUpstreams checks every value the route carries and answers the addresses it proxies to.
func routeUpstreams(record *appRecord) ([]string, error) {
	for _, name := range []string{record.Project, record.AppID, record.AppName} {
		if !recordName.MatchString(name) {
			return nil, fmt.Errorf("%q cannot appear in a route", name)
		}
	}
	if record.Port < 1 || record.Port > 65535 {
		return nil, fmt.Errorf("the app's port %d cannot be routed", record.Port)
	}
	upstreams := make([]string, 0, len(record.Serving))
	for _, name := range record.Serving {
		if !upstreamName.MatchString(name) {
			return nil, fmt.Errorf("%q is not an app container the edge may route to", name)
		}
		upstreams = append(upstreams, name+":"+strconv.Itoa(record.Port))
	}
	return upstreams, nil
}

// checkHosts refuses anything but a lowercase hostname, and the platform's own hosts.
func (r *Runtime) checkHosts(hosts []string) error {
	for _, host := range hosts {
		if problems := validation.IsDNS1123Subdomain(host); len(problems) > 0 || strings.Contains(host, "*") {
			return fmt.Errorf("%q cannot be routed: %s", host, strings.Join(problems, "; "))
		}
		if slices.Contains(r.opts.ReservedHosts, host) {
			return fmt.Errorf("%q is one of the platform's own hosts", host)
		}
	}
	return nil
}

func nonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

// writeAtomically renames into place, so the edge never reads half a file;
// the temporary name does not match the edge's *.caddy import.
func writeAtomically(path string, content []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	_, writeErr := file.Write(content)
	if err := errors.Join(writeErr, file.Chmod(0o644), file.Close()); err != nil {
		_ = os.Remove(file.Name())
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		_ = os.Remove(file.Name())
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
	}
	return nil
}
