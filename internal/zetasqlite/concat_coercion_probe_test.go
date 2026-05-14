package zetasqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// TestProbeConcatCoercion runs through `||` operator and sibling string-sink
// cases to record which inputs the emulator currently accepts or rejects.
//
// The goal is observation, not assertion: every case logs its outcome and the
// test only fails if database open itself fails. The output is what we use to
// fill in a support matrix vs. BigQuery production behavior.
func TestProbeConcatCoercion(t *testing.T) {
	t.Setenv("TZ", "UTC")
	ctx := context.Background()
	db, err := sql.Open("zetasqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	type probe struct {
		group string
		name  string
		sql   string
	}
	cases := []probe{
		// --- BQ doc spec: STRING / BYTES / ARRAY<T> ---
		{"doc-spec", "string||string", `SELECT "a" || "b"`},
		{"doc-spec", "string||string||string", `SELECT "a" || "b" || "c"`},
		{"doc-spec", "string||NULL", `SELECT "a" || CAST(NULL AS STRING)`},
		{"doc-spec", "NULL||string", `SELECT CAST(NULL AS STRING) || "a"`},
		{"doc-spec", "bytes||bytes", `SELECT b"a" || b"b"`},
		{"doc-spec", "bytes||bytes||bytes", `SELECT b"a" || b"b" || b"c"`},
		{"doc-spec", "array<int64>||array<int64>", `SELECT [1, 2] || [3, 4]`},
		{"doc-spec", "array<string>||array<string>", `SELECT ["a"] || ["b", "c"]`},
		{"doc-spec", "array<date>||array<date>", `SELECT [DATE "2026-01-01"] || [DATE "2026-02-01"]`},

		// --- Cross-type within doc (should error per BQ) ---
		{"doc-cross", "string||bytes (expected reject)", `SELECT "a" || b"b"`},
		{"doc-cross", "string||array (expected reject)", `SELECT "a" || [1]`},

		// --- Issue #98: doc-undefined extensions BQ production accepts ---
		{"ext-#98", `"abc" || 123 (int64)`, `SELECT "abc" || 123`},
		{"ext-#98", `123 || "abc" (int64 first)`, `SELECT 123 || "abc"`},
		{"ext-#98", `"abc" || 1.5 (float64)`, `SELECT "abc" || 1.5`},
		{"ext-#98", `"abc" || DATE "2026-01-01"`, `SELECT "abc" || DATE "2026-01-01"`},
		{"ext-#98", `"abc" || TIMESTAMP`, `SELECT "abc" || TIMESTAMP "2026-01-01 00:00:00 UTC"`},
		{"ext-#98", `"abc" || TRUE`, `SELECT "abc" || TRUE`},
		{"ext-#98", `int + string ("p-" || id)`, `WITH t AS (SELECT 7 AS id) SELECT "p-" || t.id FROM t`},

		// --- Sibling sink: CONCAT (issue #43, same root) ---
		{"sibling-CONCAT", `CONCAT(string,int64)`, `SELECT CONCAT("a", 1)`},
		{"sibling-CONCAT", `CONCAT(string,float64)`, `SELECT CONCAT("a", 1.5)`},
		{"sibling-CONCAT", `CONCAT(string,date)`, `SELECT CONCAT("a", DATE "2026-01-01")`},
		{"sibling-CONCAT", `CONCAT(string,bool)`, `SELECT CONCAT("a", TRUE)`},

		// --- Sibling sink: other STRING-only signatures ---
		{"sibling-STARTS_WITH", `STARTS_WITH(int,string)`, `SELECT STARTS_WITH(123, "1")`},
		{"sibling-ENDS_WITH", `ENDS_WITH(int,string)`, `SELECT ENDS_WITH(123, "3")`},
		{"sibling-REPLACE", `REPLACE(int,string,string)`, `SELECT REPLACE(123, "2", "x")`},
		{"sibling-STRPOS", `STRPOS(int,string)`, `SELECT STRPOS(123, "2")`},
		{"sibling-SUBSTR", `SUBSTR(int,int)`, `SELECT SUBSTR(12345, 2)`},
		{"sibling-LENGTH", `LENGTH(int)`, `SELECT LENGTH(123)`},
		{"sibling-LOWER", `LOWER(int)`, `SELECT LOWER(123)`},
		{"sibling-UPPER", `UPPER(int)`, `SELECT UPPER(123)`},
		{"sibling-LPAD", `LPAD(int,int,string)`, `SELECT LPAD(7, 3, "0")`},
		{"sibling-REGEXP_CONTAINS", `REGEXP_CONTAINS(int,string)`, `SELECT REGEXP_CONTAINS(123, r"\d+")`},
		{"sibling-REGEXP_EXTRACT", `REGEXP_EXTRACT(int,string)`, `SELECT REGEXP_EXTRACT(123, r"\d+")`},
		{"sibling-REGEXP_REPLACE", `REGEXP_REPLACE(int,string,string)`, `SELECT REGEXP_REPLACE(123, r"\d", "x")`},
		{"sibling-FORMAT", `FORMAT(string,int)`, `SELECT FORMAT("%d", 1)`},
		{"sibling-ARRAY_TO_STRING", `ARRAY_TO_STRING([int,int],string)`, `SELECT ARRAY_TO_STRING([1, 2], "-")`},
		{"sibling-SPLIT", `SPLIT(int,string)`, `SELECT SPLIT(123, "2")`},
	}

	for _, c := range cases {
		c := c
		t.Run(fmt.Sprintf("%s/%s", c.group, c.name), func(t *testing.T) {
			rows, err := db.QueryContext(ctx, c.sql)
			if err != nil {
				t.Logf("[REJECT] %s -> %s", c.sql, oneLine(err.Error()))
				return
			}
			defer rows.Close()
			cols, _ := rows.Columns()
			outs := []string{}
			for rows.Next() {
				vals := make([]interface{}, len(cols))
				ptrs := make([]interface{}, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					t.Logf("[SCAN-ERR] %s -> %s", c.sql, oneLine(err.Error()))
					return
				}
				outs = append(outs, fmt.Sprintf("%v", vals))
			}
			if err := rows.Err(); err != nil {
				t.Logf("[ROWS-ERR] %s -> %s", c.sql, oneLine(err.Error()))
				return
			}
			t.Logf("[ACCEPT] %s -> %s", c.sql, strings.Join(outs, " | "))
		})
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	return s
}
