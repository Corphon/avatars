package tools

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLInput describes a database query request.
type SQLInput struct {
	Query    string `json:"query"`              // SELECT query to execute
	Database string `json:"database,omitempty"` // path to SQLite file (default: .avatars/memory/hot-memory.db)
	MaxRows  int    `json:"max_rows,omitempty"` // max result rows (default 100)
}

// SQLTool executes read-only SQL queries against a SQLite database.
// It provides structured data access for the Agent, addressing the
// "Agent can access databases" parity requirement.
type SQLTool struct{}

func (SQLTool) Name() string {
	return "sql"
}

func (SQLTool) IsConcurrencySafe(input any) bool {
	return true
}

func (t SQLTool) Call(ctx context.Context, input any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	in, ok := input.(SQLInput)
	if !ok {
		if qStr, isStr := input.(string); isStr {
			in = SQLInput{Query: qStr}
		} else {
			return Result{}, fmt.Errorf("sql requires a query string or {query, database} object")
		}
	}

	query := strings.TrimSpace(in.Query)
	if query == "" {
		return Result{}, fmt.Errorf("sql: query is required")
	}

	// Only allow SELECT for safety.
	upperQuery := strings.ToUpper(strings.TrimSpace(query))
	if !strings.HasPrefix(upperQuery, "SELECT") && !strings.HasPrefix(upperQuery, "PRAGMA") {
		return Result{}, fmt.Errorf("sql: only SELECT and PRAGMA queries are allowed for safety")
	}

	dbPath := strings.TrimSpace(in.Database)
	if dbPath == "" {
		dbPath = ".avatars/memory/hot-memory.db"
	}

	maxRows := in.MaxRows
	if maxRows <= 0 || maxRows > 500 {
		maxRows = 100
	}

	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return Result{}, fmt.Errorf("sql: open database: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx2, query)
	if err != nil {
		return Result{}, fmt.Errorf("sql: query error: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return Result{}, fmt.Errorf("sql: get columns: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(strings.Join(columns, " | "))
	sb.WriteString("\n")
	for i := range columns {
		sb.WriteString(strings.Repeat("-", len(columns[i])))
		if i < len(columns)-1 {
			sb.WriteString("-+-")
		}
	}
	sb.WriteString("\n")

	rowCount := 0
	vals := make([]any, len(columns))
	valPtrs := make([]any, len(columns))
	for i := range vals {
		valPtrs[i] = &vals[i]
	}

	for rows.Next() && rowCount < maxRows {
		if err := rows.Scan(valPtrs...); err != nil {
			return Result{}, fmt.Errorf("sql: scan row: %w", err)
		}
		parts := make([]string, len(columns))
		for i, v := range vals {
			if v == nil {
				parts[i] = "NULL"
			} else {
				parts[i] = fmt.Sprintf("%v", v)
			}
		}
		sb.WriteString(strings.Join(parts, " | "))
		sb.WriteString("\n")
		rowCount++
	}

	if rowCount >= maxRows {
		sb.WriteString(fmt.Sprintf("... (limited to %d rows)\n", maxRows))
	}

	sb.WriteString(fmt.Sprintf("(%d rows)\n", rowCount))
	return Result{Content: sb.String()}, nil
}
