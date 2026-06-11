package zetasqlite

import (
	"fmt"
	"strconv"
	"strings"

	parsed "github.com/glassmonkey/zetasql-wasm/ast"
)

// decodeCreateSchemaOptions evaluates parsed-AST OPTIONS into a
// DatasetOptions value plus the list of OPTIONS names the emulator
// received but does not persist.
//
// The resolved-AST side of CreateSchemaStmtNode exposes OPTIONS only
// as raw proto, so values are read from the parsed-AST side
// (OptionsListNode → OptionsEntryNode → typed literal nodes). See
// analyzer.go newCreateSchemaStmtAction for the wiring.
//
// The case list is the *authoritative* enumeration of OPTIONS the
// emulator persists; anything outside this switch lands in `unknown`
// so the server can warn-log it (= accept on the wire without
// pretending to persist). Keep this list aligned with the field copy
// in addDatasetMetadata on the server side — both encode the
// "what we persist" boundary.
func decodeCreateSchemaOptions(opts *parsed.OptionsListNode) (DatasetOptions, []string, error) {
	var out DatasetOptions
	if opts == nil {
		return out, nil, nil
	}
	var unknown []string
	for _, entry := range opts.OptionsEntries() {
		ident := entry.Name()
		if ident == nil {
			continue
		}
		name := strings.ToLower(ident.IdString())
		value := entry.Value()
		switch name {
		case "description":
			s, ok := value.(*parsed.StringLiteralNode)
			if !ok {
				return out, nil, fmt.Errorf("OPTIONS(%s): expected STRING, got %s", name, value.Kind())
			}
			out.Description = s.StringValue()
		case "friendly_name":
			s, ok := value.(*parsed.StringLiteralNode)
			if !ok {
				return out, nil, fmt.Errorf("OPTIONS(%s): expected STRING, got %s", name, value.Kind())
			}
			out.FriendlyName = s.StringValue()
		case "location":
			s, ok := value.(*parsed.StringLiteralNode)
			if !ok {
				return out, nil, fmt.Errorf("OPTIONS(%s): expected STRING, got %s", name, value.Kind())
			}
			out.Location = s.StringValue()
		case "storage_billing_model":
			s, ok := value.(*parsed.StringLiteralNode)
			if !ok {
				return out, nil, fmt.Errorf("OPTIONS(%s): expected STRING, got %s", name, value.Kind())
			}
			out.StorageBillingModel = s.StringValue()
		case "default_collation":
			s, ok := value.(*parsed.StringLiteralNode)
			if !ok {
				return out, nil, fmt.Errorf("OPTIONS(%s): expected STRING, got %s", name, value.Kind())
			}
			out.DefaultCollation = s.StringValue()
		case "default_rounding_mode":
			s, ok := value.(*parsed.StringLiteralNode)
			if !ok {
				return out, nil, fmt.Errorf("OPTIONS(%s): expected STRING, got %s", name, value.Kind())
			}
			out.DefaultRoundingMode = s.StringValue()
		case "default_table_expiration_days":
			n, err := parseIntLiteral(value)
			if err != nil {
				return out, nil, fmt.Errorf("OPTIONS(%s): %w", name, err)
			}
			out.DefaultTableExpirationDays = n
		case "default_partition_expiration_days":
			n, err := parseIntLiteral(value)
			if err != nil {
				return out, nil, fmt.Errorf("OPTIONS(%s): %w", name, err)
			}
			out.DefaultPartitionExpirationDays = n
		case "max_time_travel_hours":
			f, err := parseNumberLiteral(value)
			if err != nil {
				return out, nil, fmt.Errorf("OPTIONS(%s): %w", name, err)
			}
			out.MaxTimeTravelHours = f
		case "is_case_insensitive":
			b, ok := value.(*parsed.BooleanLiteralNode)
			if !ok {
				return out, nil, fmt.Errorf("OPTIONS(%s): expected BOOL, got %s", name, value.Kind())
			}
			out.IsCaseInsensitive = b.Value()
		case "labels":
			labels, err := evalLabelsOption(value)
			if err != nil {
				return out, nil, fmt.Errorf("OPTIONS(%s): %w", name, err)
			}
			out.Labels = labels
		default:
			unknown = append(unknown, name)
		}
	}
	return out, unknown, nil
}

// parseIntLiteral parses an IntLiteralNode. The lexer emits the
// literal as written (including a trailing "L" for long literals,
// which strconv.ParseInt does not accept), so the suffix is trimmed
// before parsing. Kept as a helper because the case "INT literal +
// trim L + ParseInt" is reused by parseNumberLiteral.
func parseIntLiteral(expr parsed.ExpressionNode) (int64, error) {
	n, ok := expr.(*parsed.IntLiteralNode)
	if !ok {
		return 0, fmt.Errorf("expected INT, got %s", expr.Kind())
	}
	s := strings.TrimSuffix(n.Image(), "L")
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid INT literal %q: %w", n.Image(), err)
	}
	return v, nil
}

// parseNumberLiteral accepts either a FLOAT or INT literal and
// returns float64. `max_time_travel_hours=168` is valid BigQuery DDL,
// so an INT in float position is promoted rather than rejected.
func parseNumberLiteral(expr parsed.ExpressionNode) (float64, error) {
	switch n := expr.(type) {
	case *parsed.FloatLiteralNode:
		v, err := strconv.ParseFloat(n.Image(), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid FLOAT literal %q: %w", n.Image(), err)
		}
		return v, nil
	case *parsed.IntLiteralNode:
		i, err := parseIntLiteral(n)
		if err != nil {
			return 0, err
		}
		return float64(i), nil
	}
	return 0, fmt.Errorf("expected number, got %s", expr.Kind())
}

// evalLabelsOption decodes the `labels` OPTIONS shape, which BigQuery
// parses as ARRAY<STRUCT<STRING, STRING>>: `labels=[("k","v"), ...]`.
// Returns a map[string]string keyed by label name. The OPTIONS-side
// iteration order is lost in the conversion, but the result matches
// bigqueryv2.Dataset.Labels exactly.
func evalLabelsOption(expr parsed.ExpressionNode) (map[string]string, error) {
	arr, ok := expr.(*parsed.ArrayConstructorNode)
	if !ok {
		return nil, fmt.Errorf("expected ARRAY, got %s", expr.Kind())
	}
	out := map[string]string{}
	for _, elem := range arr.Elements() {
		sc, ok := elem.(*parsed.StructConstructorWithParensNode)
		if !ok {
			return nil, fmt.Errorf("expected STRUCT element, got %s", elem.Kind())
		}
		fields := sc.FieldExpressions()
		if len(fields) != 2 {
			return nil, fmt.Errorf("expected 2 STRUCT fields, got %d", len(fields))
		}
		key, ok := fields[0].(*parsed.StringLiteralNode)
		if !ok {
			return nil, fmt.Errorf("label key: expected STRING, got %s", fields[0].Kind())
		}
		value, ok := fields[1].(*parsed.StringLiteralNode)
		if !ok {
			return nil, fmt.Errorf("label value: expected STRING, got %s", fields[1].Kind())
		}
		out[key.StringValue()] = value.StringValue()
	}
	return out, nil
}
