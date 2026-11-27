//go:build integration

package apphost_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

// A Standard app is 1 vCPU and 1 GiB per replica.
func budgetGate(cpuMilli int, on bool) *planbudget.Gate {
	numbers := config.PlanNumbers{CPUMilli: cpuMilli, MemoryMiB: 1 << 20, DiskGiB: 1000, MaxProjects: 50, MaxServices: 100}
	return planbudget.NewGate(func(context.Context) bool { return on }, config.PlanBudgetConfig{Free: numbers, Standard: numbers})
}

type budgetFixture struct {
	apps    *apphost.PostgresAppStore
	deploys *apphost.PostgresDeployStore
	project string
}

// newBudgetFixture seeds an org on the STANDARD plan with one project, and stores that admit against cpuMilli.
func newBudgetFixture(t *testing.T, name string, cpuMilli int) budgetFixture {
	t.Helper()
	apps := newPGAppStore(t)
	org, project, user := "org-"+name, "proj-"+name, "usr-"+name
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO users (id, username, email, password_hash) VALUES ('%[1]s', '%[1]s', '%[1]s@x.test', 'x')`, user),
		fmt.Sprintf(`INSERT INTO orgs (id, name, slug, tier, owner_id) VALUES ('%[1]s', '%[1]s', '%[1]s', 'STANDARD', '%[2]s')`, org, user),
		fmt.Sprintf(`INSERT INTO database_instances (project_id, org_id, status, no_database, storage_size) VALUES ('%s', '%s', 'ACTIVE', TRUE, '5Gi')`, project, org),
	} {
		if _, err := sharedDB.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	gate := budgetGate(cpuMilli, true)
	apps.SetPlanBudget(gate)
	deploys := apphost.NewPostgresDeployStore(sharedDB)
	deploys.SetPlanBudget(gate)
	return budgetFixture{apps: apps, deploys: deploys, project: project}
}

func (f budgetFixture) add(t *testing.T, id string, replicas int, status string) *apphost.App {
	t.Helper()
	app := sampleApp(f.project, id, id)
	app.Replicas, app.Status = replicas, status
	if err := f.apps.Create(app, 100); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return app
}

func isBudgetRefusal(err error) bool {
	var refusal *planbudget.Refusal
	return errors.As(err, &refusal)
}

func raceSteps(steps []func() error) []error {
	results := make([]error, len(steps))
	var start, done sync.WaitGroup
	start.Add(1)
	for i, step := range steps {
		done.Add(1)
		go func(i int, step func() error) {
			defer done.Done()
			start.Wait()
			results[i] = step()
		}(i, step)
	}
	start.Done()
	done.Wait()
	return results
}

func exactlyOneAdmitted(t *testing.T, results []error) {
	t.Helper()
	admitted := 0
	for i, err := range results {
		switch {
		case err == nil:
			admitted++
		case isBudgetRefusal(err):
		default:
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if admitted != 1 {
		t.Fatalf("%d steps admitted, want exactly 1: %v", admitted, results)
	}
}

func TestConcurrentAppCreatesFillTheBudgetOnce(t *testing.T) {
	f := newBudgetFixture(t, "appcreate", 2000)
	f.add(t, "seed", 1, apphost.StatusRunning)
	steps := make([]func() error, 6)
	for i := range steps {
		id := fmt.Sprintf("racer%d", i)
		steps[i] = func() error {
			app := sampleApp(f.project, id, id)
			return f.apps.Create(app, 100)
		}
	}
	exactlyOneAdmitted(t, raceSteps(steps))
}

func TestConcurrentScaleUpsFillTheBudgetOnce(t *testing.T) {
	f := newBudgetFixture(t, "appscale", 3500)
	steps := make([]func() error, 2)
	for i := range steps {
		app := f.add(t, fmt.Sprintf("scale%d", i), 1, apphost.StatusRunning)
		steps[i] = func() error {
			scaled := *app
			scaled.Replicas = 2
			return f.apps.Update(&scaled, app.Version)
		}
	}
	exactlyOneAdmitted(t, raceSteps(steps))
}

func TestConcurrentDeploysOfStoppedAppsFillTheBudgetOnce(t *testing.T) {
	f := newBudgetFixture(t, "appdeploy", 2000)
	f.add(t, "live", 1, apphost.StatusRunning)
	steps := make([]func() error, 4)
	for i := range steps {
		app := f.add(t, fmt.Sprintf("stopped%d", i), 1, apphost.StatusStopped)
		steps[i] = func() error {
			return f.deploys.Create(&apphost.Deploy{ID: fmt.Sprintf("dep-%s", app.ID), AppID: app.ID, ProjectID: f.project, Status: apphost.DeployStatusPending})
		}
	}
	exactlyOneAdmitted(t, raceSteps(steps))
}

func TestConcurrentAppResumesFillTheBudgetOnce(t *testing.T) {
	f := newBudgetFixture(t, "appresume", 2000)
	f.add(t, "live", 1, apphost.StatusRunning)
	steps := make([]func() error, 4)
	for i := range steps {
		app := f.add(t, fmt.Sprintf("sleeper%d", i), 1, apphost.StatusStopped)
		steps[i] = func() error {
			_, err := f.apps.Transition(f.project, app.ID, []string{apphost.StatusStopped}, apphost.StatusResuming)
			return err
		}
	}
	exactlyOneAdmitted(t, raceSteps(steps))
}

func TestStoppingScalingDownAndDeletingAnAppAlwaysPassOverBudget(t *testing.T) {
	f := newBudgetFixture(t, "appshrink", 100000)
	a := f.add(t, "big", 3, apphost.StatusRunning)
	b := f.add(t, "other", 1, apphost.StatusRunning)
	c := f.add(t, "gone", 1, apphost.StatusRunning)
	over := budgetGate(100, true)
	f.apps.SetPlanBudget(over)
	f.deploys.SetPlanBudget(over)

	fresh := sampleApp(f.project, "late", "late")
	if err := f.apps.Create(fresh, 100); !isBudgetRefusal(err) {
		t.Fatalf("over budget refuses a new app: %v", err)
	}
	down := *a
	down.Replicas = 1
	if err := f.apps.Update(&down, a.Version); err != nil {
		t.Fatalf("scale down: %v", err)
	}
	if _, err := f.apps.Transition(f.project, b.ID, []string{apphost.StatusRunning}, apphost.StatusPausing); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := f.apps.Transition(f.project, c.ID, []string{apphost.StatusRunning}, apphost.StatusDeleting); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := f.apps.Delete(f.project, c.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestAppAdmissionIsOffWhileTheFlagIsOff(t *testing.T) {
	f := newBudgetFixture(t, "appoff", 1)
	off := budgetGate(1, false)
	f.apps.SetPlanBudget(off)
	for i := 0; i < 3; i++ {
		f.add(t, fmt.Sprintf("free%d", i), 3, apphost.StatusRunning)
	}
}

// A plan change that gives an app a bigger tier is growth like any other.
func TestAppResizeOnAPlanChangeIsAdmittedLikeGrowth(t *testing.T) {
	f := newBudgetFixture(t, "appplan", 1500)
	small := sampleApp(f.project, "tiny", "tiny")
	small.Tier = "FREE"
	small.Replicas, small.Status = 1, apphost.StatusResuming
	if err := f.apps.Create(small, 100); err != nil {
		t.Fatal(err)
	}
	resize := &apphost.Deploy{ID: "rz1", AppID: "tiny", ProjectID: f.project, Kind: apphost.DeployKindResize, Status: apphost.DeployStatusSucceeded,
		Config: apphost.DeployConfig{Tier: "ENTERPRISE", Replicas: 1}}
	done := timeNow()
	resize.FinishedAt = &done
	if err := f.deploys.RecordResize(resize); !isBudgetRefusal(err) {
		t.Fatalf("a Free app moving to a 2 vCPU tier on 1.5 vCPU: %v", err)
	}
	resize.ID, resize.Config.Tier = "rz2", "STANDARD"
	if err := f.deploys.RecordResize(resize); err != nil {
		t.Fatalf("1 vCPU fits: %v", err)
	}
}

func timeNow() time.Time { return time.Now().UTC() }
