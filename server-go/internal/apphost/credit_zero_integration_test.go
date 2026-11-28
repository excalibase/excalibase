//go:build integration

package apphost_test

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

// zeroGateFor: a Free plan of 8 vCPU, whose allowance is 1 vCPU / 1 GiB, with the credit check wired as the platform wires it.
func zeroGateFor() *planbudget.Gate {
	free := config.PlanNumbers{
		CPUMilli: 8000, MemoryMiB: 1 << 20, DiskGiB: 1000, MaxProjects: 50, MaxServices: 100,
		AllowanceCPUMilli: 1000, AllowanceMemoryMiB: 1024, AllowanceDiskGiB: 100,
	}
	gate := planbudget.NewGate(func(context.Context) bool { return true }, config.PlanBudgetConfig{Free: free, Standard: free})
	gate.SetZeroCheck(planbudget.CreditUsedUp)
	return gate
}

// creditFixture is a FREE org whose credit account holds `balance` micro-dollars, on stores that
// admit through zeroGateFor. Standard-sized apps are 1 vCPU / 1 GiB per replica.
func newCreditFixture(t *testing.T, name string, balance int64) budgetFixture {
	t.Helper()
	f := newBudgetFixture(t, name, 100000)
	org := "org-" + name
	if _, err := sharedDB.Exec(`UPDATE orgs SET tier = 'FREE' WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := sharedDB.Exec(`INSERT INTO credit_accounts (org_id, balance_micros) VALUES ($1, $2)`, org, balance); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f budgetFixture) useGate(gate *planbudget.Gate) {
	f.apps.SetPlanBudget(gate)
	f.deploys.SetPlanBudget(gate)
}

func isCreditRefusal(err error) bool {
	var refusal *planbudget.Refusal
	return errors.As(err, &refusal) && refusal.CreditExhausted
}

// R12 and the refusals at $0, for apps, through the real store: an org at $0 that is over its allowance.
func TestAtZeroAnAppOverTheAllowanceCanStillShrinkPauseAndDeleteButNotGrowOrStart(t *testing.T) {
	f := newCreditFixture(t, "czapp", 0)
	big := f.add(t, "big", 2, apphost.StatusRunning)
	pausing := f.add(t, "pausing", 1, apphost.StatusRunning)
	doomed := f.add(t, "doomed", 1, apphost.StatusRunning)
	asleep := f.add(t, "asleep", 1, apphost.StatusStopped)
	f.useGate(zeroGateFor())

	if err := f.apps.Create(sampleApp(f.project, "late", "late"), 100); !isCreditRefusal(err) {
		t.Fatalf("a new app at $0 over the allowance: %v", err)
	}
	up := *big
	up.Replicas = 3
	if err := f.apps.Update(&up, big.Version); !isCreditRefusal(err) {
		t.Fatalf("scaling up: %v", err)
	}
	if _, err := f.apps.Transition(f.project, asleep.ID, []string{apphost.StatusStopped}, apphost.StatusResuming); !isCreditRefusal(err) {
		t.Fatalf("starting a stopped app over the allowance: %v", err)
	}

	down := *big
	down.Replicas = 1
	if err := f.apps.Update(&down, big.Version); err != nil {
		t.Errorf("scale down was refused: %v", err)
	}
	if _, err := f.apps.Transition(f.project, pausing.ID, []string{apphost.StatusRunning}, apphost.StatusPausing); err != nil {
		t.Errorf("pause was refused: %v", err)
	}
	if _, err := f.apps.Transition(f.project, doomed.ID, []string{apphost.StatusRunning}, apphost.StatusDeleting); err != nil {
		t.Errorf("delete was refused: %v", err)
	}
	if err := f.apps.Delete(f.project, doomed.ID); err != nil {
		t.Errorf("removing the record was refused: %v", err)
	}
}

func TestCreditAddedLetsTheSameStepsThrough(t *testing.T) {
	f := newCreditFixture(t, "czback", 0)
	f.add(t, "live", 1, apphost.StatusRunning)
	asleep := f.add(t, "asleep", 1, apphost.StatusStopped)
	f.useGate(zeroGateFor())
	if _, err := f.apps.Transition(f.project, asleep.ID, []string{apphost.StatusStopped}, apphost.StatusResuming); !isCreditRefusal(err) {
		t.Fatalf("at $0: %v", err)
	}
	if _, err := sharedDB.Exec(`UPDATE credit_accounts SET balance_micros = 5000000 WHERE org_id = $1`, "org-czback"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.apps.Transition(f.project, asleep.ID, []string{apphost.StatusStopped}, apphost.StatusResuming); err != nil {
		t.Fatalf("with credit the start fits the plan: %v", err)
	}
}

func TestAtZeroAnAppThatFitsTheAllowanceIsAdmittedAndTheNextOneIsNot(t *testing.T) {
	f := newCreditFixture(t, "czfit", 0)
	f.useGate(zeroGateFor())
	if err := f.apps.Create(sampleApp(f.project, "first", "first"), 100); err != nil {
		t.Fatalf("one 1 vCPU / 1 GiB app is exactly the free allowance: %v", err)
	}
	if err := f.apps.Create(sampleApp(f.project, "second", "second"), 100); !isCreditRefusal(err) {
		t.Fatalf("a second one is over it: %v", err)
	}
}
