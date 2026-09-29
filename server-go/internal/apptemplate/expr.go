package apptemplate

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const (
	exprOpen  = "${{"
	exprClose = "}}"
	// MinSecretLength and MaxSecretLength bound secret(n).
	MinSecretLength = 16
	MaxSecretLength = 128
)

type exprKind int

const (
	exprSecret exprKind = iota + 1
	exprDatabase
	exprAppHost
	exprAppPort
	exprAppEnv
)

type expr struct {
	kind     exprKind
	length   int
	app      string
	variable string
}

// part is literal text or one expression.
type part struct {
	literal string
	expr    *expr
}

var (
	secretExpr   = regexp.MustCompile(`^secret\(([0-9]{1,4})\)$`)
	databaseExpr = regexp.MustCompile(`^db\.([A-Z_]+)$`)
	appFieldExpr = regexp.MustCompile(`^apps\.([a-z][a-z0-9-]*)\.(host|port)$`)
	appEnvExpr   = regexp.MustCompile(`^apps\.([a-z][a-z0-9-]*)\.env\.([A-Za-z_][A-Za-z0-9_]*)$`)
)

// parseValue splits a value into literal text and expressions. A database
// reference must be the whole value: it is stored as a reference the deploy
// resolves, never pasted into text.
func parseValue(value string) ([]part, error) {
	var parts []part
	rest := value
	for {
		open := strings.Index(rest, exprOpen)
		if open < 0 {
			break
		}
		closing := strings.Index(rest[open:], exprClose)
		if closing < 0 {
			return nil, errors.New("an expression is not closed with }}")
		}
		if open > 0 {
			parts = append(parts, part{literal: rest[:open]})
		}
		e, err := parseExpr(strings.TrimSpace(rest[open+len(exprOpen) : open+closing]))
		if err != nil {
			return nil, err
		}
		parts = append(parts, part{expr: e})
		rest = rest[open+closing+len(exprClose):]
	}
	if rest != "" {
		parts = append(parts, part{literal: rest})
	}
	for _, p := range parts {
		if p.expr != nil && p.expr.kind == exprDatabase && len(parts) != 1 {
			return nil, fmt.Errorf("${{ db.%s }} must be the whole value", p.expr.variable)
		}
	}
	return parts, nil
}

func parseExpr(body string) (*expr, error) {
	if m := secretExpr.FindStringSubmatch(body); m != nil {
		length, _ := strconv.Atoi(m[1])
		if length < MinSecretLength || length > MaxSecretLength {
			return nil, fmt.Errorf("secret(n) takes a length from %d to %d", MinSecretLength, MaxSecretLength)
		}
		return &expr{kind: exprSecret, length: length}, nil
	}
	if m := databaseExpr.FindStringSubmatch(body); m != nil {
		if !slices.Contains(apphost.DatabaseSourceVariables(), m[1]) {
			return nil, fmt.Errorf("the database exposes no variable %s (it exposes %s)",
				m[1], strings.Join(apphost.DatabaseSourceVariables(), ", "))
		}
		return &expr{kind: exprDatabase, variable: m[1]}, nil
	}
	if m := appFieldExpr.FindStringSubmatch(body); m != nil {
		kind := exprAppHost
		if m[2] == "port" {
			kind = exprAppPort
		}
		return &expr{kind: kind, app: m[1]}, nil
	}
	if m := appEnvExpr.FindStringSubmatch(body); m != nil {
		return &expr{kind: exprAppEnv, app: m[1], variable: m[2]}, nil
	}
	return nil, fmt.Errorf("unknown expression %q (expected secret(n), db.<VAR>, apps.<name>.host, apps.<name>.port or apps.<name>.env.<VAR>)", body)
}

// Where a variable's value comes from, as the details page names it.
const (
	SourceLiteral   = "literal"
	SourceGenerated = "generated"
	SourceDatabase  = "database"
	SourceApp       = "app"
)

// ValueSource names the strongest source in a value: a generated secret, then
// the database, then another app, else plain text.
func ValueSource(value string) string {
	parts, err := parseValue(value)
	if err != nil {
		return SourceLiteral
	}
	source := SourceLiteral
	for _, p := range parts {
		switch {
		case p.expr == nil:
		case p.expr.kind == exprSecret:
			return SourceGenerated
		case p.expr.kind == exprDatabase:
			source = SourceDatabase
		case source == SourceLiteral:
			source = SourceApp
		}
	}
	return source
}
