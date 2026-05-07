package zetasqlite

import (
	"database/sql"
	"testing"
)

// TestNamedQueryParameterAnalyzed locks in the fix for the boot regression
// reported after the zetasql-wasm v0.5.0 migration: named query parameters
// were not registered with the analyzer, so any query referencing @name
// failed with "Query parameter 'name' not found" before the metadata layer
// could complete its first SELECT and the server never reached Listen.
//
// The test uses the same shape that the metadata layer issues at startup
// (`WHERE x IN UNNEST(@ids)` style) so that a re-introduction of the bug
// surfaces here long before bigquery-emulator boot does.
func TestNamedQueryParameterAnalyzed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		args  []any
		want  int
	}{
		{
			name:  "unnest string array",
			query: "SELECT x FROM UNNEST(@ids) AS x",
			args:  []any{sql.Named("ids", []string{"a", "b", "c"})},
			want:  3,
		},
		{
			name:  "unnest int array",
			query: "SELECT x FROM UNNEST(@ids) AS x",
			args:  []any{sql.Named("ids", []int64{10, 20})},
			want:  2,
		},
		{
			name:  "unnest empty array",
			query: "SELECT x FROM UNNEST(@ids) AS x",
			args:  []any{sql.Named("ids", []string{})},
			want:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("zetasqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			rows, err := db.QueryContext(t.Context(), tc.query, tc.args...)
			if err != nil {
				t.Fatalf("QueryContext: %v", err)
			}
			defer rows.Close()

			got := 0
			for rows.Next() {
				got++
			}
			if rows.Err() != nil {
				t.Fatalf("rows.Err: %v", rows.Err())
			}
			if got != tc.want {
				t.Errorf("row count: got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestPositionalQueryParameterAnalyzed mirrors TestNamedQueryParameterAnalyzed
// for positional ("?") parameters, which require the analyzer to disable
// AllowUndeclaredParameters mode in addition to publishing the parameter
// types up-front.
func TestPositionalQueryParameterAnalyzed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		args  []any
		want  int
	}{
		{
			name:  "unnest positional string array",
			query: "SELECT x FROM UNNEST(?) AS x",
			args:  []any{[]string{"alpha", "beta"}},
			want:  2,
		},
		{
			name:  "unnest positional empty array",
			query: "SELECT x FROM UNNEST(?) AS x",
			args:  []any{[]string{}},
			want:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("zetasqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			rows, err := db.QueryContext(t.Context(), tc.query, tc.args...)
			if err != nil {
				t.Fatalf("QueryContext: %v", err)
			}
			defer rows.Close()

			got := 0
			for rows.Next() {
				got++
			}
			if rows.Err() != nil {
				t.Fatalf("rows.Err: %v", rows.Err())
			}
			if got != tc.want {
				t.Errorf("row count: got %d, want %d", got, tc.want)
			}
		})
	}
}
