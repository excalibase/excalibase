package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// Every tool here is a read: what an agent checks first when a user asks why
// the project is slow, failing or out of room.

type requestMetricsArgs struct {
	projectArg
	Range string `json:"range,omitempty" jsonschema:"1h (the default), 24h or 7d"`
}

type metricsArgs struct {
	projectArg
	History int `json:"history,omitempty" jsonschema:"also return this many past samples; none when left out"`
	// ContainerRange asks for the containers' past samples too, only where the host reports them.
	ContainerRange string `json:"container_range,omitempty" jsonschema:"single host only: also return each container's samples over 1h, 6h, 24h, 7d or 30d; only their current state when left out"`
}

func monitoringTools() []entry {
	return []entry{
		dark(features.RequestMetrics, tool("get_request_metrics", "The project's API traffic over a range (1h, 24h or 7d): per family (graphql, rest, auth, realtime) the requests, "+
			"client and server errors and latency p50 and p95 in milliseconds, in steps over time (points) and summed (totals). Read-only.", readTool, getRequestMetrics)),
		tool("get_metrics", "The project database's CPU, memory, disk and connections now (Postgres or MySQL), and with history the recent samples. "+
			"When metricsAvailable is false, unavailableReason says why. On a single host that reports container monitoring it also lists the project's own containers "+
			"(database, apps) with their state, health, CPU, memory and network now, and with container_range their samples over that range. Read-only.", readTool, getMetrics),
		tool("list_alerts", "The project's active alerts. They come from CPU and memory monitoring (and, on a single host, container monitoring). "+
			"Backups are not covered: no alert here does not mean backups are fine. Read-only.", readTool, listAlerts),
		tool("get_database_info", "The project database's disk (size, limit, used), plan, CPU, memory and tunable settings; use it when a disk fills up. No secrets. "+
			"Postgres on Kubernetes only: a MySQL project, or a database on a single host, is refused.", readTool, getDatabaseInfo),
		tool("get_db_endpoint", "Where to connect to the project's database from outside: host, port, TLS and CA certificate, and connection strings that carry no password. "+
			"Needs Developer. Opening or closing the public port is an Admin's choice in Studio.", readTool, getDBEndpoint),
		tool("get_auth_settings", "How the project's end users may sign in: email verification and site URL always; redirect URLs and who may sign up only while a sign-in flag "+
			"(OAuth, email code or guest) is on; email codes and guest sign-in while their features are on; and the OAuth providers with their callback URLs while OAuth is on. "+
			"Client secrets are never shown. Read-only; needs Developer.", readTool, getAuthSettings),
		dark(features.DBReadAddress, tool("get_db_read_address", "The database's read-only address to its replicas, internal and public, and whether it is on. Needs Developer. Read-only.",
			readTool, getDBReadAddress)),
	}
}

// Each answer is read into a view that names what an agent may see. A field
// the route adds later stays out until a view names it.

type clusterView struct {
	Tier              domain.TierType   `json:"tier"`
	OrgTier           domain.TierType   `json:"orgTier"`
	StorageSize       string            `json:"storageSize"`
	StorageStart      string            `json:"storageStart"`
	StorageLimit      string            `json:"storageLimit"`
	StorageUsedBytes  *int64            `json:"storageUsedBytes"`
	Instances         int64             `json:"instances"`
	CPU               string            `json:"cpu"`
	Memory            string            `json:"memory"`
	Parameters        map[string]string `json:"parameters"`
	TunableParameters []string          `json:"tunableParameters"`
}

type endpointStringsView struct {
	RequireTLS      string `json:"requireTls"`
	AllowPlaintext  string `json:"allowPlaintext"`
	MongoRequireTLS string `json:"mongoRequireTls,omitempty"`
}

type internalEndpointView struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	ConnectionString string `json:"connectionString"`
	MongoHost        string `json:"mongoHost,omitempty"`
	MongoPort        int    `json:"mongoPort,omitempty"`
	MongoConnection  string `json:"mongoConnectionString,omitempty"`
}

type endpointView struct {
	PublicOffered     bool                 `json:"publicOffered"`
	PublicEnabled     bool                 `json:"publicEnabled"`
	Available         bool                 `json:"available"`
	Host              string               `json:"host"`
	Port              int                  `json:"port"`
	RequireTLS        bool                 `json:"requireTls"`
	Database          string               `json:"database"`
	Username          string               `json:"username"`
	ConnectionStrings endpointStringsView  `json:"connectionStrings"`
	CACertificate     string               `json:"caCertificate"`
	Internal          internalEndpointView `json:"internal"`
	MongoPort         int                  `json:"mongoPort,omitempty"`
	MongoAvailable    *bool                `json:"mongoAvailable,omitempty"`
	SingleHost        bool                 `json:"singleHost,omitempty"`
	Engine            string               `json:"engine,omitempty"`
}

type readAddressView struct {
	Engine     string `json:"engine,omitempty"`
	Replicas   int    `json:"replicas"`
	Reason     string `json:"reason,omitempty"`
	SingleHost bool   `json:"singleHost,omitempty"`
	Internal   *struct {
		Host             string `json:"host"`
		Port             int    `json:"port"`
		ConnectionString string `json:"connectionString"`
	} `json:"internal,omitempty"`
	PublicOffered    bool   `json:"publicOffered"`
	PublicOpen       bool   `json:"publicOpen"`
	Enabled          bool   `json:"enabled"`
	Available        bool   `json:"available"`
	Host             string `json:"host,omitempty"`
	Port             int    `json:"port,omitempty"`
	ConnectionString string `json:"connectionString,omitempty"`
	Username         string `json:"username,omitempty"`
	ReadOnlyLogin    bool   `json:"readOnlyLogin,omitempty"`
}

type authSettingsView struct {
	RequireEmailVerification bool               `json:"requireEmailVerification"`
	SiteURL                  string             `json:"siteUrl"`
	RedirectURLs             *[]string          `json:"redirectUrls,omitempty"`
	AllowNewUsers            *bool              `json:"allowNewUsers,omitempty"`
	EmailOTPEnabled          *bool              `json:"emailOtpEnabled,omitempty"`
	GuestSignInEnabled       *bool              `json:"guestSignInEnabled,omitempty"`
	GuestCaptchaSecretSet    *bool              `json:"guestCaptchaSecretSet,omitempty"`
	MFAEnabled               *bool              `json:"mfaEnabled,omitempty"`
	Providers                []authProviderView `json:"providers,omitempty"`
	Note                     string             `json:"note"`
}

type authProviderView struct {
	Provider        string `json:"provider"`
	Enabled         bool   `json:"enabled"`
	ClientID        string `json:"clientId"`
	ClientSecretSet bool   `json:"clientSecretSet"`
	CallbackURL     string `json:"callbackUrl"`
}

func getRequestMetrics(ctx context.Context, c *call, in requestMetricsArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var query url.Values
	if in.Range != "" {
		query = url.Values{"range": {in.Range}}
	}
	var charts domain.RequestCharts
	if err := c.get(ctx, provisionAPI+projectID+"/requests", query, &charts); err != nil {
		return nil, err
	}
	return charts, nil
}

func getMetrics(ctx context.Context, c *call, in metricsArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if in.ContainerRange != "" && !slices.Contains(containerRanges, in.ContainerRange) {
		return nil, fmt.Errorf("container_range must be one of %s", strings.Join(containerRanges, ", "))
	}
	var current domain.DatabaseMetrics
	if err := c.get(ctx, provisionAPI+projectID+"/metrics/current", nil, &current); err != nil {
		return nil, err
	}
	out := map[string]any{"current": current}
	if in.History > 0 {
		var history domain.MetricsHistory
		query := url.Values{"limit": {strconv.Itoa(in.History)}}
		if err := c.get(ctx, provisionAPI+projectID+"/metrics/history", query, &history); err != nil {
			return nil, err
		}
		out["history"] = history
	}
	addContainerMetrics(ctx, c, projectID, in.ContainerRange, out)
	return out, nil
}

var containerRanges = []string{"1h", "6h", "24h", "7d", "30d"}

// containerSampleView is one minute of a container; the project and name are on the container.
type containerSampleView struct {
	Timestamp            time.Time `json:"timestamp"`
	CPUCores             *float64  `json:"cpuCores"`
	CPUPercent           *float64  `json:"cpuPercent"`
	MemoryBytes          *int64    `json:"memoryBytes"`
	MemoryLimitBytes     *int64    `json:"memoryLimitBytes"`
	NetworkRxBytesPerSec *float64  `json:"networkRxBytesPerSec"`
	NetworkTxBytesPerSec *float64  `json:"networkTxBytesPerSec"`
}

type containerView struct {
	Name      string                `json:"name"`
	Component string                `json:"component"`
	State     string                `json:"state"`
	Health    string                `json:"health,omitempty"`
	Current   *containerSampleView  `json:"current"`
	Points    []containerSampleView `json:"points,omitempty"`
}

// addContainerMetrics folds in the project's containers where the host reports
// them. A platform that does not (Kubernetes) says so and keeps the database answer whole.
func addContainerMetrics(ctx context.Context, c *call, projectID, rangeName string, out map[string]any) {
	if !features.Enabled(ctx, c.settings.Features, features.HostMetrics) {
		return
	}
	query := url.Values{}
	if rangeName != "" {
		query.Set("range", rangeName)
	}
	var answer struct {
		Range      string          `json:"range"`
		Containers []containerView `json:"containers"`
	}
	if err := c.get(ctx, "/api/projects/"+projectID+"/containers/metrics", query, &answer); err != nil {
		if isStatus(err, http.StatusNotFound) {
			out["containersNote"] = "this platform does not report container metrics"
		} else {
			out["containersError"] = err.Error()
		}
		return
	}
	for i := range answer.Containers {
		if rangeName == "" {
			answer.Containers[i].Points = nil
		}
	}
	if answer.Containers == nil {
		answer.Containers = []containerView{}
	}
	out["containers"] = answer.Containers
	if rangeName != "" {
		out["containerRange"] = answer.Range
	}
}

func listAlerts(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	alerts := []domain.Alert{}
	if err := c.get(ctx, "/api/alerts/project/"+projectID+"/", nil, &alerts); err != nil {
		return nil, err
	}
	return map[string]any{"alerts": alerts}, nil
}

func getDatabaseInfo(ctx context.Context, c *call, in projectArg) (any, error) {
	return readView[clusterView](ctx, c, in.ProjectID, func(id string) string { return provisionAPI + id + "/cluster" })
}

func getDBEndpoint(ctx context.Context, c *call, in projectArg) (any, error) {
	return readView[endpointView](ctx, c, in.ProjectID, func(id string) string { return projectsAPI + id + "/db-endpoint/" })
}

func getDBReadAddress(ctx context.Context, c *call, in projectArg) (any, error) {
	return readView[readAddressView](ctx, c, in.ProjectID, func(id string) string { return projectsAPI + id + "/db-read-address/" })
}

// readView resolves the project and reads one route into the view V.
func readView[V any](ctx context.Context, c *call, requested string, path func(projectID string) string) (any, error) {
	projectID, err := c.useProject(requested)
	if err != nil {
		return nil, err
	}
	var view V
	if err := c.get(ctx, path(projectID), nil, &view); err != nil {
		return nil, err
	}
	return view, nil
}

// getAuthSettings reads how the project's end users may sign in. The sign-in
// providers join it only while their feature is on; their client secrets are
// write-only and never come back.
func getAuthSettings(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var view authSettingsView
	if err := c.get(ctx, projectsAPI+projectID+"/auth-settings/", nil, &view); err != nil {
		return nil, err
	}
	if features.Enabled(ctx, c.settings.Features, features.AuthOAuth) {
		var answer struct {
			Providers []authProviderView `json:"providers"`
		}
		if err := c.get(ctx, projectsAPI+projectID+"/auth-providers/", nil, &answer); err != nil {
			return nil, err
		}
		view.Providers = answer.Providers
	}
	view.Note = "Read-only. Changing sign-in settings, providers and captcha is done by a project Admin in Studio."
	return view, nil
}
