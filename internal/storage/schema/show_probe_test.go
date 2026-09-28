package schema

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func withShowProbes(t *testing.T) {
	t.Helper()
	prev := useShowProbes
	useShowProbes = true
	t.Cleanup(func() { useShowProbes = prev })
}

func newShowMock(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func TestShowTableExistsNoDatabaseIssuesNoShow(t *testing.T) {
	withShowProbes(t)
	db, mock := newShowMock(t)
	mock.ExpectQuery("SELECT DATABASE()").WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(nil))
	got, err := schemaTableExists(context.Background(), db, "schema_migrations")
	if err != nil || got {
		t.Fatalf("got %v, %v; want false, nil", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestShowTableExistsExactNameOnly(t *testing.T) {
	withShowProbes(t)
	cases := []struct {
		name   string
		listed []string
		want   bool
	}{
		{"present", []string{"schema_migrations"}, true},
		{"case-insensitive like the INFORMATION_SCHEMA compare", []string{"Schema_Migrations"}, true},
		{"LIKE wildcard over-match is filtered", []string{"schemaXmigrations"}, false},
		{"absent", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newShowMock(t)
			mock.ExpectQuery("SELECT DATABASE()").WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("hq"))
			rows := sqlmock.NewRows([]string{"Tables_in_hq"})
			for _, n := range tc.listed {
				rows.AddRow(n)
			}
			mock.ExpectQuery("SHOW TABLES LIKE 'schema_migrations'").WillReturnRows(rows)
			got, err := schemaTableExists(context.Background(), db, "schema_migrations")
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestShowColumnExistsNeverShowsColumnsOfAMissingTable(t *testing.T) {
	withShowProbes(t)
	db, mock := newShowMock(t)
	mock.ExpectQuery("SELECT DATABASE()").WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("hq"))
	mock.ExpectQuery("SHOW TABLES LIKE 'wisps'").WillReturnRows(sqlmock.NewRows([]string{"Tables_in_hq"}))
	got, err := schemaColumnExists(context.Background(), db, "wisps", "is_blocked")
	if err != nil || got {
		t.Fatalf("got %v, %v; want false, nil", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestShowColumnExistsReadsFieldColumn(t *testing.T) {
	withShowProbes(t)
	for _, tc := range []struct {
		name  string
		field string
		want  bool
	}{{"present", "content_hash", true}, {"over-match filtered", "contentXhash", false}} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newShowMock(t)
			mock.ExpectQuery("SELECT DATABASE()").WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("hq"))
			mock.ExpectQuery("SHOW TABLES LIKE 'issues'").WillReturnRows(sqlmock.NewRows([]string{"Tables_in_hq"}).AddRow("issues"))
			mock.ExpectQuery("SHOW COLUMNS FROM `issues` LIKE 'content_hash'").WillReturnRows(
				sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
					AddRow(tc.field, "varchar(64)", "YES", "", nil, ""))
			got, err := columnExists(context.Background(), db, "issues", "content_hash")
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestShowProbeQuotesNames(t *testing.T) {
	if got, want := sqlIdentifier("we`ird"), "`we``ird`"; got != want {
		t.Fatalf("sqlIdentifier = %q, want %q", got, want)
	}
	if !regexp.MustCompile(`^'o''k'$`).MatchString(sqlStringLiteral("o'k")) {
		t.Fatalf("sqlStringLiteral(o'k) = %q", sqlStringLiteral("o'k"))
	}
}
