package permissions

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Exp is a boolean expression under construction; it encodes to the grammar
// ValidateBoolExp accepts. The empty Exp is true.
type Exp map[string]any

// Session variables the legacy rule variables become.
const (
	VarUserID    = "X-Excalibase-User-Id"
	VarProjectID = "X-Excalibase-Project-Id"
	VarRole      = "X-Excalibase-Role"

	sessionVariableName = "X-Excalibase-"
)

// claimName is a custom {{claim}} a legacy rule may name.
var claimName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ErrUnexpressible marks a legacy rule the permission grammar cannot say.
var ErrUnexpressible = errors.New("not expressible as a permission")

func unexpressible(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnexpressible, fmt.Sprintf(format, args...))
}

// And conjoins expressions, dropping ones that are always true.
func And(parts ...Exp) Exp {
	kept := make([]Exp, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			kept = append(kept, part)
		}
	}
	switch len(kept) {
	case 0:
		return Exp{}
	case 1:
		return kept[0]
	}
	return Exp{opAnd: kept}
}

// Or disjoins expressions; one that is always true makes the whole true.
func Or(parts ...Exp) Exp {
	for _, part := range parts {
		if len(part) == 0 {
			return Exp{}
		}
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return Exp{opOr: parts}
}

// Not negates an expression.
func Not(part Exp) Exp {
	return Exp{opNot: part}
}

// ConvertRules turns a legacy policy's rules into one expression. A policy
// without rules matched every row, so it becomes the empty expression.
func ConvertRules(logic domain.LogicOperator, rules []domain.Rule) (Exp, error) {
	parts := make([]Exp, 0, len(rules))
	for _, r := range rules {
		exp, err := ConvertRule(r)
		if err != nil {
			return nil, err
		}
		parts = append(parts, exp)
	}
	if len(parts) == 0 {
		return Exp{}, nil
	}
	switch logic {
	case domain.LogicAnd, "":
		if len(parts) == 1 {
			return parts[0], nil
		}
		return Exp{opAnd: parts}, nil
	case domain.LogicOr:
		return Or(parts...), nil
	}
	return nil, unexpressible("rule logic %q", logic)
}

var ruleOperators = map[domain.RuleOperator]string{
	domain.OpEQ: opEq, domain.OpNEQ: opNeq, domain.OpGT: opGt, domain.OpGTE: opGte,
	domain.OpLT: opLt, domain.OpLTE: opLte, domain.OpIN: opIn, domain.OpNOTIN: opNin,
	domain.OpLIKE: opLike, domain.OpNOTLIKE: opNlike,
}

// ConvertRule turns one legacy rule into {column: {op: value}}.
func ConvertRule(r domain.Rule) (Exp, error) {
	if strings.Contains(r.Field, ".") {
		return nil, unexpressible("field %q is a JSON path", r.Field)
	}
	if !columnName.MatchString(r.Field) {
		return nil, unexpressible("field %q is not a column name", r.Field)
	}
	switch r.Operator {
	case domain.OpISNULL:
		return Exp{r.Field: map[string]any{opIsNull: true}}, nil
	case domain.OpISNOTNULL:
		return Exp{r.Field: map[string]any{opIsNull: false}}, nil
	}
	op, ok := ruleOperators[r.Operator]
	if !ok {
		return nil, unexpressible("operator %q", r.Operator)
	}
	value := r.Value
	if isRuleVariable(value) {
		return convertVariable(r, op, strings.TrimSpace(value[2:len(value)-2]))
	}
	operand, err := literalOperand(r, op)
	if err != nil {
		return nil, err
	}
	return Exp{r.Field: map[string]any{op: operand}}, nil
}

// isRuleVariable mirrors the engine's VariableResolver: a value wrapped in
// {{ }} names a variable, anything else is a literal.
func isRuleVariable(value string) bool {
	return strings.HasPrefix(value, "{{") && strings.HasSuffix(value, "}}") && len(value) >= 4
}

func convertVariable(r domain.Rule, op, name string) (Exp, error) {
	switch name {
	case "currentUserId":
		return Exp{r.Field: map[string]any{scalarOperator(op): VarUserID}}, nil
	case "currentTenantId":
		return Exp{r.Field: map[string]any{scalarOperator(op): VarProjectID}}, nil
	case "currentUserRoles":
		// A request runs as one role now, so membership in the caller's
		// roles is equality with the role it runs as.
		if op != opEq && op != opIn {
			return nil, unexpressible("{{currentUserRoles}} with %s", r.Operator)
		}
		return Exp{r.Field: map[string]any{opEq: VarRole}}, nil
	case "currentUserGroupIds":
		return nil, unexpressible("groups have no session variable")
	case "now", "today":
		return nil, unexpressible("time variable {{%s}}", name)
	}
	if strings.HasPrefix(name, "daysAgo:") {
		return nil, unexpressible("time variable {{%s}}", name)
	}
	if !claimName.MatchString(name) {
		return nil, unexpressible("variable {{%s}}", name)
	}
	return Exp{r.Field: map[string]any{op: sessionVariableName + strings.ToLower(name)}}, nil
}

// scalarOperator turns membership in a one-value variable into equality: the
// engine read IN {{currentUserId}} as a list of that one id.
func scalarOperator(op string) string {
	switch op {
	case opIn:
		return opEq
	case opNin:
		return opNeq
	}
	return op
}

func literalOperand(r domain.Rule, op string) (any, error) {
	if op == opLike || op == opNlike {
		return stringLiteral(r.Value)
	}
	if op != opIn && op != opNin {
		return castLiteral(r.Value, r.FieldType)
	}
	parts := strings.Split(r.Value, ",")
	values := make([]any, 0, len(parts))
	for _, part := range parts {
		value, err := castLiteral(strings.TrimSpace(part), r.FieldType)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func stringLiteral(value string) (any, error) {
	if looksLikeSessionVariable(value) {
		return nil, unexpressible("literal %q would read as a session variable", value)
	}
	return value, nil
}

// castLiteral binds a literal the way the engine's VariableResolver cast it:
// text as given, numbers parsed.
func castLiteral(raw string, fieldType domain.FieldType) (any, error) {
	value := strings.TrimSpace(raw)
	switch fieldType {
	case domain.FieldInteger, domain.FieldLong:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return nil, unexpressible("%q is not an integer", value)
		}
		return json.Number(value), nil
	case domain.FieldDouble, "DECIMAL":
		if _, err := strconv.ParseFloat(value, 64); err != nil || !json.Valid([]byte(value)) {
			return nil, unexpressible("%q is not a number", value)
		}
		return json.Number(value), nil
	case domain.FieldBoolean:
		return strings.EqualFold(value, "true"), nil
	case domain.FieldString, domain.FieldUUID, domain.FieldDate, domain.FieldDatetime:
		return stringLiteral(raw)
	}
	return nil, unexpressible("field type %q", fieldType)
}
