package zetasqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/glassmonkey/zetasql-wasm"
	parsed "github.com/glassmonkey/zetasql-wasm/ast"
	"github.com/google/go-cmp/cmp"
)

// parseSchemaOptionsList drives the parser to extract the OptionsList
// node from a CREATE SCHEMA SQL fragment. Kept inline here (not a
// shared helper) so the test reads top-to-bottom: each subtest names
// the SQL, this drives it through the parser, then asserts on the
// decoder output.
func parseSchemaOptionsList(t *testing.T, sql string) *parsed.OptionsListNode {
	t.Helper()
	engine, err := zetasql.New(context.Background())
	if err != nil {
		t.Fatalf("engine init: %v", err)
	}
	stmt, err := engine.Parse(context.Background(), sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	root, ok := stmt.Root.(*parsed.CreateSchemaStatementNode)
	if !ok {
		t.Fatalf("expected CreateSchemaStatementNode, got %T", stmt.Root)
	}
	return root.OptionsList()
}

func TestDecodeCreateSchemaOptions(t *testing.T) {
	tests := []struct {
		name        string
		sql         string
		wantOpts    DatasetOptions
		wantUnknown []string
	}{
		{
			name: "empty OPTIONS",
			sql:  "CREATE SCHEMA ds OPTIONS()",
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
			wantOpts: DatasetOptions{
				Description:         "d",
				FriendlyName:        "fn",
				Location:            "US",
				StorageBillingModel: "LOGICAL",
				DefaultCollation:    "und:ci",
				DefaultRoundingMode: "ROUND_HALF_EVEN",
			},
		},
		{
			name: "int options (days)",
			sql: "CREATE SCHEMA ds OPTIONS(" +
				"default_table_expiration_days=7, " +
				"default_partition_expiration_days=30" +
				")",
			wantOpts: DatasetOptions{
				DefaultTableExpirationDays:     7,
				DefaultPartitionExpirationDays: 30,
			},
		},
		{
			name: "max_time_travel_hours accepts FLOAT literal",
			sql:  "CREATE SCHEMA ds OPTIONS(max_time_travel_hours=168.5)",
			wantOpts: DatasetOptions{
				MaxTimeTravelHours: 168.5,
			},
		},
		{
			name: "max_time_travel_hours accepts INT literal (promoted to float)",
			sql:  "CREATE SCHEMA ds OPTIONS(max_time_travel_hours=168)",
			wantOpts: DatasetOptions{
				MaxTimeTravelHours: 168,
			},
		},
		{
			name: "bool option",
			sql:  "CREATE SCHEMA ds OPTIONS(is_case_insensitive=true)",
			wantOpts: DatasetOptions{
				IsCaseInsensitive: true,
			},
		},
		{
			name: "labels",
			sql:  `CREATE SCHEMA ds OPTIONS(labels=[("env","dev"),("team","data")])`,
			wantOpts: DatasetOptions{
				Labels: map[string]string{"env": "dev", "team": "data"},
			},
		},
		{
			name:        "unknown options are accepted and collected",
			sql:         "CREATE SCHEMA ds OPTIONS(default_kms_key_name='k', failover_reservation='r')",
			wantUnknown: []string{"default_kms_key_name", "failover_reservation"},
		},
		{
			name: "OPTIONS names are lower-cased before lookup",
			sql:  "CREATE SCHEMA ds OPTIONS(DESCRIPTION='upper')",
			wantOpts: DatasetOptions{
				Description: "upper",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := parseSchemaOptionsList(t, tt.sql)
			gotOpts, gotUnknown, err := decodeCreateSchemaOptions(opts)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if diff := cmp.Diff(tt.wantOpts, gotOpts); diff != "" {
				t.Errorf("Options (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantUnknown, gotUnknown); diff != "" {
				t.Errorf("Unknown (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeCreateSchemaOptions_TypeMismatchErrors(t *testing.T) {
	// Each case feeds a literal of the wrong shape for the named
	// OPTION; decode must surface a typed error rather than silently
	// coerce or zero-init. The wantErrSubstr check pins both the
	// OPTIONS name (so the user can locate the bad line) and the
	// expected-kind hint.
	tests := []struct {
		name          string
		sql           string
		wantErrSubstr string
	}{
		{
			name:          "string option fed an INT literal",
			sql:           "CREATE SCHEMA ds OPTIONS(description=42)",
			wantErrSubstr: "OPTIONS(description): expected STRING",
		},
		{
			name:          "int option fed a STRING literal",
			sql:           "CREATE SCHEMA ds OPTIONS(default_table_expiration_days='7')",
			wantErrSubstr: "OPTIONS(default_table_expiration_days): expected INT",
		},
		{
			name:          "bool option fed an INT literal",
			sql:           "CREATE SCHEMA ds OPTIONS(is_case_insensitive=1)",
			wantErrSubstr: "OPTIONS(is_case_insensitive): expected BOOL",
		},
		{
			name:          "labels fed a STRING literal",
			sql:           "CREATE SCHEMA ds OPTIONS(labels='not-an-array')",
			wantErrSubstr: "OPTIONS(labels): expected ARRAY",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := parseSchemaOptionsList(t, tt.sql)
			_, _, err := decodeCreateSchemaOptions(opts)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrSubstr)
			}
		})
	}
}

func TestDecodeCreateSchemaOptions_NilOptionsList(t *testing.T) {
	// CREATE SCHEMA without an OPTIONS clause parses to a nil
	// OptionsList; decode must accept it and return a zero value
	// (not a nil-deref panic).
	opts, unknown, err := decodeCreateSchemaOptions(nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if diff := cmp.Diff(DatasetOptions{}, opts); diff != "" {
		t.Errorf("Options (-want +got):\n%s", diff)
	}
	if unknown != nil {
		t.Errorf("Unknown: want nil, got %v", unknown)
	}
}
