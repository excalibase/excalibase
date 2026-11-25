package service

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/metrics"
)

// crashWindow is the rolling period crashes are counted over.
const crashWindow = time.Hour

// databaseCrashMetric is the alert metric a crash-looping database raises.
const databaseCrashMetric = "databaseCrashLoop"

// ContainerCrashSource reads how a project's containers have been restarting.
type ContainerCrashSource interface {
	AppContainerStates(ctx context.Context, namespace, appID string) ([]k8s.ContainerCrash, error)
	DatabaseContainerStates(ctx context.Context, namespace string) ([]k8s.ContainerCrash, error)
}

// CrashStopper stops an app that kept crashing.
type CrashStopper interface {
	SuspendForCrashLoop(ctx context.Context, projectID, appID string, crashes int, window time.Duration) (*apphost.Suspension, error)
}

// CrashAlerter raises and resolves one alert per project and metric; AlertingService is one.
type CrashAlerter interface {
	Track(projectID, metric string, breached bool, newAlert func() domain.Alert) AlertChange
}

// CrashNotifier tells an app's owners it was stopped. Optional.
type CrashNotifier interface {
	AppStopped(ctx context.Context, inst *domain.DatabaseInstance, app *apphost.App, suspension *apphost.Suspension)
}

// CrashGuardApps lists the apps the guard may stop.
type CrashGuardApps interface {
	ListSuspendable() ([]*apphost.App, error)
}

// CrashGuardInstances lists the projects whose namespaces the guard may read.
type CrashGuardInstances interface {
	FindAll() ([]*domain.DatabaseInstance, error)
}

// CrashGuardConfig wires a CrashGuard.
type CrashGuardConfig struct {
	Apps      CrashGuardApps
	Instances CrashGuardInstances
	Pods      ContainerCrashSource
	Stopper   CrashStopper
	Alerts    CrashAlerter
	Notifier  CrashNotifier
	// Threshold is the crashes, over a rolling hour, that trip the guard.
	Threshold int
	// ExemptProjects and ProtectedNamespaces are never read, stopped or alerted on.
	ExemptProjects      []string
	ProtectedNamespaces []string
	Now                 func() time.Time
}

// CrashGuard stops customer apps that keep crashing and only alerts for
// databases that do (EXC-839). It counts crashes (a container run that ended
// in a non-zero exit, not an OOM kill) over a rolling hour from the restart
// counts it sees on each sweep.
type CrashGuard struct {
	config CrashGuardConfig
	mu     sync.Mutex
	seen   map[string]*crashHistory // keyed "app/<id>" or "db/<project>"
}

func NewCrashGuard(config CrashGuardConfig) *CrashGuard {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &CrashGuard{config: config, seen: map[string]*crashHistory{}}
}

// crashHistory remembers each container's last restart count and when its crashes happened.
type crashHistory struct {
	restarts map[string]int32
	crashes  []time.Time
}

// observe folds one sweep's container states in and returns the crashes in the window.
// A container seen for the first time sets a baseline: its earlier restarts are unknown to us.
func (h *crashHistory) observe(states []k8s.ContainerCrash, now time.Time) int {
	current := make(map[string]int32, len(states))
	for _, state := range states {
		id := state.PodUID + "/" + state.Container
		previous, known := h.restarts[id]
		current[id] = state.RestartCount
		if !known || state.Deleting || state.RestartCount <= previous || !isCrash(state.LastExit) {
			continue
		}
		for i := previous; i < state.RestartCount; i++ {
			h.crashes = append(h.crashes, now)
		}
	}
	h.restarts = current
	h.crashes = slices.DeleteFunc(h.crashes, func(at time.Time) bool { return now.Sub(at) >= crashWindow })
	return len(h.crashes)
}

// isCrash: a run that ended non-zero and not by the OOM killer. A pod being
// deleted is dropped by observe, since a rollout's exits are not crashes.
func isCrash(exit *k8s.ContainerExit) bool {
	return exit != nil && exit.ExitCode != 0 && exit.Reason != "OOMKilled"
}

func (g *CrashGuard) history(key string) *crashHistory {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen[key] == nil {
		g.seen[key] = &crashHistory{restarts: map[string]int32{}}
	}
	return g.seen[key]
}

func (g *CrashGuard) forget(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.seen, key)
}

// Sweep reads every guarded project's containers once.
func (g *CrashGuard) Sweep(ctx context.Context) {
	instances, err := g.config.Instances.FindAll()
	if err != nil {
		log.Printf("crash guard: list projects: %v", err)
		return
	}
	guarded := g.guardedInstances(instances)
	g.sweepApps(ctx, guarded)
	g.sweepDatabases(ctx, guarded)
}

func (g *CrashGuard) guardedInstances(instances []*domain.DatabaseInstance) map[string]*domain.DatabaseInstance {
	guarded := make(map[string]*domain.DatabaseInstance, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.Namespace == "" || slices.Contains(g.config.ExemptProjects, inst.ProjectID) ||
			slices.Contains(g.config.ProtectedNamespaces, inst.Namespace) {
			continue
		}
		guarded[inst.ProjectID] = inst
	}
	return guarded
}

func (g *CrashGuard) sweepApps(ctx context.Context, guarded map[string]*domain.DatabaseInstance) {
	apps, err := g.config.Apps.ListSuspendable()
	if err != nil {
		log.Printf("crash guard: list apps: %v", err)
		return
	}
	for _, app := range apps {
		if ctx.Err() != nil {
			return
		}
		if inst := guarded[app.ProjectID]; inst != nil {
			g.sweepApp(ctx, inst, app)
		}
	}
}

func (g *CrashGuard) sweepApp(ctx context.Context, inst *domain.DatabaseInstance, app *apphost.App) {
	states, err := g.config.Pods.AppContainerStates(ctx, inst.Namespace, app.ID)
	if err != nil {
		log.Printf("crash guard: read pods of app %s/%s: %v", app.ProjectID, app.ID, err)
		return
	}
	key := "app/" + app.ID
	crashes := g.history(key).observe(states, g.config.Now())
	if crashes < g.config.Threshold {
		return
	}
	suspension, err := g.config.Stopper.SuspendForCrashLoop(ctx, app.ProjectID, app.ID, crashes, crashWindow)
	if err != nil && suspension == nil {
		log.Printf("crash guard: stop app %s/%s after %d crashes: %v", app.ProjectID, app.ID, crashes, err)
		return
	}
	if err != nil {
		log.Printf("crash guard: app %s/%s is stopped, but not marked: %v", app.ProjectID, app.ID, err)
	}
	metrics.CrashLoopTripped("app")
	log.Printf("crash guard: stopped app %s/%s: %d crashes in the last hour", app.ProjectID, app.ID, crashes)
	g.forget(key)
	if g.config.Notifier != nil {
		g.config.Notifier.AppStopped(ctx, inst, app, suspension)
	}
}

// sweepDatabases only reads and alerts: a database is never scaled or stopped.
func (g *CrashGuard) sweepDatabases(ctx context.Context, guarded map[string]*domain.DatabaseInstance) {
	for projectID, inst := range guarded {
		if ctx.Err() != nil {
			return
		}
		if inst.NoDatabase {
			continue
		}
		states, err := g.config.Pods.DatabaseContainerStates(ctx, inst.Namespace)
		if err != nil {
			log.Printf("crash guard: read database pods of %s: %v", projectID, err)
			continue
		}
		crashes := g.history("db/"+projectID).observe(states, g.config.Now())
		g.alertDatabase(projectID, crashes)
	}
}

func (g *CrashGuard) alertDatabase(projectID string, crashes int) {
	breached := crashes >= g.config.Threshold
	change := g.config.Alerts.Track(projectID, databaseCrashMetric, breached, func() domain.Alert {
		value, threshold := float64(crashes), float64(g.config.Threshold)
		return domain.Alert{
			ID:       fmt.Sprintf("%s-%s-%d", databaseCrashMetric, projectID, g.config.Now().UnixNano()),
			Severity: "CRITICAL",
			Message:  fmt.Sprintf("The database crashed %d times in the last hour. It is left running; check its disk and logs.", crashes),
			Value:    &value, Threshold: &threshold,
			Timestamp: &domain.FlexTime{Time: g.config.Now()},
		}
	})
	if change.Raised != nil {
		metrics.CrashLoopTripped("database")
		log.Printf("crash guard: database of project %s crashed %d times in the last hour (alert only)", projectID, crashes)
	}
}

// Start sweeps on a timer while this replica leads. Returns a function that stops it.
func (g *CrashGuard) Start(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.sweepIfLeader(ctx, leader)
			}
		}
	}()
	return cancel
}

func (g *CrashGuard) sweepIfLeader(ctx context.Context, leader LeaderChecker) {
	leading, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("crash guard: leadership check: %v", err)
		return
	}
	if leading {
		g.Sweep(ctx)
	}
}
