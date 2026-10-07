package schema

import (
	"regexp"
	"strings"
)

var (
	dmlStatement    = regexp.MustCompile(`(?i)^(INSERT|UPDATE|DELETE|MERGE)\b`)
	returningClause = regexp.MustCompile(`(?i)\bRETURNING\b`)
	dollarTag       = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)
)

// returnsRows reports a write with a RETURNING clause: its rows are the
// answer, as they are for a SELECT. The word inside a string, quoted
// identifier or comment does not count.
func returnsRows(query string) bool {
	code := strings.TrimSpace(sqlCode(query))
	return dmlStatement.MatchString(code) && returningClause.MatchString(code)
}

// sqlCode is query with its string literals, quoted identifiers and comments
// blanked out.
func sqlCode(query string) string {
	var code strings.Builder
	for i := 0; i < len(query); {
		end := skipQuoted(query, i)
		if end == i {
			code.WriteByte(query[i])
			i++
			continue
		}
		code.WriteByte(' ')
		i = end
	}
	return code.String()
}

// skipQuoted answers where the quoted text or comment starting at i ends, or i.
func skipQuoted(query string, i int) int {
	rest := query[i:]
	switch {
	case rest[0] == '\'' && i > 0 && (query[i-1] == 'E' || query[i-1] == 'e'):
		return closeEscapedQuote(query, i)
	case rest[0] == '\'' || rest[0] == '"':
		return closeQuote(query, i, rest[0])
	case strings.HasPrefix(rest, "--"):
		return closeAt(query, i, "\n")
	case strings.HasPrefix(rest, "/*"):
		return closeAt(query, i+2, "*/")
	case rest[0] == '$':
		if tag := dollarTag.FindString(rest); tag != "" {
			return closeAt(query, i+len(tag), tag)
		}
	}
	return i
}

// closeQuote ends a quoted run; a doubled quote is an escaped one.
func closeQuote(query string, i int, quote byte) int {
	for j := i + 1; j < len(query); j++ {
		if query[j] != quote {
			continue
		}
		if j+1 < len(query) && query[j+1] == quote {
			j++
			continue
		}
		return j + 1
	}
	return len(query)
}

// closeEscapedQuote ends an E'...' string, where a backslash escapes the next character.
func closeEscapedQuote(query string, i int) int {
	for j := i + 1; j < len(query); j++ {
		switch {
		case query[j] == '\\':
			j++
		case query[j] == '\'' && j+1 < len(query) && query[j+1] == '\'':
			j++
		case query[j] == '\'':
			return j + 1
		}
	}
	return len(query)
}

func closeAt(query string, from int, closing string) int {
	if end := strings.Index(query[from:], closing); end >= 0 {
		return from + end + len(closing)
	}
	return len(query)
}
