package zetasqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// TestProbeOperatorList runs every operator listed in the BigQuery
// "Operators" reference and records what the emulator does. Cases are taken
// from the doc's canonical examples plus a few edges (NULL, out-of-range,
// NaN, divide-by-zero). Pass/fail is observation-only; the test only fails
// if database open itself fails.
func TestProbeOperatorList(t *testing.T) {
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
		// ============================================================
		// 1. Field access operator (`.`)
		// ============================================================
		{"field-access", "struct.field", `SELECT STRUCT('Yonge Street' AS street, 'Canada' AS country).country`},
		{"field-access", "nested struct.field.field", `SELECT STRUCT(STRUCT('Yonge Street' AS street, 'Canada' AS country) AS address).address.country`},
		{"field-access", "json.field", `SELECT JSON '{"a": 1}'.a`},
		{"field-access", "json.missing (expect NULL per doc)", `SELECT JSON '{"a": 1}'.missing`},

		// ============================================================
		// 2. Array subscript operator
		// ============================================================
		{"array-subscript", "bare [0]", `SELECT ["coffee", "tea", "milk"][0]`},
		{"array-subscript", "OFFSET(0)", `SELECT ["coffee", "tea", "milk"][OFFSET(0)]`},
		{"array-subscript", "ORDINAL(1)", `SELECT ["coffee", "tea", "milk"][ORDINAL(1)]`},
		{"array-subscript", "SAFE_OFFSET(6) -> NULL", `SELECT ["coffee", "tea", "milk"][SAFE_OFFSET(6)]`},
		{"array-subscript", "SAFE_ORDINAL(6) -> NULL", `SELECT ["coffee", "tea", "milk"][SAFE_ORDINAL(6)]`},
		{"array-subscript", "[6] out-of-range (expect error)", `SELECT ["coffee", "tea", "milk"][6]`},
		{"array-subscript", "OFFSET(6) out-of-range (expect error)", `SELECT ["coffee", "tea", "milk"][OFFSET(6)]`},

		// ============================================================
		// 3. Struct subscript operator
		// ============================================================
		{"struct-subscript", "bare [0]", `SELECT STRUCT<INT64, STRING, BOOL>(23, "tea", FALSE)[0]`},
		{"struct-subscript", "OFFSET(0)", `SELECT STRUCT<INT64, STRING, BOOL>(23, "tea", FALSE)[OFFSET(0)]`},
		{"struct-subscript", "ORDINAL(1)", `SELECT STRUCT<INT64, STRING, BOOL>(23, "tea", FALSE)[ORDINAL(1)]`},
		{"struct-subscript", "[6] out-of-range (expect error)", `SELECT STRUCT<INT64, STRING, BOOL>(23, "tea", FALSE)[6]`},

		// ============================================================
		// 4. JSON subscript operator
		// ============================================================
		{"json-subscript", "field-name", `SELECT JSON '{"a": 1}'["a"]`},
		{"json-subscript", "array-index", `SELECT JSON '[1, 2, 3]'[0]`},
		{"json-subscript", "missing field (expect NULL)", `SELECT JSON '{"a": 1}'["b"]`},
		{"json-subscript", "doc example chained", `SELECT JSON '{"class":{"students":[{"name":"Jane"}]}}'.class.students[0]["name"]`},

		// ============================================================
		// 5. Arithmetic operators
		// ============================================================
		{"arithmetic", "INT64 +", `SELECT 1 + 2`},
		{"arithmetic", "INT64 -", `SELECT 5 - 3`},
		{"arithmetic", "INT64 *", `SELECT 2 * 3`},
		{"arithmetic", "INT64 / -> FLOAT64", `SELECT 10 / 2`},
		{"arithmetic", "unary +", `SELECT +1`},
		{"arithmetic", "unary -", `SELECT -2`},
		{"arithmetic", "INT + FLOAT -> FLOAT", `SELECT 1 + 1.5`},
		{"arithmetic", "NUMERIC + INT", `SELECT CAST(1 AS NUMERIC) + 1`},
		{"arithmetic", "BIGNUMERIC * INT", `SELECT CAST(2 AS BIGNUMERIC) * 3`},
		{"arithmetic", "1/0 (expect error)", `SELECT 1 / 0`},

		// ============================================================
		// 6. Date arithmetic operators
		// ============================================================
		{"date-arith", "DATE + INT64", `SELECT DATE "2020-09-22" + 1`},
		{"date-arith", "INT64 + DATE", `SELECT 1 + DATE "2020-09-22"`},
		{"date-arith", "DATE - INT64", `SELECT DATE "2020-09-22" - 7`},

		// ============================================================
		// 7. Datetime subtraction (-> INTERVAL)
		// ============================================================
		{"datetime-sub", "DATE - DATE", `SELECT DATE "2021-05-20" - DATE "2020-04-19"`},
		{"datetime-sub", "TIMESTAMP - TIMESTAMP", `SELECT TIMESTAMP "2021-06-01 12:34:56.789" - TIMESTAMP "2021-05-31 00:00:00"`},
		{"datetime-sub", "DATETIME - DATETIME", `SELECT DATETIME "2021-06-01 12:00:00" - DATETIME "2021-05-31 00:00:00"`},

		// ============================================================
		// 8. Interval arithmetic operators
		// ============================================================
		{"interval-arith", "DATE + INTERVAL HOUR", `SELECT DATE "2021-04-20" + INTERVAL 25 HOUR`},
		{"interval-arith", "TIMESTAMP - INTERVAL SECOND", `SELECT TIMESTAMP "2021-05-02 00:01:02.345+00" - INTERVAL 10 SECOND`},
		{"interval-arith", "INTERVAL * INT", `SELECT INTERVAL '1:2:3' HOUR TO SECOND * 10`},
		{"interval-arith", "INTERVAL / INT", `SELECT INTERVAL 10 YEAR / 3`},

		// ============================================================
		// 9. Bitwise operators
		// ============================================================
		{"bitwise", "~", `SELECT ~1`},
		{"bitwise", "&", `SELECT 5 & 3`},
		{"bitwise", "|", `SELECT 5 | 3`},
		{"bitwise", "^", `SELECT 5 ^ 3`},
		{"bitwise", "<<", `SELECT 1 << 2`},
		{"bitwise", ">>", `SELECT 8 >> 1`},
		{"bitwise", "<< negative shift (expect error)", `SELECT 1 << -1`},

		// ============================================================
		// 10. Logical operators
		// ============================================================
		{"logical", "TRUE AND FALSE", `SELECT TRUE AND FALSE`},
		{"logical", "TRUE OR FALSE", `SELECT TRUE OR FALSE`},
		{"logical", "NOT TRUE", `SELECT NOT TRUE`},
		{"logical", "NULL AND TRUE", `SELECT NULL AND TRUE`},
		{"logical", "NULL OR FALSE", `SELECT NULL OR FALSE`},
		{"logical", "NULL AND FALSE -> FALSE", `SELECT NULL AND FALSE`},
		{"logical", "NULL OR TRUE -> TRUE", `SELECT NULL OR TRUE`},

		// ============================================================
		// 11. Comparison operators
		// ============================================================
		{"comparison", "=", `SELECT 1 = 1`},
		{"comparison", "<", `SELECT 1 < 2`},
		{"comparison", ">", `SELECT 1 > 2`},
		{"comparison", "<=", `SELECT 1 <= 1`},
		{"comparison", ">=", `SELECT 1 >= 1`},
		{"comparison", "!=", `SELECT 1 != 2`},
		{"comparison", "<>", `SELECT 1 <> 2`},
		{"comparison", "BETWEEN", `SELECT 1 BETWEEN 0 AND 2`},
		{"comparison", "NOT BETWEEN", `SELECT 1 NOT BETWEEN 3 AND 5`},
		{"comparison", "NaN = NaN -> FALSE", `SELECT CAST('NaN' AS FLOAT64) = CAST('NaN' AS FLOAT64)`},
		{"comparison", "NaN != NaN -> TRUE", `SELECT CAST('NaN' AS FLOAT64) != CAST('NaN' AS FLOAT64)`},
		{"comparison", "STRUCT(1,NULL)=STRUCT(1,NULL) -> NULL", `SELECT STRUCT(1, CAST(NULL AS INT64)) = STRUCT(1, CAST(NULL AS INT64))`},
		{"comparison", "STRUCT(1,NULL)=STRUCT(2,NULL) -> FALSE", `SELECT STRUCT(1, CAST(NULL AS INT64)) = STRUCT(2, CAST(NULL AS INT64))`},

		// ============================================================
		// 12. EXISTS operator
		// ============================================================
		{"exists", "non-empty -> TRUE", `SELECT EXISTS(SELECT 1)`},
		{"exists", "empty -> FALSE", `SELECT EXISTS(SELECT 1 WHERE FALSE)`},
		{"exists", "doc-style WITH", `WITH Words AS (SELECT 'Intend' AS value, 'east' AS direction UNION ALL SELECT 'Secure', 'north' UNION ALL SELECT 'Clarity', 'west') SELECT EXISTS(SELECT value FROM Words WHERE direction = 'south')`},

		// ============================================================
		// 13. IN operator
		// ============================================================
		{"in", "literal list TRUE", `SELECT 'a' IN ('a', 'b')`},
		{"in", "literal list FALSE", `SELECT 'z' IN ('a', 'b')`},
		{"in", "NOT IN", `SELECT 'z' NOT IN ('a', 'b')`},
		{"in", "subquery", `SELECT 1 IN (SELECT 1 UNION ALL SELECT 2)`},
		{"in", "UNNEST simple", `SELECT 1 IN UNNEST([1, 2, 3])`},
		{"in", "UNNEST with NULL match", `SELECT 1 IN UNNEST([CAST(NULL AS INT64), 1])`},
		{"in", "UNNEST not-found w/ NULL -> NULL", `SELECT 1 IN UNNEST([CAST(NULL AS INT64), 2])`},
		{"in", "struct-key", `SELECT (1, 'a') IN ((1, 'a'), (2, 'b'))`},

		// ============================================================
		// 14. IS operators
		// ============================================================
		{"is-op", "NULL IS NULL -> TRUE", `SELECT NULL IS NULL`},
		{"is-op", "1 IS NOT NULL -> TRUE", `SELECT 1 IS NOT NULL`},
		{"is-op", "TRUE IS TRUE", `SELECT TRUE IS TRUE`},
		{"is-op", "FALSE IS NOT TRUE -> TRUE", `SELECT FALSE IS NOT TRUE`},
		{"is-op", "FALSE IS FALSE -> TRUE", `SELECT FALSE IS FALSE`},
		{"is-op", "NULL IS UNKNOWN -> TRUE", `SELECT NULL IS UNKNOWN`},
		{"is-op", "TRUE IS NOT UNKNOWN -> TRUE", `SELECT TRUE IS NOT UNKNOWN`},

		// ============================================================
		// 15. IS DISTINCT FROM operator
		// ============================================================
		{"is-distinct", "1 IS DISTINCT FROM 2 -> TRUE", `SELECT 1 IS DISTINCT FROM 2`},
		{"is-distinct", "1 IS DISTINCT FROM NULL -> TRUE", `SELECT 1 IS DISTINCT FROM NULL`},
		{"is-distinct", "NULL IS DISTINCT FROM NULL -> FALSE", `SELECT NULL IS DISTINCT FROM NULL`},
		{"is-distinct", "1 IS NOT DISTINCT FROM 1 -> TRUE", `SELECT 1 IS NOT DISTINCT FROM 1`},
		{"is-distinct", "NULL IS NOT DISTINCT FROM NULL -> TRUE", `SELECT NULL IS NOT DISTINCT FROM NULL`},

		// ============================================================
		// 16. LIKE operator
		// ============================================================
		{"like", "apple LIKE a% -> TRUE", `SELECT 'apple' LIKE 'a%'`},
		{"like", "%a LIKE apple -> FALSE", `SELECT '%a' LIKE 'apple'`},
		{"like", "apple NOT LIKE a% -> FALSE", `SELECT 'apple' NOT LIKE 'a%'`},
		{"like", "underscore _pple -> TRUE", `SELECT 'apple' LIKE '_pple'`},
		{"like", "NULL LIKE a% (doc says error)", `SELECT NULL LIKE 'a%'`},
		{"like", "apple LIKE NULL (doc says error)", `SELECT 'apple' LIKE CAST(NULL AS STRING)`},
		{"like", "bytes b'a%' LIKE b'a__'", `SELECT b'abc' LIKE b'a__'`},

		// ============================================================
		// 17. Quantified LIKE operator
		// ============================================================
		{"q-like", "LIKE ANY (a%, b%) -> TRUE", `SELECT 'apple' LIKE ANY ('a%', 'b%')`},
		{"q-like", "LIKE SOME (synonym)", `SELECT 'apple' LIKE SOME ('a%', 'b%')`},
		{"q-like", "LIKE ALL (a%, %le) -> TRUE", `SELECT 'apple' LIKE ALL ('a%', '%le')`},
		{"q-like", "NOT LIKE ANY -> TRUE", `SELECT 'apple' NOT LIKE ANY ('a%', 'b%')`},
		{"q-like", "LIKE ANY UNNEST", `SELECT 'apple' LIKE ANY UNNEST(['%pp%', '%xx%'])`},
		{"q-like", "NULL LIKE ANY (a, b) -> NULL", `SELECT CAST(NULL AS STRING) LIKE ANY ('a', 'b')`},
		{"q-like", "a LIKE ANY (a, NULL) -> TRUE", `SELECT 'a' LIKE ANY ('a', CAST(NULL AS STRING))`},
		{"q-like", "a LIKE ANY (b, NULL) -> NULL", `SELECT 'a' LIKE ANY ('b', CAST(NULL AS STRING))`},
		{"q-like", "a LIKE ALL (a, NULL) -> NULL", `SELECT 'a' LIKE ALL ('a', CAST(NULL AS STRING))`},
		{"q-like", "a LIKE ALL (b, NULL) -> FALSE", `SELECT 'a' LIKE ALL ('b', CAST(NULL AS STRING))`},

		// ============================================================
		// 18. WITH expression
		// ============================================================
		{"with-expr", "doc canonical (123+456+789)", `SELECT WITH(a AS '123', b AS CONCAT(a, '456'), c AS '789', CONCAT(b, c))`},
		{"with-expr", "RAND() evaluated once -> 0.0", `SELECT WITH(a AS RAND(), a - a)`},
		{"with-expr", "aggregate result stored", `SELECT WITH(s AS SUM(input), c AS COUNT(input), s/c) FROM UNNEST([1.0, 2.0, 3.0]) AS input`},

		// ============================================================
		// 19. Graph operators (expected: emulator does NOT support)
		// ============================================================
		{"graph", "GRAPH MATCH (expect reject)", `GRAPH graph_db.FinGraph MATCH (a:Account)-[t:Transfers]->(b:Account) RETURN a.id`},
	}

	for _, c := range cases {
		c := c
		t.Run(fmt.Sprintf("%s/%s", c.group, c.name), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Logf("[PANIC] %s -> %v", c.sql, r)
				}
			}()
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
