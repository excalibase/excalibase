package schema

import "strings"

// mysqlStatements splits a script on the semicolons MySQL would end a
// statement at: not inside a quoted string, a quoted name or a comment.
func mysqlStatements(script string) []string {
	var statements []string
	start := 0
	for index := 0; index < len(script); index++ {
		switch {
		case script[index] == ';':
			statements = appendStatement(statements, script[start:index])
			start = index + 1
		default:
			index = mysqlSkip(script, index)
		}
	}
	return appendStatement(statements, script[start:])
}

func appendStatement(statements []string, statement string) []string {
	if strings.TrimSpace(leadingComments.ReplaceAllString(statement, "")) == "" {
		return statements
	}
	return append(statements, statement)
}

// mysqlSkip returns the last index of the quoted text or comment opening at
// index, or index itself.
func mysqlSkip(script string, index int) int {
	switch character := script[index]; {
	case character == '\'' || character == '"' || character == '`':
		return closeMySQLQuote(script, index, character)
	case character == '#':
		return lineEnd(script, index)
	case strings.HasPrefix(script[index:], "-- ") || strings.HasPrefix(script[index:], "--\n") || strings.HasPrefix(script[index:], "--\t"):
		return lineEnd(script, index)
	case strings.HasPrefix(script[index:], "/*"):
		if end := strings.Index(script[index+2:], "*/"); end >= 0 {
			return index + 2 + end + 1
		}
		return len(script) - 1
	}
	return index
}

// closeMySQLQuote finds the closing quote; a doubled quote and, outside a
// quoted name, a backslash escape stay inside.
func closeMySQLQuote(script string, open int, quote byte) int {
	for index := open + 1; index < len(script); index++ {
		switch {
		case script[index] == '\\' && quote != '`':
			index++
		case script[index] == quote && index+1 < len(script) && script[index+1] == quote:
			index++
		case script[index] == quote:
			return index
		}
	}
	return len(script) - 1
}

func lineEnd(script string, index int) int {
	if end := strings.IndexByte(script[index:], '\n'); end >= 0 {
		return index + end
	}
	return len(script) - 1
}
