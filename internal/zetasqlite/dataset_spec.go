package zetasqlite

import (
	"fmt"
	"strconv"
	"strings"

	parsed "github.com/glassmonkey/zetasql-wasm/ast"
	resolved "github.com/glassmonkey/zetasql-wasm/resolved_ast"
)

// DatasetSpec is the schema-side counterpart of TableSpec /
// FunctionSpec. CREATE SCHEMA writes one of these via
// ChangedCatalog.Dataset.Added; DROP SCHEMA writes via Deleted. The
// server's syncCatalog reflects them into the metaRepo.
type DatasetSpec struct {
	NamePath   []string
	CreateMode resolved.CreateMode

	// KnownOptions holds OPTIONS recognised as standard BigQuery
	// CREATE SCHEMA OPTIONS whose value the emulator persists into
	// the dataset content. Value types: string, int64, float64,
	// bool, map[string]string for labels.
	KnownOptions map[string]any

	// UnknownOptions lists OPTIONS the emulator received but does
	// not persist. The server's syncCatalog emits a WARN log per
	// entry so silent-ignore failures are surfaced (= the user can
	// tell that the OPTION was accepted but not reflected). This is
	// the boundary between "completeness" (accept anything BQ
	// accepts) and "soundness" (don't pretend to persist things we
	// can't): unknowns are accepted on the wire but logged.
	UnknownOptions []string

	// IsIfExists is set for DROP SCHEMA IF EXISTS.
	IsIfExists bool
}

func (s *DatasetSpec) DatasetID() string {
	if len(s.NamePath) == 0 {
		return ""
	}
	return s.NamePath[len(s.NamePath)-1]
}

func (s *DatasetSpec) ProjectID() string {
	if len(s.NamePath) < 2 {
		return ""
	}
	return s.NamePath[0]
}

// IsIfNotExists returns true when the source CREATE SCHEMA was written
// with IF NOT EXISTS; the server uses this to suppress the "dataset
// already created" error from Project.AddDataset.
func (s *DatasetSpec) IsIfNotExists() bool {
	return s.CreateMode == resolved.CreateIfNotExistsMode
}

// IsOrReplace returns true when the source CREATE SCHEMA was written
// with OR REPLACE.
func (s *DatasetSpec) IsOrReplace() bool {
	return s.CreateMode == resolved.CreateOrReplaceMode
}

// knownCreateSchemaOptions enumerates BigQuery CREATE SCHEMA OPTIONS
// whose value maps to a field on bigqueryv2.Dataset and can therefore
// be read back through the REST API. OPTIONS outside this set are
// accepted but accumulated into DatasetSpec.UnknownOptions for
// warn-logging on the server side. See:
// https://cloud.google.com/bigquery/docs/reference/standard-sql/data-definition-language#schema_option_list
var knownCreateSchemaOptions = map[string]struct{}{
	"description":                      {},
	"friendly_name":                    {},
	"labels":                           {},
	"location":                         {},
	"default_table_expiration_days":    {},
	"default_partition_expiration_days": {},
	"max_time_travel_hours":            {},
	"storage_billing_model":            {},
	"is_case_insensitive":              {},
	"default_collation":                {},
	"default_rounding_mode":            {},
}

// decodeCreateSchemaOptions evaluates parsed-AST OPTIONS into Go
// values. The resolved-AST side of CreateSchemaStmtNode currently
// exposes OPTIONS only as raw proto, so we pair the resolved node
// with the parsed node (which gives us OptionsListNode) at dispatch
// time. See analyzer.go newCreateSchemaStmtAction.
//
// Returns the (lower-cased) name → value map for OPTIONS the
// emulator persists, plus the list of OPTIONS names the emulator
// received but does not persist (= silently dropped without this
// signal, so the server emits a WARN log).
func decodeCreateSchemaOptions(opts *parsed.OptionsListNode) (map[string]any, []string, error) {
	if opts == nil {
		return nil, nil, nil
	}
	known := map[string]any{}
	var unknown []string
	for _, entry := range opts.OptionsEntries() {
		ident := entry.Name()
		if ident == nil {
			continue
		}
		name := strings.ToLower(ident.IdString())
		value, err := evalOptionLiteral(entry.Value())
		if err != nil {
			return nil, nil, fmt.Errorf("OPTIONS(%s): %w", name, err)
		}
		if _, ok := knownCreateSchemaOptions[name]; !ok {
			unknown = append(unknown, name)
			continue
		}
		known[name] = value
	}
	return known, unknown, nil
}

// evalOptionLiteral converts a parsed-AST expression appearing as an
// OPTIONS value into the corresponding Go value. Only the literal
// shapes BigQuery DDL OPTIONS use are covered; non-literal
// expressions (which BigQuery itself rejects in OPTIONS position)
// return an error so the caller can surface them rather than silently
// store something garbage.
func evalOptionLiteral(expr parsed.ExpressionNode) (any, error) {
	switch n := expr.(type) {
	case *parsed.StringLiteralNode:
		return n.StringValue(), nil
	case *parsed.IntLiteralNode:
		// IntLiteralNode.Image() returns the literal as written
		// ("42", "0xFF", "42L"). BigQuery DDL OPTIONS only allow
		// plain decimal int literals, but we accept what
		// strconv.ParseInt can handle.
		s := strings.TrimSuffix(n.Image(), "L")
		v, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid int literal %q: %w", n.Image(), err)
		}
		return v, nil
	case *parsed.FloatLiteralNode:
		v, err := strconv.ParseFloat(n.Image(), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid float literal %q: %w", n.Image(), err)
		}
		return v, nil
	case *parsed.BooleanLiteralNode:
		return n.Value(), nil
	case *parsed.ArrayConstructorNode:
		return evalOptionLabels(n)
	}
	return nil, fmt.Errorf("unsupported OPTIONS value of kind %s", expr.Kind())
}

// evalOptionLabels handles the `labels` OPTIONS shape, which BigQuery
// parses as ARRAY<STRUCT<STRING, STRING>>: `labels=[("k","v"), ...]`.
// Returns a map[string]string keyed by label name. The choice of map
// over slice means the OPTIONS-side iteration order is lost, but it
// matches bigqueryv2.Dataset.Labels exactly.
func evalOptionLabels(arr *parsed.ArrayConstructorNode) (map[string]string, error) {
	labels := map[string]string{}
	for _, elem := range arr.Elements() {
		key, value, err := evalLabelEntry(elem)
		if err != nil {
			return nil, fmt.Errorf("labels: %w", err)
		}
		labels[key] = value
	}
	return labels, nil
}

func evalLabelEntry(elem parsed.ExpressionNode) (string, string, error) {
	sc, ok := elem.(*parsed.StructConstructorWithParensNode)
	if !ok {
		return "", "", fmt.Errorf("expected STRUCT element, got %s", elem.Kind())
	}
	fields := sc.FieldExpressions()
	if len(fields) != 2 {
		return "", "", fmt.Errorf("expected 2 STRUCT fields, got %d", len(fields))
	}
	key, ok := fields[0].(*parsed.StringLiteralNode)
	if !ok {
		return "", "", fmt.Errorf("expected STRING key, got %s", fields[0].Kind())
	}
	value, ok := fields[1].(*parsed.StringLiteralNode)
	if !ok {
		return "", "", fmt.Errorf("expected STRING value, got %s", fields[1].Kind())
	}
	return key.StringValue(), value.StringValue(), nil
}
