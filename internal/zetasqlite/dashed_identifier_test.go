package zetasqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// TestDashedIdentifier_DriverScope pins how dashed identifier paths are
// resolved end-to-end through the zetasqlite driver, against namePath
// `["my-dashed-project","ds"]`. Cases are grouped by behavior:
//
//   - wantErr == "": parser + analyzer + catalog accept the SQL.
//   - wantErr != "": current behavior is rejection (either parser/analyzer
//     scope of FEATURE_V_1_3_ALLOW_DASHES_IN_TABLE_NAME, or an
//     emulator-side catalog gap that this pin makes regress-detectable).
//
// wantErr is a substring match against err.Error() because emulator does
// not surface sentinel errors for these failure modes — the zetasql
// analyzer/parser messages are passed through verbatim.
//
// MERGE INTO is intentionally excluded — its current failure is an
// emulator MERGE-implementation limitation unrelated to dashed
// identifiers.
func TestDashedIdentifier_DriverScope(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantErr string
	}{
		{
			name: "INSERT INTO dashed-qualified table",
			sql:  `INSERT INTO my-dashed-project.ds.t (id, val) VALUES (10, 'x')`,
		},
		{
			name: "UPDATE dashed-qualified table",
			sql:  `UPDATE my-dashed-project.ds.t SET val = 'updated' WHERE id = 1`,
		},
		{
			name: "DELETE FROM dashed-qualified table",
			sql:  `DELETE FROM my-dashed-project.ds.t WHERE id = 2`,
		},
		{
			name: "JOIN with dashed-qualified tables on both sides",
			sql: `SELECT a.id, b.val
FROM my-dashed-project.ds.t AS a
JOIN my-dashed-project.ds.src AS b
ON a.id = b.id`,
		},
		{
			name: "FROM with backtick-quoted dataset segment",
			sql:  "SELECT * FROM my-dashed-project.`ds`.t WHERE id = 1",
		},
		{
			name: "FROM with whole-path backtick quote",
			sql:  "SELECT * FROM `my-dashed-project.ds.t` WHERE id = 1",
		},
		{
			name: "FROM with backtick-quoted project segment",
			sql:  "SELECT * FROM `my-dashed-project`.ds.t WHERE id = 1",
		},
		{
			// Emulator-side gap: INFORMATION_SCHEMA view is not
			// registered under a dashed project's catalog node.
			// Flip wantErr to "" when the gap is closed.
			name:    "INFORMATION_SCHEMA under dashed project (currently unsupported)",
			sql:     `SELECT table_name FROM my-dashed-project.ds.INFORMATION_SCHEMA.TABLES`,
			wantErr: "Table not found: `my-dashed-project`.ds.INFORMATION_SCHEMA.TABLES",
		},
		{
			// ZetaSQL parser scope: FEATURE_V_1_3_ALLOW_DASHES_IN_TABLE_NAME
			// covers table names in path expressions, not the name
			// position of CREATE TABLE FUNCTION.
			name:    "CREATE TABLE FUNCTION with dashed name (parser scope-外)",
			sql:     `CREATE TABLE FUNCTION my-dashed-project.ds.my_tvf() AS (SELECT id, val FROM my-dashed-project.ds.t)`,
			wantErr: "Syntax error: Expected (",
		},
		{
			// Same parser scope as above on the call-site.
			name:    "TVF call with dashed-qualified name (parser scope-外)",
			sql:     `SELECT * FROM my-dashed-project.ds.my_tvf()`,
			wantErr: `Syntax error: Expected ";" or end of input but got "("`,
		},
		{
			// ZetaSQL analyzer scope: dashed extension applies to
			// table paths, not to column references inside a SELECT
			// expression — the analyzer parses `my` as an
			// identifier and rejects the dash.
			name:    "4-part column reference proj.ds.t.col (analyzer scope-外)",
			sql:     `SELECT my-dashed-project.ds.t.id FROM my-dashed-project.ds.t WHERE id = 1`,
			wantErr: "Unrecognized name: my",
		},
		{
			// Emulator-side gap: region-qualified INFORMATION_SCHEMA
			// (`region-us.INFORMATION_SCHEMA.JOBS_BY_PROJECT`) is
			// not registered in the catalog. Flip wantErr to "" when
			// the gap is closed.
			name:    "region-qualified INFORMATION_SCHEMA (currently unsupported)",
			sql:     `SELECT job_id FROM region-us.INFORMATION_SCHEMA.JOBS_BY_PROJECT`,
			wantErr: "Table not found: `region-us`.INFORMATION_SCHEMA.JOBS_BY_PROJECT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDashedFixture(t)
			_, err := db.Exec(tc.sql)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Exec err: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Exec succeeded, want err containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Exec err = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// dashedDriverCounter gives each openDashedFixture call a globally unique
// driver name. sql.Register cannot be called twice with the same name in
// the same process, which is what `go test -count=N>1` would do if the
// driver name were derived from t.Name() alone.
var dashedDriverCounter atomic.Int64

func openDashedFixture(t *testing.T) *sql.DB {
	t.Helper()
	driverName := fmt.Sprintf("zetasqlite-dashed-%d", dashedDriverCounter.Add(1))
	sql.Register(driverName, &ZetaSQLiteDriver{
		ConnectHook: func(conn *ZetaSQLiteConn) error {
			return conn.SetNamePath([]string{"my-dashed-project", "ds"})
		},
	})
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS t (id INT64 NOT NULL, val STRING)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS src (id INT64 NOT NULL, val STRING)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t (id, val) VALUES (1, 'a'), (2, 'b')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO src (id, val) VALUES (2, 'B'), (3, 'C')`); err != nil {
		t.Fatal(err)
	}
	return db
}
