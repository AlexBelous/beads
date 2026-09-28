package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// useShowProbes routes the existence probes (schemaTableExists,
// schemaColumnExists, columnExists, sentinelTableExists and the cursor probe in
// currentVersion) through SHOW instead of INFORMATION_SCHEMA. The
// INFORMATION_SCHEMA branch stays as the fallback and is what the sqlmock-based
// tests of this package exercise (show_probe_mode_test.go switches it off).
var useShowProbes = true

// Existence probes answered with SHOW instead of INFORMATION_SCHEMA.
//
// Dolt materializes INFORMATION_SCHEMA.TABLES and .COLUMNS across every
// database on the server before the WHERE applies. On a shared sql-server with
// a handful of rig databases, one COLUMNS probe measured 115-122 ms and one
// TABLES probe 12-14 ms, while SHOW COLUMNS ... LIKE and SHOW TABLES LIKE
// answered the same question in 2.0 ms and 0.8 ms. Every CLI open runs these
// probes on the lock-free convergence path (alreadyConverged), so on a busy
// city they were the single largest query class on the server.
//
// The answers are the same questions asked of the same session catalog:
//   - no database selected: false, exactly what COUNT(*) ... WHERE
//     TABLE_SCHEMA = DATABASE() returned, and no statement that could fail is
//     issued (a failing statement pins a Dolt session to its pre-statement
//     catalog snapshot, be-bv7x);
//   - a column is only looked up after SHOW TABLES has proven its table, so
//     SHOW COLUMNS never runs against a missing table;
//   - LIKE only narrows the rows; the name is then compared exactly (Dolt
//     identifiers are case-insensitive, as the INFORMATION_SCHEMA compare was),
//     so '_' and '%' in a name cannot over-match.

// sessionHasDatabase reports whether the session has a current database.
func sessionHasDatabase(ctx context.Context, db DBConn) (bool, error) {
	var name sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
		return false, err
	}
	return name.Valid && name.String != "", nil
}

func showTableExists(ctx context.Context, db DBConn, table string) (bool, error) {
	selected, err := sessionHasDatabase(ctx, db)
	if err != nil || !selected {
		return false, err
	}
	return showNameListed(ctx, db, "SHOW TABLES LIKE "+sqlStringLiteral(table), table)
}

func showColumnExists(ctx context.Context, db DBConn, table, column string) (bool, error) {
	present, err := showTableExists(ctx, db, table)
	if err != nil || !present {
		return false, err
	}
	return showNameListed(ctx, db,
		"SHOW COLUMNS FROM "+sqlIdentifier(table)+" LIKE "+sqlStringLiteral(column), column)
}

// showNameListed runs a SHOW whose first result column is a name and reports
// whether want is among the rows.
func showNameListed(ctx context.Context, db DBConn, query, want string) (bool, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return false, err
	}
	if len(cols) == 0 {
		return false, fmt.Errorf("%s: no result columns", query)
	}
	found := false
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return false, err
		}
		var name string
		switch v := values[0].(type) {
		case []byte:
			name = string(v)
		case string:
			name = v
		default:
			name = fmt.Sprint(v)
		}
		if strings.EqualFold(name, want) {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return found, nil
}

// sqlStringLiteral is defined in schema.go.

func sqlIdentifier(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}
