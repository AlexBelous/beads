//go:build livecheck

package schema

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// TestLiveShowProbeEquivalence compares the SHOW and INFORMATION_SCHEMA answers
// for every table and column of every database on a live server, plus absent
// names. Run: BEADS_LIVECHECK_DSN='root@tcp(127.0.0.1:42068)/' go test -tags livecheck -run TestLiveShowProbeEquivalence
func TestLiveShowProbeEquivalence(t *testing.T) {
	dsn := os.Getenv("BEADS_LIVECHECK_DSN")
	if dsn == "" {
		t.Skip("BEADS_LIVECHECK_DSN not set")
	}
	ctx := context.Background()
	root, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var dbs []string
	rows, err := root.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		if n != "information_schema" && n != "mysql" && !strings.HasPrefix(n, "__") {
			dbs = append(dbs, n)
		}
	}
	rows.Close()
	checked := 0
	for _, name := range dbs {
		conn, err := root.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "USE "+sqlIdentifier(name)); err != nil {
			conn.Close()
			continue
		}
		answer := func(show bool, fn func() (bool, error)) bool {
			prev := useShowProbes
			useShowProbes = show
			defer func() { useShowProbes = prev }()
			got, err := fn()
			if err != nil {
				t.Fatalf("%s: probe error (show=%v): %v", name, show, err)
			}
			return got
		}
		var tables []string
		trs, err := conn.QueryContext(ctx, "SHOW TABLES")
		if err != nil {
			t.Fatal(err)
		}
		for trs.Next() {
			var n string
			_ = trs.Scan(&n)
			tables = append(tables, n)
		}
		trs.Close()
		for _, table := range append(tables, "no_such_table_x", "schema_migrationz") {
			table := table
			a := answer(false, func() (bool, error) { return schemaTableExists(ctx, conn, table) })
			b := answer(true, func() (bool, error) { return schemaTableExists(ctx, conn, table) })
			checked++
			if a != b {
				t.Errorf("%s.%s: table exists information_schema=%v show=%v", name, table, a, b)
			}
			var cols []string
			if a {
				crs, err := conn.QueryContext(ctx, "SHOW COLUMNS FROM "+sqlIdentifier(table))
				if err == nil {
					for crs.Next() {
						vals := make([]any, 6)
						ptrs := make([]any, 6)
						for i := range vals {
							ptrs[i] = &vals[i]
						}
						if cn, _ := crs.Columns(); len(cn) != 6 {
							vals = make([]any, len(cn))
							ptrs = make([]any, len(cn))
							for i := range vals {
								ptrs[i] = &vals[i]
							}
						}
						_ = crs.Scan(ptrs...)
						if b, ok := vals[0].([]byte); ok {
							cols = append(cols, string(b))
						}
					}
					crs.Close()
				}
			}
			for _, col := range append(cols, "no_such_column_x") {
				col := col
				a := answer(false, func() (bool, error) { return schemaColumnExists(ctx, conn, table, col) })
				b := answer(true, func() (bool, error) { return schemaColumnExists(ctx, conn, table, col) })
				c := answer(false, func() (bool, error) { return columnExists(ctx, conn, table, col) })
				checked++
				if a != b || a != c {
					t.Errorf("%s.%s.%s: column exists information_schema=%v show=%v columnExists(legacy)=%v", name, table, col, a, b, c)
				}
			}
		}
		conn.Close()
	}
	t.Logf("databases=%d probes compared=%d", len(dbs), checked)
}
