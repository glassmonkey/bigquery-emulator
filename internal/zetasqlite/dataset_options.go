package zetasqlite

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	parsed "github.com/glassmonkey/zetasql-wasm/ast"
)

// ErrOptionTypeMismatch is the typed witness for "OPTIONS value was a
// literal of the wrong shape" (e.g. INT given where STRING expected).
// Wrapped errors carry the option name and the kind details in their
// message; callers can errors.Is for the failure mode and additionally
// substring-match for the option name when locating which line broke.
var ErrOptionTypeMismatch = errors.New("CREATE SCHEMA OPTIONS value type mismatch")

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
			s, err := stringLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.Description = s
		case "friendly_name":
			s, err := stringLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.FriendlyName = s
		case "location":
			s, err := stringLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.Location = s
		case "storage_billing_model":
			s, err := stringLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.StorageBillingModel = s
		case "default_collation":
			s, err := stringLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.DefaultCollation = s
		case "default_rounding_mode":
			s, err := stringLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.DefaultRoundingMode = s
		case "default_table_expiration_days":
			n, err := intLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.DefaultTableExpirationDays = n
		case "default_partition_expiration_days":
			n, err := intLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.DefaultPartitionExpirationDays = n
		case "max_time_travel_hours":
			f, err := numberLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.MaxTimeTravelHours = f
		case "is_case_insensitive":
			b, err := boolLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.IsCaseInsensitive = b
		case "labels":
			labels, err := labelsLiteral(name, value)
			if err != nil {
				return out, nil, err
			}
			out.Labels = labels
		default:
			unknown = append(unknown, name)
		}
	}
	return out, unknown, nil
}

// The literal helpers below each share the same contract: produce the
// typed value for one OPTIONS entry, or return an error wrapping
// ErrOptionTypeMismatch so the caller (and tests) can detect the
// failure mode by type rather than by string. The OPTIONS name is
// included in the message for source localisation.

func stringLiteral(name string, expr parsed.ExpressionNode) (string, error) {
	s, ok := expr.(*parsed.StringLiteralNode)
	if !ok {
		return "", fmt.Errorf("OPTIONS(%s): expected STRING, got %s: %w", name, expr.Kind(), ErrOptionTypeMismatch)
	}
	return s.StringValue(), nil
}

func intLiteral(name string, expr parsed.ExpressionNode) (int64, error) {
	n, ok := expr.(*parsed.IntLiteralNode)
	if !ok {
		return 0, fmt.Errorf("OPTIONS(%s): expected INT, got %s: %w", name, expr.Kind(), ErrOptionTypeMismatch)
	}
	// IntLiteralNode.Image can include a trailing "L" suffix for long
	// literals, which strconv.ParseInt does not accept.
	s := strings.TrimSuffix(n.Image(), "L")
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("OPTIONS(%s): invalid INT literal %q: %w", name, n.Image(), err)
	}
	return v, nil
}

func numberLiteral(name string, expr parsed.ExpressionNode) (float64, error) {
	switch n := expr.(type) {
	case *parsed.FloatLiteralNode:
		v, err := strconv.ParseFloat(n.Image(), 64)
		if err != nil {
			return 0, fmt.Errorf("OPTIONS(%s): invalid FLOAT literal %q: %w", name, n.Image(), err)
		}
		return v, nil
	case *parsed.IntLiteralNode:
		// `max_time_travel_hours=168` is valid BigQuery DDL — promote
		// the INT to a float rather than reject it.
		s := strings.TrimSuffix(n.Image(), "L")
		i, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			return 0, fmt.Errorf("OPTIONS(%s): invalid INT literal %q: %w", name, n.Image(), err)
		}
		return float64(i), nil
	}
	return 0, fmt.Errorf("OPTIONS(%s): expected FLOAT, got %s: %w", name, expr.Kind(), ErrOptionTypeMismatch)
}

func boolLiteral(name string, expr parsed.ExpressionNode) (bool, error) {
	b, ok := expr.(*parsed.BooleanLiteralNode)
	if !ok {
		return false, fmt.Errorf("OPTIONS(%s): expected BOOL, got %s: %w", name, expr.Kind(), ErrOptionTypeMismatch)
	}
	return b.Value(), nil
}

// labelsLiteral decodes the `labels` OPTIONS shape, which BigQuery
// parses as ARRAY<STRUCT<STRING, STRING>>: `labels=[("k","v"), ...]`.
// Returns a map[string]string keyed by label name. The OPTIONS-side
// iteration order is lost in the conversion, but the result matches
// bigqueryv2.Dataset.Labels exactly.
func labelsLiteral(name string, expr parsed.ExpressionNode) (map[string]string, error) {
	arr, ok := expr.(*parsed.ArrayConstructorNode)
	if !ok {
		return nil, fmt.Errorf("OPTIONS(%s): expected ARRAY, got %s: %w", name, expr.Kind(), ErrOptionTypeMismatch)
	}
	out := map[string]string{}
	for _, elem := range arr.Elements() {
		sc, ok := elem.(*parsed.StructConstructorWithParensNode)
		if !ok {
			return nil, fmt.Errorf("OPTIONS(%s): expected STRUCT element, got %s: %w", name, elem.Kind(), ErrOptionTypeMismatch)
		}
		fields := sc.FieldExpressions()
		if len(fields) != 2 {
			return nil, fmt.Errorf("OPTIONS(%s): expected 2 STRUCT fields, got %d: %w", name, len(fields), ErrOptionTypeMismatch)
		}
		key, ok := fields[0].(*parsed.StringLiteralNode)
		if !ok {
			return nil, fmt.Errorf("OPTIONS(%s): label key expected STRING, got %s: %w", name, fields[0].Kind(), ErrOptionTypeMismatch)
		}
		value, ok := fields[1].(*parsed.StringLiteralNode)
		if !ok {
			return nil, fmt.Errorf("OPTIONS(%s): label value expected STRING, got %s: %w", name, fields[1].Kind(), ErrOptionTypeMismatch)
		}
		out[key.StringValue()] = value.StringValue()
	}
	return out, nil
}
