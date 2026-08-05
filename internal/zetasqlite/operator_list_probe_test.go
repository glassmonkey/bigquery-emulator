package zetasqlite

import (
	"context"
	"database/sql"
	"fmt"
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

		// ============================================================
		// 20. Operator precedence (doc precedence table)
		// ============================================================
		{"precedence", "* before + -> 7", `SELECT 1 + 2 * 3`},
		{"precedence", "paren overrides -> 9", `SELECT (1 + 2) * 3`},
		{"precedence", "unary - before + -> 1", `SELECT -2 + 3`},
		{"precedence", "arith before compare -> TRUE", `SELECT 2 + 3 = 5`},
		{"precedence", "& before | -> 3", `SELECT 1 | 2 & 3`},
		{"precedence", "^ before | -> 7", `SELECT 1 ^ 2 | 4`},
		{"precedence", "NOT before AND -> FALSE", `SELECT NOT FALSE AND FALSE`},
		{"precedence", "AND before OR -> TRUE", `SELECT TRUE OR FALSE AND FALSE`},

		// ============================================================
		// 21. Bitwise operators on BYTES (doc: &, |, ^, ~ on BYTES)
		// ============================================================
		{"bitwise-bytes", "BYTES &", `SELECT b'\x0f' & b'\x03'`},
		{"bitwise-bytes", "BYTES |", `SELECT b'\x01' | b'\x02'`},
		{"bitwise-bytes", "BYTES ^", `SELECT b'\xff' ^ b'\x0f'`},
		{"bitwise-bytes", "BYTES ~", `SELECT ~b'\x00'`},

		// ============================================================
		// 22. Comparison: NULL operand & non-INT types
		// ============================================================
		{"comparison-ext", "1 = NULL -> NULL", `SELECT 1 = CAST(NULL AS INT64)`},
		{"comparison-ext", "string < string -> TRUE", `SELECT 'a' < 'b'`},
		{"comparison-ext", "timestamp < timestamp -> TRUE", `SELECT TIMESTAMP "2021-01-01 00:00:00" < TIMESTAMP "2021-01-02 00:00:00"`},
		{"comparison-ext", "bytes = bytes -> TRUE", `SELECT b'abc' = b'abc'`},
	}

	// truth records production BigQuery behavior for the cases we have
	// confirmed against the docs. Keyed by the exact SQL text; cases absent
	// here stay in pure observation mode (bqUnknown). The MISMATCH entries are
	// the payload of this probe: they name concrete gaps vs. real BigQuery.
	truth := map[string]groundTruth{
		// Agreement pins (guard against regressing correct behavior).
		`SELECT 1 + 2`:             {bqAccept, "[3]"},
		`SELECT TRUE AND FALSE`:    {bqAccept, "[false]"},
		`SELECT 1 = 1`:             {bqAccept, "[true]"},
		`SELECT NULL IS NULL`:      {bqAccept, "[true]"},
		`SELECT 'apple' LIKE 'a%'`: {bqAccept, "[true]"},
		// BQ rejects a literal NULL LIKE operand at analysis time.
		`SELECT NULL LIKE 'a%'`: {bqReject, ""},
		// --- surfaced bugs ---
		// `_` matches exactly one char, so 'apple' LIKE '_pple' is TRUE in BQ;
		// the emulator returns false (silent wrong result).
		`SELECT 'apple' LIKE '_pple'`: {bqAccept, "[true]"},
		// A non-literal NULL pattern makes LIKE return NULL in BQ (three-valued
		// logic); the emulator returns false.
		`SELECT 'apple' LIKE CAST(NULL AS STRING)`: {bqAccept, "[<nil>]"},
		// Quantified LIKE is a documented BQ operator; the emulator rejects it.
		`SELECT 'apple' LIKE ANY ('a%', 'b%')`: {bqAccept, "[true]"},
		// precedence: pinned to the value BQ's precedence table produces.
		`SELECT 1 + 2 * 3`:               {bqAccept, "[7]"},
		`SELECT (1 + 2) * 3`:             {bqAccept, "[9]"},
		`SELECT -2 + 3`:                  {bqAccept, "[1]"},
		`SELECT 2 + 3 = 5`:               {bqAccept, "[true]"},
		`SELECT 1 | 2 & 3`:               {bqAccept, "[3]"}, // & tighter than |
		`SELECT 1 ^ 2 | 4`:               {bqAccept, "[7]"}, // ^ tighter than |
		`SELECT NOT FALSE AND FALSE`:     {bqAccept, "[false]"},
		`SELECT TRUE OR FALSE AND FALSE`: {bqAccept, "[true]"},
		// bytes bitwise: BQ accepts; value not pinned (base64 render).
		`SELECT b'\x0f' & b'\x03'`: {bqAccept, ""},
		`SELECT b'\x01' | b'\x02'`: {bqAccept, ""},
		`SELECT b'\xff' ^ b'\x0f'`: {bqAccept, ""},
		`SELECT ~b'\x00'`:          {bqAccept, ""},
		// comparison across NULL / non-INT types.
		`SELECT 1 = CAST(NULL AS INT64)`: {bqAccept, "[<nil>]"}, // NULL comparison -> NULL
		`SELECT 'a' < 'b'`:               {bqAccept, "[true]"},
		`SELECT TIMESTAMP "2021-01-01 00:00:00" < TIMESTAMP "2021-01-02 00:00:00"`: {bqAccept, "[true]"},
		`SELECT b'abc' = b'abc'`: {bqAccept, "[true]"},
	}

	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%s", c.group, c.name), func(t *testing.T) {
			runProbe(t, ctx, db, c.sql, truth[c.sql])
		})
	}
}
