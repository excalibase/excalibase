// Package permissions validates, stores the rules for and folds legacy policy
// data into the Hasura-style API permissions of EXC-370: one object per
// (table, role, operation), boolean expressions over the row, tracked
// functions and function permissions.
//
// Column existence is deliberately not checked here: the engine refuses a
// permission naming a column it cannot find when it builds a role's schema.
package permissions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	keyFilter            = "filter"
	keyCheck             = "check"
	keyColumns           = "columns"
	keyLimit             = "limit"
	keyAllowAggregations = "allowAggregations"
	keySet               = "set"

	allColumns = "*"
)

var (
	// qualifiedPart is one lower-case, unquoted identifier of a table or
	// function key; quoted and mixed-case names are refused for GA.
	qualifiedPart = regexp.MustCompile(`^[a-z_][a-z0-9_$]{0,62}$`)
	// columnName is a column or relationship name inside an expression, a
	// column list or a preset.
	columnName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)
	validRole  = regexp.MustCompile(domain.GrantRolePattern)
)

// operationKeys lists, per operation, the keys an object may carry and
// whether each is required.
var operationKeys = map[string]map[string]bool{
	domain.PermissionSelect: {keyFilter: true, keyColumns: true, keyLimit: false, keyAllowAggregations: false},
	domain.PermissionInsert: {keyCheck: true, keyColumns: true, keySet: false},
	domain.PermissionUpdate: {keyFilter: true, keyCheck: false, keyColumns: true, keySet: false},
	domain.PermissionDelete: {keyFilter: true},
}

// ValidateQualifiedName accepts "schema.name", both parts lower-case
// identifiers. Used for table and function keys.
func ValidateQualifiedName(name string) error {
	schemaName, object, ok := strings.Cut(name, ".")
	if !ok || !qualifiedPart.MatchString(schemaName) || !qualifiedPart.MatchString(object) {
		return fmt.Errorf("%q is not schema.name in lower-case identifiers", name)
	}
	return nil
}

// ValidateRole accepts a role a permission can name: never service, which
// bypasses permissions altogether.
func ValidateRole(role string) error {
	if role == domain.GrantRoleService {
		return errors.New(`role "service" bypasses permissions, so a permission for it means nothing`)
	}
	if !validRole.MatchString(role) {
		return fmt.Errorf("role %q must be lower-case letters, digits and underscores, "+
			"starting with a letter, at most 63 characters", role)
	}
	return nil
}

// ValidateOperation accepts select, insert, update or delete.
func ValidateOperation(op string) error {
	if _, ok := operationKeys[op]; !ok {
		return fmt.Errorf("operation %q must be select, insert, update or delete", op)
	}
	return nil
}

// NormalizePermission validates an operation object and returns it in
// canonical form (compact, keys sorted).
func NormalizePermission(op string, body []byte) (json.RawMessage, error) {
	allowed, ok := operationKeys[op]
	if !ok {
		return nil, ValidateOperation(op)
	}
	fields, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("a %s permission must be a JSON object: %w", op, err)
	}
	for key := range fields {
		if _, known := allowed[key]; !known {
			return nil, fmt.Errorf("%q is not a key of a %s permission", key, op)
		}
	}
	for key, required := range allowed {
		if _, present := fields[key]; required && !present {
			return nil, fmt.Errorf("a %s permission needs %q", op, key)
		}
	}
	for key, value := range fields {
		if err := validateField(key, value); err != nil {
			return nil, err
		}
	}
	return json.Marshal(fields)
}

func validateField(key string, value json.RawMessage) error {
	switch key {
	case keyFilter, keyCheck:
		if err := ValidateBoolExp(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		return nil
	case keyColumns:
		return validateColumns(value)
	case keyLimit:
		return validateLimit(value)
	case keyAllowAggregations:
		var allow bool
		if err := strictDecode(value, &allow); err != nil {
			return errors.New("allowAggregations must be true or false")
		}
		return nil
	default:
		return validatePresets(value)
	}
}

func validateColumns(value json.RawMessage) error {
	var star string
	if strictDecode(value, &star) == nil {
		if star != allColumns {
			return errors.New(`columns must be "*" or a list of column names`)
		}
		return nil
	}
	var names []string
	if err := strictDecode(value, &names); err != nil || names == nil {
		return errors.New(`columns must be "*" or a list of column names`)
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !columnName.MatchString(name) {
			return fmt.Errorf("column %q is not a plain identifier", name)
		}
		if seen[name] {
			return fmt.Errorf("column %q is listed twice", name)
		}
		seen[name] = true
	}
	return nil
}

func validateLimit(value json.RawMessage) error {
	if isJSONNull(value) {
		return nil
	}
	var limit int64
	if err := strictDecode(value, &limit); err != nil || limit < 1 || limit > 1<<31-1 {
		return errors.New("limit must be a positive integer or null")
	}
	return nil
}

func validatePresets(value json.RawMessage) error {
	presets, err := decodeObject(value)
	if err != nil {
		return errors.New("set must be an object of column: value")
	}
	for column, preset := range presets {
		if !columnName.MatchString(column) {
			return fmt.Errorf("preset column %q is not a plain identifier", column)
		}
		var text string
		if json.Unmarshal(preset, &text) == nil && looksLikeSessionVariable(text) && !IsSessionVariable(text) {
			return fmt.Errorf("preset %q: %q is not a valid session variable", column, text)
		}
	}
	return nil
}

// decodeObject decodes exactly one JSON object and nothing after it.
func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := strictDecode(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("null is not an object")
	}
	return fields, nil
}

func strictDecode(raw []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}
