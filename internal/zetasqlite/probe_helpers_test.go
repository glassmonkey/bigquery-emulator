package zetasqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// bqExpect encodes what production BigQuery does with a probe query, so the
// probes can be *self-labeling*: an observed emulator result that silently
// disagrees with BigQuery is reported as [MISMATCH] instead of hiding inside
// an [ACCEPT]/[REJECT] line. The zero value (bqUnknown) means "no ground truth
// recorded yet" and keeps a case in pure observation mode.
type bqExpect int

const (
	bqUnknown bqExpect = iota // no documented BQ behavior yet -> observe only
	bqAccept                  // BQ analyzes+runs the query (value pinned in groundTruth.want if known)
	bqReject                  // BQ rejects the query (analyzer or runtime error)
)

// groundTruth is the documented BigQuery behavior for a single probe query.
// want is compared only when non-empty; a bqAccept with an empty want asserts
// "BQ accepts" without pinning the exact value (e.g. issue #98, where BQ's
// dry-run validates but the rendered string is not recorded).
type groundTruth struct {
	bq   bqExpect
	want string
}

// runProbe executes sqlText and logs a single self-labeling outcome. It is
// observation-only and never fails the test (t.Fatal/t.Error are not used):
// the probes exist to surface bugs, not to gate CI. When gt carries a
// documented BigQuery expectation it emits [OK]/[OK-REJECT] on agreement and a
// loud [MISMATCH] on disagreement; with gt == (bqUnknown) it falls back to the
// original [ACCEPT]/[REJECT] observation labels.
//
// The recover() guard means a panic in the emulator is logged as [PANIC] for
// this one case instead of aborting the whole probe run.
func runProbe(t *testing.T, ctx context.Context, db *sql.DB, sqlText string, gt groundTruth) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Logf("[PANIC] %s -> %v", sqlText, r)
		}
	}()

	rows, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		switch {
		case gt.bq == bqReject:
			t.Logf("[OK-REJECT] %s -> %s", sqlText, oneLine(err.Error()))
		case gt.bq == bqAccept:
			t.Logf("[MISMATCH] %s -> got REJECT, want ACCEPT%s | %s", sqlText, wantSuffix(gt), oneLine(err.Error()))
		default:
			t.Logf("[REJECT] %s -> %s", sqlText, oneLine(err.Error()))
		}
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
			logRunError(t, sqlText, "SCAN-ERR", err, gt)
			return
		}
		outs = append(outs, fmt.Sprintf("%v", vals))
	}
	if err := rows.Err(); err != nil {
		logRunError(t, sqlText, "ROWS-ERR", err, gt)
		return
	}
	got := strings.Join(outs, " | ")

	switch {
	case gt.bq == bqReject:
		t.Logf("[MISMATCH] %s -> got %s, want REJECT", sqlText, got)
	case gt.bq == bqAccept && gt.want != "" && got != gt.want:
		t.Logf("[MISMATCH] %s -> got %s, want %s", sqlText, got, gt.want)
	case gt.bq == bqAccept:
		t.Logf("[OK] %s -> %s", sqlText, got)
	default:
		t.Logf("[ACCEPT] %s -> %s", sqlText, got)
	}
}

// logRunError reports an error raised after the query was accepted and started
// returning rows (a scan/rows decode failure). When BigQuery is known to accept
// and return a value, decoding failure is a disagreement -> [MISMATCH]; else it
// is logged under its raw kind for observation.
func logRunError(t *testing.T, sqlText, kind string, err error, gt groundTruth) {
	t.Helper()
	if gt.bq == bqAccept {
		t.Logf("[MISMATCH] %s -> got %s, want ACCEPT%s | %s", sqlText, kind, wantSuffix(gt), oneLine(err.Error()))
		return
	}
	t.Logf("[%s] %s -> %s", kind, sqlText, oneLine(err.Error()))
}

// wantSuffix renders the pinned expected value for a [MISMATCH] on a rejected
// query, or "" when the ground truth only records "BQ accepts".
func wantSuffix(gt groundTruth) string {
	if gt.want == "" {
		return ""
	}
	return " (" + gt.want + ")"
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	return s
}
