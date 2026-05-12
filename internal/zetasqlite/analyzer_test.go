package zetasqlite

import (
	"context"
	"testing"
)

// TestRejectInvalidLiteralCast pins the gate's contract: which shapes
// trigger an analyzer-side rejection and which shapes pass through
// to runtime. Each case states the intent in its name so a reader can
// read the table as the gate's spec.
func TestRejectInvalidLiteralCast(t *testing.T) {
	ctx := context.Background()
	cat := NewCatalog(nil)
	a, err := NewAnalyzer(ctx, cat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(ctx) })

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

		// --- accept cases (same gate ran, parse succeeded, no rejection) ---
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			out, err := a.engine.Analyze(ctx, tc.sql, cat.SimpleCatalog(), a.opt)
			if err != nil {
				t.Fatalf("engine.Analyze failed: %v", err)
			}
			got := rejectInvalidLiteralCast(out.Resolved, tc.sql)
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotLine, gotCol := byteOffsetToLineColumn(tc.sql, tc.offset)
			if gotLine != tc.wantLine || gotCol != tc.wantCol {
				t.Errorf("byteOffsetToLineColumn(%q, %d) = (%d, %d), want (%d, %d)",
					tc.sql, tc.offset, gotLine, gotCol, tc.wantLine, tc.wantCol)
			}
		})
	}
}
