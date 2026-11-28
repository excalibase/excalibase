package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

func TestARefusalIsAConflictThatNamesWhatIsLeft(t *testing.T) {
	refusal := &planbudget.Refusal{Dimension: "CPU", Need: planbudget.Size{CPUMilli: 500}, Left: planbudget.Size{CPUMilli: 250}, Budget: planbudget.Size{CPUMilli: 2000}}
	w := httptest.NewRecorder()
	if !writePlanBudgetRefusal(w, fmt.Errorf("record the disk: %w", refusal)) {
		t.Fatal("a wrapped refusal must be recognised")
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("%d", w.Code)
	}
	var body planBudgetBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "plan_budget_exceeded" || body.Left.CPUMilli != 250 || body.Need.CPUMilli != 500 || body.Budget.CPUMilli != 2000 {
		t.Fatalf("%+v", body)
	}
	if !strings.Contains(body.Error, "0.25 vCPU is left") {
		t.Fatalf("the message says what is left: %q", body.Error)
	}
}

func TestOtherErrorsAreLeftToTheCaller(t *testing.T) {
	w := httptest.NewRecorder()
	if writePlanBudgetRefusal(w, errors.New("boom")) || w.Body.Len() != 0 {
		t.Fatal("a plain error must not be answered here")
	}
}

func TestAFunctionsRuntimeThatDoesNotFitIsAConflict(t *testing.T) {
	w := httptest.NewRecorder()
	writeRuntimeUnavailable(w, "deploy hello", fmt.Errorf("admit runtime: %w", &planbudget.Refusal{Dimension: "memory", Need: planbudget.Size{MemoryBytes: 128 << 20}, Left: planbudget.Size{MemoryBytes: 64 << 20}}))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "plan_budget_exceeded") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestEveryWriterThatGrowsAServiceAnswersARefusalAsAConflict(t *testing.T) {
	refusal := fmt.Errorf("wrapped: %w", &planbudget.Refusal{Dimension: "disk"})
	writers := map[string]func(w http.ResponseWriter){
		"project creation": func(w http.ResponseWriter) { writeProjectCreationError(w, refusal) },
		"cluster change":   func(w http.ResponseWriter) { writeClusterChangeError(w, "p", "resize", refusal) },
		"app store":        func(w http.ResponseWriter) { (&AppHandler{}).writeStoreError(w, refusal) },
		"pool-busy gate":   func(w http.ResponseWriter) { writeIfPoolBusy(w, refusal) },
	}
	for name, write := range writers {
		w := httptest.NewRecorder()
		write(w)
		if w.Code != http.StatusConflict {
			t.Errorf("%s answered %d", name, w.Code)
		}
	}
}

func TestARefusalFromUsedUpCreditSaysSoAndKeepsTheCode(t *testing.T) {
	refusal := &planbudget.Refusal{Dimension: "memory", Need: planbudget.Size{MemoryBytes: 1 << 30}, Left: planbudget.Size{MemoryBytes: 512 << 20}, CreditExhausted: true}
	w := httptest.NewRecorder()
	if !writePlanBudgetRefusal(w, refusal) {
		t.Fatal("recognised")
	}
	var body planBudgetBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "plan_budget_exceeded" || !body.CreditExhausted || !strings.Contains(body.Error, "credit is used up") {
		t.Fatalf("the code stays so every caller keeps working, and the body says the credit is gone: %+v", body)
	}
	plain := httptest.NewRecorder()
	writePlanBudgetRefusal(plain, &planbudget.Refusal{Dimension: "CPU"})
	if strings.Contains(plain.Body.String(), "creditExhausted\":true") {
		t.Fatalf("an ordinary refusal is not a credit refusal: %s", plain.Body.String())
	}
}

// An idle-paused database that cannot start at $0 is told why: its own resume answers the credit refusal (EXC-894).
func TestAResumeRefusedForUsedUpCreditSaysSoThroughTheLifecycleAnswer(t *testing.T) {
	w := httptest.NewRecorder()
	refusal := fmt.Errorf("persist resume: %w", &planbudget.Refusal{Dimension: "CPU", Need: planbudget.Size{CPUMilli: 500}, Left: planbudget.Size{CPUMilli: 0}, CreditExhausted: true})
	(&ProvisioningHandler{}).writeLifecycleError(context.Background(), w, "proj-1", refusal)
	if w.Code != http.StatusConflict {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var body planBudgetBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.CreditExhausted || !strings.Contains(body.Error, "Contact support to add credit") {
		t.Fatalf("%+v", body)
	}
}

func TestTheRefusalBodyCarriesTheWholeStep(t *testing.T) {
	w := httptest.NewRecorder()
	step := planbudget.Size{CPUMilli: 500, MemoryBytes: 512 << 20, DiskBytes: 1 << 30}
	writePlanBudgetRefusal(w, &planbudget.Refusal{Dimension: "CPU", Need: planbudget.Size{CPUMilli: 500}, Step: step, CreditExhausted: true})
	var body planBudgetBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Step != step || !strings.Contains(w.Body.String(), `"step":`) {
		t.Fatalf("%s", w.Body.String())
	}
}
