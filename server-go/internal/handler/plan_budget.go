package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

// planBudgetCode is the machine-readable reason a step did not fit the plan.
const planBudgetCode = "plan_budget_exceeded"

type planBudgetBody struct {
	Error     string          `json:"error"`
	Code      string          `json:"code"`
	Dimension string          `json:"dimension"`
	Need      planbudget.Size `json:"need"`
	Left      planbudget.Size `json:"left"`
	Budget    planbudget.Size `json:"budget"`
	// Step is the whole size of what was asked for, so a client can tell whether it would fit alone.
	Step planbudget.Size `json:"step"`
	// CreditExhausted is set when the credit is used up and the free allowance is the ceiling.
	CreditExhausted bool `json:"creditExhausted,omitempty"`
	Status          int  `json:"status"`
}

// writePlanBudgetRefusal answers a step that does not fit the org's plan with a
// 409 that names what was needed, what is left and what the plan allows, all in
// the caller's own numbers and never the platform's capacity. It reports
// whether err was such a refusal.
func writePlanBudgetRefusal(w http.ResponseWriter, err error) bool {
	var refusal *planbudget.Refusal
	if !errors.As(err, &refusal) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(planBudgetBody{
		Error: refusal.Error(), Code: planBudgetCode, Dimension: refusal.Dimension,
		Need: refusal.Need, Left: refusal.Left, Budget: refusal.Budget, Step: refusal.Step, CreditExhausted: refusal.CreditExhausted, Status: http.StatusConflict,
	})
	return true
}
