package zetasqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/glassmonkey/zetasql-wasm"
	parsed "github.com/glassmonkey/zetasql-wasm/ast"
	"github.com/google/go-cmp/cmp"
)

// sharedEngine carries the WASM-backed parser/analyzer engine reused
// across every test in this file. Spinning the engine up costs
// multiple seconds (WASM compile + global ctor init) and the engine
// itself holds no per-call state we care about, so amortising the
// init across cases keeps the test suite fast. The mutex guards
// Parse, which writes into shared WASM linear memory and is not safe
// to invoke from two goroutines at once.
var (
	sharedEngineOnce sync.Once
	sharedEngine     *zetasql.Engine
	sharedEngineMu   sync.Mutex
)

func parseSchemaOptionsList(t *testing.T, sql string) *parsed.OptionsListNode {
	t.Helper()
	sharedEngineOnce.Do(func() {
		e, err := zetasql.New(context.Background())
		if err != nil {
			t.Fatalf("engine init: %v", err)
		}
		sharedEngine = e
	})
	sharedEngineMu.Lock()
	defer sharedEngineMu.Unlock()
	stmt, err := sharedEngine.Parse(context.Background(), sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	root, ok := stmt.Root.(*parsed.CreateSchemaStatementNode)
	if !ok {
		t.Fatalf("expected CreateSchemaStatementNode, got %T", stmt.Root)
	}
	return root.OptionsList()
}

// decodeResult packs the multi-value return of
// decodeCreateSchemaOptions into one comparable value so each table
// case has a single Assert step (R3 / Assertion Roulette).
type decodeResult struct {
	Opts    DatasetOptions
	Unknown []string
}

func TestDecodeCreateSchemaOptions(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want decodeResult
	}{
		{
			name: "empty OPTIONS",
			sql:  "CREATE SCHEMA ds OPTIONS()",
			want: decodeResult{},
		},
		{
			name: "string options",
			sql: "CREATE SCHEMA ds OPTIONS(" +
				"description='d', " +
				"friendly_name='fn', " +
				"location='US', " +
				"storage_billing_model='LOGICAL', " +
				"default_collation='und:ci', " +
				"default_rounding_mode='ROUND_HALF_EVEN'" +
				")",
			want: decodeResult{
				Opts: DatasetOptions{
					Description:         "d",
					FriendlyName:        "fn",
					Location:            "US",
					StorageBillingModel: "LOGICAL",
					DefaultCollation:    "und:ci",
					DefaultRoundingMode: "ROUND_HALF_EVEN",
				},
			},
		},
		{
			name: "int options (days)",
			sql: "CREATE SCHEMA ds OPTIONS(" +
				"default_table_expiration_days=7, " +
				"default_partition_expiration_days=30" +
				")",
			want: decodeResult{
				Opts: DatasetOptions{
					DefaultTableExpirationDays:     7,
					DefaultPartitionExpirationDays: 30,
				},
			},
		},
		{
			name: "max_time_travel_hours accepts FLOAT literal",
			sql:  "CREATE SCHEMA ds OPTIONS(max_time_travel_hours=168.5)",
			want: decodeResult{Opts: DatasetOptions{MaxTimeTravelHours: 168.5}},
		},
		{
			name: "max_time_travel_hours accepts INT literal (promoted to float)",
			sql:  "CREATE SCHEMA ds OPTIONS(max_time_travel_hours=168)",
			want: decodeResult{Opts: DatasetOptions{MaxTimeTravelHours: 168}},
		},
		{
			name: "bool option",
			sql:  "CREATE SCHEMA ds OPTIONS(is_case_insensitive=true)",
			want: decodeResult{Opts: DatasetOptions{IsCaseInsensitive: true}},
		},
		{
			name: "labels",
			sql:  `CREATE SCHEMA ds OPTIONS(labels=[("env","dev"),("team","data")])`,
			want: decodeResult{Opts: DatasetOptions{Labels: map[string]string{"env": "dev", "team": "data"}}},
		},
		{
			name: "unknown options are accepted and collected",
			sql:  "CREATE SCHEMA ds OPTIONS(default_kms_key_name='k', failover_reservation='r')",
			want: decodeResult{Unknown: []string{"default_kms_key_name", "failover_reservation"}},
		},
		{
			name: "OPTIONS names are lower-cased before lookup",
			sql:  "CREATE SCHEMA ds OPTIONS(DESCRIPTION='upper')",
			want: decodeResult{Opts: DatasetOptions{Description: "upper"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := parseSchemaOptionsList(t, tt.sql)
			gotOpts, gotUnknown, err := decodeCreateSchemaOptions(opts)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := decodeResult{Opts: gotOpts, Unknown: gotUnknown}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("decode result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeCreateSchemaOptions_TypeMismatchErrors(t *testing.T) {
	// Each case feeds a literal of the wrong shape for the named
	// OPTION. The decoder must return an error wrapping
	// ErrOptionTypeMismatch (= typed witness for "we recognised the
	// option name but rejected the literal kind"), and the error
	// message must mention the option name so the caller can
	// locate which line broke.
	tests := []struct {
		name       string
		sql        string
		optionName string
	}{
		{
			name:       "string option fed an INT literal",
			sql:        "CREATE SCHEMA ds OPTIONS(description=42)",
			optionName: "description",
		},
		{
			name:       "int option fed a STRING literal",
			sql:        "CREATE SCHEMA ds OPTIONS(default_table_expiration_days='7')",
			optionName: "default_table_expiration_days",
		},
		{
			name:       "bool option fed an INT literal",
			sql:        "CREATE SCHEMA ds OPTIONS(is_case_insensitive=1)",
			optionName: "is_case_insensitive",
		},
		{
			name:       "labels fed a STRING literal",
			sql:        "CREATE SCHEMA ds OPTIONS(labels='not-an-array')",
			optionName: "labels",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := parseSchemaOptionsList(t, tt.sql)
			_, _, err := decodeCreateSchemaOptions(opts)
			if !errors.Is(err, ErrOptionTypeMismatch) {
				t.Fatalf("error %v does not wrap ErrOptionTypeMismatch", err)
			}
			if !strings.Contains(err.Error(), tt.optionName) {
				t.Errorf("error %q does not mention option name %q", err, tt.optionName)
			}
		})
	}
}

func TestDecodeCreateSchemaOptions_NilOptionsList(t *testing.T) {
	// CREATE SCHEMA without an OPTIONS clause parses to a nil
	// OptionsList; decode must accept it and return a zero value
	// (not a nil-deref panic).
	gotOpts, gotUnknown, err := decodeCreateSchemaOptions(nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	got := decodeResult{Opts: gotOpts, Unknown: gotUnknown}
	if diff := cmp.Diff(decodeResult{}, got); diff != "" {
		t.Errorf("decode result (-want +got):\n%s", diff)
	}
}
