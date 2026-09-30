package permissions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Limits on one filter or check expression. They keep the SQL the engine
// compiles from a permission bounded.
const (
	MaxExpressionDepth = 16
	MaxExpressionNodes = 200
)

const (
	opAnd    = "_and"
	opOr     = "_or"
	opNot    = "_not"
	opExists = "_exists"
	opTable  = "_table"
	opWhere  = "_where"

	opEq     = "_eq"
	opNeq    = "_neq"
	opGt     = "_gt"
	opGte    = "_gte"
	opLt     = "_lt"
	opLte    = "_lte"
	opIn     = "_in"
	opNin    = "_nin"
	opLike   = "_like"
	opNlike  = "_nlike"
	opIsNull = "_is_null"

	sessionVariablePrefix = "x-excalibase-"
)

// sessionVariable matches X-Excalibase-<name>, compared case-insensitively.
// Underscores are allowed in the name because token claims carry them
// (X-Excalibase-tenant_id).
var sessionVariable = regexp.MustCompile(`^(?i)x-excalibase-[a-z0-9_-]+$`)

// IsSessionVariable reports whether a string names a session variable.
func IsSessionVariable(value string) bool {
	return sessionVariable.MatchString(value)
}

// looksLikeSessionVariable is the prefix test: a string with the prefix is
// read as a variable and so must be a well-formed one.
func looksLikeSessionVariable(value string) bool {
	return strings.HasPrefix(strings.ToLower(value), sessionVariablePrefix)
}

func isComparisonOperator(key string) bool {
	switch key {
	case opEq, opNeq, opGt, opGte, opLt, opLte, opIn, opNin, opLike, opNlike, opIsNull:
		return true
	}
	return false
}

// ValidateBoolExp checks one expression against the Hasura grammar of
// docs/features/permissions.md §4 and the size limits above.
func ValidateBoolExp(raw json.RawMessage) error {
	walker := &expWalker{}
	return walker.exp(raw, 1)
}

type expWalker struct{ nodes int }

func (w *expWalker) count(depth int) error {
	w.nodes++
	if w.nodes > MaxExpressionNodes {
		return fmt.Errorf("expression has more than %d nodes", MaxExpressionNodes)
	}
	if depth > MaxExpressionDepth {
		return fmt.Errorf("expression is nested deeper than %d", MaxExpressionDepth)
	}
	return nil
}

func (w *expWalker) exp(raw json.RawMessage, depth int) error {
	if err := w.count(depth); err != nil {
		return err
	}
	fields, err := decodeObject(raw)
	if err != nil {
		return errors.New("an expression must be a JSON object")
	}
	for key, value := range fields {
		if err := w.term(key, value, depth); err != nil {
			return err
		}
	}
	return nil
}

func (w *expWalker) term(key string, value json.RawMessage, depth int) error {
	switch key {
	case opAnd, opOr:
		return w.list(key, value, depth)
	case opNot:
		return w.exp(value, depth+1)
	case opExists:
		return w.exists(value, depth)
	}
	if !columnName.MatchString(key) {
		return fmt.Errorf("%q is not a column or relationship name", key)
	}
	fields, err := decodeObject(value)
	if err != nil {
		return fmt.Errorf("%q must map to an object", key)
	}
	for op := range fields {
		if isComparisonOperator(op) {
			return w.comparison(key, fields, depth)
		}
	}
	// No comparison operator: a relationship to the related table's rows.
	return w.exp(value, depth+1)
}

func (w *expWalker) list(key string, value json.RawMessage, depth int) error {
	var items []json.RawMessage
	if err := strictDecode(value, &items); err != nil || items == nil {
		return fmt.Errorf("%s takes an array of expressions", key)
	}
	for _, item := range items {
		if err := w.exp(item, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (w *expWalker) exists(value json.RawMessage, depth int) error {
	fields, err := decodeObject(value)
	if err != nil || len(fields) != 2 || fields[opTable] == nil || fields[opWhere] == nil {
		return errors.New(`_exists takes exactly {"_table": "schema.table", "_where": expression}`)
	}
	var table string
	if err := strictDecode(fields[opTable], &table); err != nil {
		return errors.New("_exists._table must be a string")
	}
	if err := ValidateQualifiedName(table); err != nil {
		return fmt.Errorf("_exists._table: %w", err)
	}
	return w.exp(fields[opWhere], depth+1)
}

func (w *expWalker) comparison(column string, ops map[string]json.RawMessage, depth int) error {
	if err := w.count(depth + 1); err != nil {
		return err
	}
	for op, value := range ops {
		if !isComparisonOperator(op) {
			return fmt.Errorf("%q: %q is not a comparison operator", column, op)
		}
		if err := validateOperand(op, value); err != nil {
			return fmt.Errorf("%q %s: %w", column, op, err)
		}
	}
	return nil
}

func validateOperand(op string, value json.RawMessage) error {
	switch op {
	case opIsNull:
		var isNull bool
		if err := strictDecode(value, &isNull); err != nil || isJSONNull(value) {
			return errors.New("takes true or false")
		}
		return nil
	case opIn, opNin:
		return validateListOperand(value)
	case opLike, opNlike:
		var pattern string
		if err := strictDecode(value, &pattern); err != nil || isJSONNull(value) {
			return errors.New("takes a string pattern or a session variable")
		}
		return validateStringOperand(pattern)
	default:
		return validateScalarOperand(value)
	}
}

// validateScalarOperand accepts a string, number or boolean. null is refused:
// only _is_null matches NULL.
func validateScalarOperand(value json.RawMessage) error {
	var scalar any
	if err := strictDecode(value, &scalar); err != nil {
		return err
	}
	switch typed := scalar.(type) {
	case string:
		return validateStringOperand(typed)
	case float64, bool:
		return nil
	case nil:
		return errors.New("null is not comparable; use _is_null")
	default:
		return errors.New("takes a string, number, boolean or session variable")
	}
}

func validateStringOperand(text string) error {
	if looksLikeSessionVariable(text) && !IsSessionVariable(text) {
		return fmt.Errorf("%q is not a valid session variable", text)
	}
	return nil
}

// validateListOperand accepts an array of literals, or one session variable
// holding an array.
func validateListOperand(value json.RawMessage) error {
	var variable string
	if strictDecode(value, &variable) == nil && !isJSONNull(value) {
		if !IsSessionVariable(variable) {
			return errors.New("takes an array or a session variable holding one")
		}
		return nil
	}
	var items []json.RawMessage
	if err := strictDecode(value, &items); err != nil || items == nil {
		return errors.New("takes an array or a session variable holding one")
	}
	for _, item := range items {
		var scalar any
		if err := strictDecode(item, &scalar); err != nil {
			return err
		}
		switch typed := scalar.(type) {
		case string:
			if looksLikeSessionVariable(typed) {
				return errors.New("an array element cannot be a session variable")
			}
		case float64, bool:
		default:
			return errors.New("array elements must be strings, numbers or booleans")
		}
	}
	return nil
}

func isJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
