package zetasqlite

import (
	"fmt"
	"strconv"
	"strings"

	parsed "github.com/glassmonkey/zetasql-wasm/ast"
)

// decodeCreateSchemaOptions evaluates parsed-AST OPTIONS into a
// DatasetOptions value. The resolved-AST side of CreateSchemaStmtNode
// exposes OPTIONS only as raw proto, so the parsed-AST side
// (OptionsListNode → OptionsEntryNode → typed literal nodes) is the
// path the emulator decodes from. See analyzer.go
// newCreateSchemaStmtAction for the wiring.
//
// The case list is the *authoritative* enumeration of OPTIONS the
// emulator persists; OPTIONS not in this switch are accepted but
// returned in the unknown slice so the server can warn-log them.
// Keep this list aligned with datasetContentFromOptions on the
// server side — both lists encode the "what we persist" boundary.
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
		if err := assignSchemaOption(&out, name, entry.Value()); err != nil {
			if err == errUnknownSchemaOption {
				unknown = append(unknown, name)
				continue
			}
			return out, nil, fmt.Errorf("OPTIONS(%s): %w", name, err)
		}
	}
	return out, unknown, nil
}

// errUnknownSchemaOption is a sentinel: assignSchemaOption returns
// it when the OPTIONS name does not correspond to any persisted
// field, so the caller can route it to UnknownOptions instead of
// failing.
var errUnknownSchemaOption = fmt.Errorf("unknown schema option")

func assignSchemaOption(out *DatasetOptions, name string, value parsed.ExpressionNode) error {
	switch name {
	case "description":
		v, err := evalStringOption(value)
		if err != nil {
			return err
		}
		out.Description = v
	case "friendly_name":
		v, err := evalStringOption(value)
		if err != nil {
			return err
		}
		out.FriendlyName = v
	case "location":
		v, err := evalStringOption(value)
		if err != nil {
			return err
		}
		out.Location = v
	case "storage_billing_model":
		v, err := evalStringOption(value)
		if err != nil {
			return err
		}
		out.StorageBillingModel = v
	case "default_collation":
		v, err := evalStringOption(value)
		if err != nil {
			return err
		}
		out.DefaultCollation = v
	case "default_rounding_mode":
		v, err := evalStringOption(value)
		if err != nil {
			return err
		}
		out.DefaultRoundingMode = v
	case "labels":
		v, err := evalLabelsOption(value)
		if err != nil {
			return err
		}
		out.Labels = v
	case "default_table_expiration_days":
		v, err := evalInt64Option(value)
		if err != nil {
			return err
		}
		out.DefaultTableExpirationDays = v
	case "default_partition_expiration_days":
		v, err := evalInt64Option(value)
		if err != nil {
			return err
		}
		out.DefaultPartitionExpirationDays = v
	case "max_time_travel_hours":
		v, err := evalFloat64Option(value)
		if err != nil {
			return err
		}
		out.MaxTimeTravelHours = v
	case "is_case_insensitive":
		v, err := evalBoolOption(value)
		if err != nil {
			return err
		}
		out.IsCaseInsensitive = v
	default:
		return errUnknownSchemaOption
	}
	return nil
}

func evalStringOption(expr parsed.ExpressionNode) (string, error) {
	s, ok := expr.(*parsed.StringLiteralNode)
	if !ok {
		return "", fmt.Errorf("expected STRING literal, got %s", expr.Kind())
	}
	return s.StringValue(), nil
}

func evalInt64Option(expr parsed.ExpressionNode) (int64, error) {
	n, ok := expr.(*parsed.IntLiteralNode)
	if !ok {
		return 0, fmt.Errorf("expected INT literal, got %s", expr.Kind())
	}
	// IntLiteralNode.Image() can include a trailing "L" suffix for
	// long literals; strconv.ParseInt does not accept it.
	s := strings.TrimSuffix(n.Image(), "L")
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid int literal %q: %w", n.Image(), err)
	}
	return v, nil
}

func evalFloat64Option(expr parsed.ExpressionNode) (float64, error) {
	switch n := expr.(type) {
	case *parsed.FloatLiteralNode:
		v, err := strconv.ParseFloat(n.Image(), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid float literal %q: %w", n.Image(), err)
		}
		return v, nil
	case *parsed.IntLiteralNode:
		// `max_time_travel_hours=168` is valid SQL; promote to float.
		s := strings.TrimSuffix(n.Image(), "L")
		i, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number literal %q: %w", n.Image(), err)
		}
		return float64(i), nil
	}
	return 0, fmt.Errorf("expected number literal, got %s", expr.Kind())
}

func evalBoolOption(expr parsed.ExpressionNode) (bool, error) {
	b, ok := expr.(*parsed.BooleanLiteralNode)
	if !ok {
		return false, fmt.Errorf("expected BOOL literal, got %s", expr.Kind())
	}
	return b.Value(), nil
}

// evalLabelsOption decodes the `labels` OPTIONS shape, which BigQuery
// parses as ARRAY<STRUCT<STRING, STRING>>: `labels=[("k","v"), ...]`.
// Returns a map[string]string keyed by label name. The OPTIONS-side
// iteration order is lost in the conversion, but the result matches
// bigqueryv2.Dataset.Labels exactly.
func evalLabelsOption(expr parsed.ExpressionNode) (map[string]string, error) {
	arr, ok := expr.(*parsed.ArrayConstructorNode)
	if !ok {
		return nil, fmt.Errorf("expected ARRAY literal, got %s", expr.Kind())
	}
	out := map[string]string{}
	for _, elem := range arr.Elements() {
		key, value, err := evalLabelEntry(elem)
		if err != nil {
			return nil, fmt.Errorf("label entry: %w", err)
		}
		out[key] = value
	}
	return out, nil
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
	key, err := evalStringOption(fields[0])
	if err != nil {
		return "", "", fmt.Errorf("key: %w", err)
	}
	value, err := evalStringOption(fields[1])
	if err != nil {
		return "", "", fmt.Errorf("value: %w", err)
	}
	return key, value, nil
}
