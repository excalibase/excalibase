package credits

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"time"
)

var statementHeader = []string{"time_utc", "kind", "amount_usd", "reason", "period_start_utc", "period_end_utc", "uncovered_usd"}

// WriteStatementCSV writes the month as CSV. A cell that starts with a
// spreadsheet formula character is prefixed so it is read as text.
func WriteStatementCSV(w io.Writer, statement Statement) error {
	out := csv.NewWriter(w)
	rows := [][]string{
		statementHeader,
		{statement.Month + "-01", "opening_balance", statement.Opening.Dollars(), "", "", "", ""},
	}
	for _, entry := range statement.Entries {
		rows = append(rows, []string{
			entry.CreatedAt.UTC().Format(time.RFC3339), string(entry.Kind), entry.Amount.Dollars(), entry.Reason,
			formatInstant(entry.PeriodStart), formatInstant(entry.PeriodEnd), uncoveredDollars(entry),
		})
	}
	rows = append(rows, []string{"", "closing_balance", statement.Closing.Dollars(), "", "", "", ""})
	for _, row := range rows {
		for i, cell := range row {
			row[i] = neutralise(cell)
		}
		if err := out.Write(row); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}

func formatInstant(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

func uncoveredDollars(entry Entry) string {
	switch value := entry.Details["uncovered_micros"].(type) {
	case float64:
		return Micros(int64(value)).Dollars()
	case int64:
		return Micros(value).Dollars()
	case string:
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return Micros(parsed).Dollars()
		}
	}
	return ""
}

// neutralise stops a spreadsheet running a cell as a formula; a negative
// amount is a number, not a formula, and stays as it is.
func neutralise(cell string) string {
	if cell == "" || !strings.ContainsRune("=+@\t\r", rune(cell[0])) {
		if cell != "" && cell[0] == '-' {
			if _, err := strconv.ParseFloat(cell, 64); err != nil {
				return "'" + cell
			}
		}
		return cell
	}
	return "'" + cell
}
