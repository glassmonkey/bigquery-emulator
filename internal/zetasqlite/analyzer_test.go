package zetasqlite

import (
	"context"
	"sync"
	"testing"

	"github.com/glassmonkey/zetasql-wasm"
)

// sharedEngine is the WASM-backed analyzer engine. Spinning up zetasql
// in WASM costs ~5 seconds, so we lazily build one engine for the
// whole test binary; per-case fixtures (AnalyzerOptions, Catalog) are
// still rebuilt for every subtest so no test-visible mutable state is
// shared between cases.
var (
	sharedEngine     *zetasql.Engine
	sharedEngineOnce sync.Once
	sharedEngineErr  error
)

func getSharedEngine(t *testing.T) *zetasql.Engine {
	t.Helper()
	sharedEngineOnce.Do(func() {
		sharedEngine, sharedEngineErr = zetasql.New(context.Background())
	})
	if sharedEngineErr != nil {
		t.Fatalf("init zetasql engine: %v", sharedEngineErr)
	}
	return sharedEngine
}

// TestRejectInvalidLiteralCast pins the gate's contract: which shapes
// trigger an analyzer-side rejection and which shapes pass through to
// runtime. The end-to-end TestQuery cases only exercise three points;
// this table is the rest of the contract.
//
// The gate is exercised directly rather than through Analyzer.Analyze
// (the production wire-up) because we want to pin the helper's pure
// contract — input AST + SQL → error — independently of catalog sync,
// parseScript, and statement-loop concerns. The production wire-up is
// covered by TestQuery/invalid_cast and friends.
func TestRejectInvalidLiteralCast(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantErr string // empty means no rejection expected
	}{
		// --- reject cases ---
		{
			name:    "non-numeric string literal to INT64 is rejected",
			sql:     `SELECT CAST("apple" AS INT64)`,
			wantErr: `INVALID_ARGUMENT: Could not cast literal "apple" to type INT64 [at 1:13]`,
		},
		{
			name:    "string with partial numeric prefix is rejected",
			sql:     `SELECT CAST("12abc" AS INT64)`,
			wantErr: `INVALID_ARGUMENT: Could not cast literal "12abc" to type INT64 [at 1:13]`,
		},
		{
			name:    "decimal string is rejected (INT64 has no fractional form)",
			sql:     `SELECT CAST("1.5" AS INT64)`,
			wantErr: `INVALID_ARGUMENT: Could not cast literal "1.5" to type INT64 [at 1:13]`,
		},
		{
			name:    "INT64 overflow is rejected",
			sql:     `SELECT CAST("99999999999999999999" AS INT64)`,
			wantErr: `INVALID_ARGUMENT: Could not cast literal "99999999999999999999" to type INT64 [at 1:13]`,
		},
		{
			name:    "rejection reports line and column for multiline source",
			sql:     "SELECT\n  CAST(\"apple\" AS INT64)",
			wantErr: `INVALID_ARGUMENT: Could not cast literal "apple" to type INT64 [at 2:8]`,
		},
		{
			name:    "first invalid cast wins when multiple bad casts appear",
			sql:     `SELECT CAST("apple" AS INT64), CAST("banana" AS INT64)`,
			wantErr: `INVALID_ARGUMENT: Could not cast literal "apple" to type INT64 [at 1:13]`,
		},
		{
			name:    "valid cast preceding invalid cast does not mask the invalid one",
			sql:     `SELECT CAST("42" AS INT64), CAST("apple" AS INT64)`,
			wantErr: `INVALID_ARGUMENT: Could not cast literal "apple" to type INT64 [at 1:34]`,
		},
		{
			name:    "invalid cast inside a sub-expression is still gated",
			sql:     `SELECT 1 + CAST("apple" AS INT64)`,
			wantErr: `INVALID_ARGUMENT: Could not cast literal "apple" to type INT64 [at 1:17]`,
		},

		// --- accept cases (gate ran, parse succeeded, no rejection) ---
		{
			name: "valid integer string is accepted",
			sql:  `SELECT CAST("42" AS INT64)`,
		},
		{
			name: "hex string is accepted (matches runtime base-0 rule)",
			sql:  `SELECT CAST("0x87a" AS INT64)`,
		},
		{
			name: "negative int64 minimum is accepted",
			sql:  `SELECT CAST("-9223372036854775808" AS INT64)`,
		},
		{
			name: "empty string is accepted (StringValue.ToInt64 returns 0)",
			sql:  `SELECT CAST("" AS INT64)`,
		},

		// --- skip cases (gate not applicable, runtime decides) ---
		{
			name: "SAFE_CAST is skipped (ReturnNullOnError contract)",
			sql:  `SELECT SAFE_CAST("apple" AS INT64)`,
		},
		{
			name: "non-literal source is left for runtime",
			sql:  `WITH t AS (SELECT "apple" AS x) SELECT CAST(x AS INT64) FROM t`,
		},
		{
			name: "non-INT64 target is out of scope (FLOAT64 left for runtime)",
			sql:  `SELECT CAST("apple" AS FLOAT64)`,
		},
		{
			name: "non-STRING literal source is not gated",
			sql:  `SELECT CAST(5 AS INT64)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			ctx := t.Context()
			engine := getSharedEngine(t)
			opt, err := newAnalyzerOptions()
			if err != nil {
				t.Fatalf("newAnalyzerOptions: %v", err)
			}
			cat := NewCatalog(nil)
			out, err := engine.Analyze(ctx, tc.sql, cat.SimpleCatalog(), opt)
			if err != nil {
				t.Fatalf("engine.Analyze: %v", err)
			}

			// Act
			got := rejectInvalidLiteralCast(out.Resolved, tc.sql)

			// Assert
			if tc.wantErr == "" {
				if got != nil {
					t.Errorf("expected no rejection, got %q", got.Error())
				}
				return
			}
			if got == nil {
				t.Fatalf("expected rejection %q, got nil", tc.wantErr)
			}
			if got.Error() != tc.wantErr {
				t.Errorf("rejection mismatch\n want: %q\n  got: %q", tc.wantErr, got.Error())
			}
		})
	}
}

// TestByteOffsetToLineColumn pins the offset → 1-indexed (line, column)
// conversion used by the [at L:C] suffix on rejection messages.
// Out-of-range offsets clamp to the start of input rather than panicking.
func TestByteOffsetToLineColumn(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		offset   int
		wantLine int
		wantCol  int
	}{
		{
			name:     "start of single-line input",
			sql:      "SELECT 1",
			offset:   0,
			wantLine: 1,
			wantCol:  1,
		},
		{
			name:     "interior of single-line input",
			sql:      "SELECT 1",
			offset:   7,
			wantLine: 1,
			wantCol:  8,
		},
		{
			name:     "first byte of second line",
			sql:      "SELECT\nFROM t",
			offset:   7,
			wantLine: 2,
			wantCol:  1,
		},
		{
			name:     "interior of second line with leading indent",
			sql:      "SELECT\n  FROM t",
			offset:   9,
			wantLine: 2,
			wantCol:  3,
		},
		{
			name:     "byte position equal to input length is permitted",
			sql:      "abc",
			offset:   3,
			wantLine: 1,
			wantCol:  4,
		},
		{
			name:     "negative offset clamps to start",
			sql:      "abc",
			offset:   -1,
			wantLine: 1,
			wantCol:  1,
		},
		{
			name:     "offset past end clamps to start",
			sql:      "abc",
			offset:   100,
			wantLine: 1,
			wantCol:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			gotLine, gotCol := byteOffsetToLineColumn(tc.sql, tc.offset)

			// Assert
			if gotLine != tc.wantLine || gotCol != tc.wantCol {
				t.Errorf("byteOffsetToLineColumn(%q, %d) = (%d, %d), want (%d, %d)",
					tc.sql, tc.offset, gotLine, gotCol, tc.wantLine, tc.wantCol)
			}
		})
	}
}
