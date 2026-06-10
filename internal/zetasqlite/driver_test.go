package zetasqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDriver(t *testing.T) {
	db, err := sql.Open("zetasqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS Singers (
  SingerId   INT64 NOT NULL,
  FirstName  STRING(1024),
  LastName   STRING(1024),
  SingerInfo BYTES(MAX)
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT Singers (SingerId, FirstName, LastName) VALUES (1, 'John', 'Titor')`); err != nil {
		t.Fatal(err)
	}
	row := db.QueryRow("SELECT SingerID, FirstName, LastName FROM Singers WHERE SingerId = @id", 1)
	if row.Err() != nil {
		t.Fatal(row.Err())
	}
	var (
		singerID  int64
		firstName string
		lastName  string
	)
	if err := row.Scan(&singerID, &firstName, &lastName); err != nil {
		t.Fatal(err)
	}
	if singerID != 1 || firstName != "John" || lastName != "Titor" {
		t.Fatalf("failed to find row %v %v %v", singerID, firstName, lastName)
	}
	if _, err := db.Exec(`
CREATE VIEW IF NOT EXISTS SingerNames AS SELECT FirstName || ' ' || LastName AS Name FROM Singers`); err != nil {
		t.Fatal(err)
	}

	viewRow := db.QueryRow("SELECT Name FROM SingerNames LIMIT 1")
	if viewRow.Err() != nil {
		t.Fatal(viewRow.Err())
	}

	var name string

	if err := viewRow.Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "John Titor" {
		t.Fatalf("failed to find view row")
	}
}

func TestRegisterCustomDriver(t *testing.T) {
	sql.Register("zetasqlite-custom", &ZetaSQLiteDriver{
		ConnectHook: func(conn *ZetaSQLiteConn) error {
			return conn.SetNamePath([]string{"project-id", "datasetID"})
		},
	})
	db, err := sql.Open("zetasqlite-custom", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tableID (Id INT64 NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT `project-id`.datasetID.tableID (Id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	row := db.QueryRow("SELECT * FROM project-id.datasetID.tableID WHERE Id = ?", 1)
	if row.Err() != nil {
		t.Fatal(row.Err())
	}
	var id int64
	if err := row.Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("failed to find row %v", id)
	}
}

func TestChangedCatalog(t *testing.T) {
	t.Run("table", func(t *testing.T) {
		db, err := sql.Open("zetasqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		result, err := db.Exec(`
CREATE TABLE IF NOT EXISTS Singers (
  SingerId   INT64 NOT NULL,
  FirstName  STRING(1024),
  LastName   STRING(1024),
  SingerInfo BYTES(MAX)
)`)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query(`DROP TABLE Singers`)
		if err != nil {
			t.Fatal(err)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		resultCatalog, err := ChangedCatalogFromResult(result)
		if err != nil {
			t.Fatal(err)
		}
		if !resultCatalog.Changed() {
			t.Fatal("failed to get changed catalog")
		}
		if len(resultCatalog.Table.Added) != 1 {
			t.Fatal("failed to get created table spec")
		}
		if diff := cmp.Diff(resultCatalog.Table.Added[0].NamePath, []string{"Singers"}); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
		rowsCatalog, err := ChangedCatalogFromRows(rows)
		if err != nil {
			t.Fatal(err)
		}
		if !rowsCatalog.Changed() {
			t.Fatal("failed to get changed catalog")
		}
		if len(rowsCatalog.Table.Deleted) != 1 {
			t.Fatal("failed to get deleted table spec")
		}
		if diff := cmp.Diff(rowsCatalog.Table.Deleted[0].NamePath, []string{"Singers"}); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})
	t.Run("schema", func(t *testing.T) {
		// CREATE SCHEMA / DROP SCHEMA are metadata-only operations
		// in the emulator (no SQLite-side schema is created), so the
		// contract this test pins is the ChangedCatalog signal: SQL
		// engine surfaces the dataset identity + OPTIONS to the
		// server layer, which is the only place the metaRepo can be
		// updated. If this assertion regresses, dbt's `create_schema`
		// will silently no-op on the server side.
		db, err := sql.Open("zetasqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		result, err := db.ExecContext(
			context.Background(),
			`CREATE SCHEMA IF NOT EXISTS newds OPTIONS(description='for dbt', labels=[("env","dev")], default_table_expiration_days=7)`,
		)
		if err != nil {
			t.Fatal(err)
		}
		resultCatalog, err := ChangedCatalogFromResult(result)
		if err != nil {
			t.Fatal(err)
		}
		if !resultCatalog.Changed() {
			t.Fatal("failed to get changed catalog")
		}
		if len(resultCatalog.Dataset.Added) != 1 {
			t.Fatalf("expected 1 added dataset, got %d", len(resultCatalog.Dataset.Added))
		}
		added := resultCatalog.Dataset.Added[0]
		if diff := cmp.Diff([]string{"newds"}, added.NamePath); diff != "" {
			t.Errorf("NamePath (-want +got):\n%s", diff)
		}
		if added.Options.Description != "for dbt" {
			t.Errorf("Description: want %q, got %q", "for dbt", added.Options.Description)
		}
		if diff := cmp.Diff(map[string]string{"env": "dev"}, added.Options.Labels); diff != "" {
			t.Errorf("Labels (-want +got):\n%s", diff)
		}
		if added.Options.DefaultTableExpirationDays != 7 {
			t.Errorf("DefaultTableExpirationDays: want %d, got %d", 7, added.Options.DefaultTableExpirationDays)
		}

		rows, err := db.QueryContext(context.Background(), `DROP SCHEMA newds`)
		if err != nil {
			t.Fatal(err)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rowsCatalog, err := ChangedCatalogFromRows(rows)
		if err != nil {
			t.Fatal(err)
		}
		if len(rowsCatalog.Dataset.Deleted) != 1 {
			t.Fatalf("expected 1 deleted dataset, got %d", len(rowsCatalog.Dataset.Deleted))
		}
		if diff := cmp.Diff([]string{"newds"}, rowsCatalog.Dataset.Deleted[0].NamePath); diff != "" {
			t.Errorf("Deleted NamePath (-want +got):\n%s", diff)
		}
	})
	t.Run("schema_unknown_options", func(t *testing.T) {
		// Options BigQuery defines but that the emulator does not
		// persist (no field on bigqueryv2.Dataset) must still be
		// accepted on the wire — rejecting them would break dbt and
		// other clients. The contract is that those names land in
		// UnknownOptions so the server layer can emit a WARN log
		// (silent ignore is the failure mode this guards against).
		db, err := sql.Open("zetasqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		result, err := db.ExecContext(
			context.Background(),
			`CREATE SCHEMA partial_persist OPTIONS(description='ok', default_kms_key_name='projects/p/locations/us/keyRings/r/cryptoKeys/k')`,
		)
		if err != nil {
			t.Fatal(err)
		}
		resultCatalog, err := ChangedCatalogFromResult(result)
		if err != nil {
			t.Fatal(err)
		}
		added := resultCatalog.Dataset.Added[0]
		if added.Options.Description != "ok" {
			t.Errorf("Description: want %q, got %q", "ok", added.Options.Description)
		}
		if diff := cmp.Diff([]string{"default_kms_key_name"}, added.UnknownOptions); diff != "" {
			t.Errorf("UnknownOptions (-want +got):\n%s", diff)
		}
	})
	t.Run("function", func(t *testing.T) {
		db, err := sql.Open("zetasqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		result, err := db.ExecContext(context.Background(), `CREATE FUNCTION ANY_ADD(x ANY TYPE, y ANY TYPE) AS ((x + 4) / y)`)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.QueryContext(context.Background(), `DROP FUNCTION ANY_ADD`)
		if err != nil {
			t.Fatal(err)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		resultCatalog, err := ChangedCatalogFromResult(result)
		if err != nil {
			t.Fatal(err)
		}
		if !resultCatalog.Changed() {
			t.Fatal("failed to get changed catalog")
		}
		if len(resultCatalog.Function.Added) != 1 {
			t.Fatal("failed to get created function spec")
		}
		if diff := cmp.Diff(resultCatalog.Function.Added[0].NamePath, []string{"ANY_ADD"}); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
		rowsCatalog, err := ChangedCatalogFromRows(rows)
		if err != nil {
			t.Fatal(err)
		}
		if !rowsCatalog.Changed() {
			t.Fatal("failed to get changed catalog")
		}
		if len(rowsCatalog.Function.Deleted) != 1 {
			t.Fatal("failed to get deleted function spec")
		}
		if diff := cmp.Diff(rowsCatalog.Function.Deleted[0].NamePath, []string{"ANY_ADD"}); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})
}

func TestPreparedStatements(t *testing.T) {
	t.Run("prepared select", func(t *testing.T) {
		db, err := sql.Open("zetasqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS Singers (
  SingerId   INT64 NOT NULL,
  FirstName  STRING(1024),
  LastName   STRING(1024),
  SingerInfo BYTES(MAX)
)`); err != nil {
			t.Fatal(err)
		}
		stmt, err := db.Prepare("SELECT * FROM Singers WHERE SingerId = ?")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := stmt.Query("123")
		if err != nil {
			t.Fatal(err)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if rows.Next() {
			t.Fatal("found unexpected row; expected no rows")
		}
	})
	t.Run("prepared insert", func(t *testing.T) {
		db, err := sql.Open("zetasqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS Items (ItemId   INT64 NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT `Items` (`ItemId`) VALUES (123)"); err != nil {
			t.Fatal(err)
		}

		// Test that executing without args fails
		_, err = db.Exec("INSERT `Items` (`ItemId`) VALUES (?)")
		if err == nil {
			t.Fatal("expected error when inserting without args; got no error")
		}

		stmt, err := db.Prepare("INSERT `Items` (`ItemId`) VALUES (?)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stmt.Exec(456); err != nil {
			t.Fatal(err)
		}

		stmt, err = db.PrepareContext(context.Background(), "INSERT `Items` (`ItemId`) VALUES (?)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stmt.Exec(456); err != nil {
			t.Fatal(err)
		}

		rows, err := db.Query("SELECT * FROM Items WHERE ItemId = 456")
		if err != nil {
			t.Fatal(err)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !rows.Next() {
			t.Fatal("expected no rows; expected one row")
		}
	})
}
