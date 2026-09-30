package permissions

import (
	"errors"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

// ErrFunctionNotFound is returned when no routine has the requested name.
var ErrFunctionNotFound = errors.New("function not found in the project database")

// reservedSchemas are never served by the engine (ReservedSchemas.BUILT_IN
// there, plus the catalogs).
var reservedSchemas = map[string]bool{"auth": true, "excalibase": true, "information_schema": true}

// IsServedSchema reports whether the engine may serve objects of a schema.
func IsServedSchema(name string) bool {
	lower := strings.ToLower(name)
	return !reservedSchemas[lower] && !strings.HasPrefix(lower, "pg_")
}

// FunctionDecision is how a trackable function is exposed.
type FunctionDecision struct {
	ExposedAs       string
	SecurityDefiner bool
}

// Trackable applies docs/features/permissions.md §6 to the live definition:
// a plain, not overloaded function in a served schema returning rows of a
// served table or view. Exposure follows volatility. SECURITY DEFINER is
// allowed and reported. overloads are every routine with the requested
// schema and name.
func Trackable(overloads []schema.FunctionDetail, sessionArgument *string) (FunctionDecision, error) {
	if len(overloads) == 0 {
		return FunctionDecision{}, ErrFunctionNotFound
	}
	if len(overloads) > 1 {
		return FunctionDecision{}, errors.New("the function is overloaded; rename one so the name is unambiguous")
	}
	fn := overloads[0]
	if err := trackableKind(fn.Kind); err != nil {
		return FunctionDecision{}, err
	}
	if !IsServedSchema(fn.Schema) {
		return FunctionDecision{}, fmt.Errorf("schema %s is not served by the API", fn.Schema)
	}
	if err := ValidateQualifiedName(fn.Schema + "." + fn.Name); err != nil {
		return FunctionDecision{}, fmt.Errorf("function names must be lower-case identifiers: %w", err)
	}
	if err := returnsServedTable(fn.ReturnsTable); err != nil {
		return FunctionDecision{}, err
	}
	if err := validSessionArgument(fn.Args, sessionArgument); err != nil {
		return FunctionDecision{}, err
	}
	exposedAs := domain.ExposeAsQuery
	if fn.Volatility == "VOLATILE" {
		exposedAs = domain.ExposeAsMutation
	}
	return FunctionDecision{ExposedAs: exposedAs, SecurityDefiner: fn.SecurityDefiner}, nil
}

func trackableKind(kind string) error {
	switch kind {
	case "f":
		return nil
	case "p":
		return errors.New("a procedure cannot be tracked, only a function")
	case "a", "w":
		return errors.New("an aggregate or window function cannot be tracked")
	}
	return fmt.Errorf("routine kind %q cannot be tracked", kind)
}

func returnsServedTable(table string) error {
	if table == "" {
		return errors.New("the function must return rows of a table or view (SETOF <table> or <table>)")
	}
	schemaName, _, _ := strings.Cut(table, ".")
	if !IsServedSchema(schemaName) {
		return fmt.Errorf("the function returns rows of %s, which is not served by the API", table)
	}
	if err := ValidateQualifiedName(table); err != nil {
		return fmt.Errorf("the returned table's name must be lower-case identifiers: %w", err)
	}
	return nil
}

func validSessionArgument(args []schema.FunctionArg, name *string) error {
	if name == nil {
		return nil
	}
	for _, arg := range args {
		if arg.Name != "" && arg.Name == *name {
			if arg.Type != "json" && arg.Type != "jsonb" {
				return fmt.Errorf("session argument %q must be of type json or jsonb, not %s", *name, arg.Type)
			}
			return nil
		}
	}
	return fmt.Errorf("session argument %q is not an argument of the function", *name)
}
