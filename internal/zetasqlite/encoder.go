package zetasqlite

import (
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	ast "github.com/glassmonkey/zetasql-wasm/resolved_ast"
	"github.com/glassmonkey/zetasql-wasm/types"
	"github.com/goccy/go-json"
)

func EncodeNamedValues(v []driver.NamedValue, params []*ast.ParameterNode) ([]sql.NamedArg, error) {
	if len(v) != len(params) {
		return nil, fmt.Errorf(
			"failed to match named values num (%d) and params num (%d)",
			len(v), len(params),
		)
	}
	ret := make([]sql.NamedArg, 0, len(v))
	for idx, vv := range v {
		converted, err := encodeNamedValue(vv, params[idx])
		if err != nil {
			return nil, fmt.Errorf("failed to convert value from %+v: %w", vv, err)
		}
		ret = append(ret, converted)
	}
	return ret, nil
}

func EncodeGoValues(v []interface{}, params []*ast.ParameterNode) ([]interface{}, error) {
	if len(v) != len(params) {
		return nil, fmt.Errorf(
			"failed to match args values num (%d) and params num (%d)",
			len(v), len(params),
		)
	}
	ret := make([]interface{}, 0, len(v))
	for idx, vv := range v {
		paramType, err := types.TypeFromProto(params[idx].Type())
		if err != nil {
			return nil, fmt.Errorf("failed to convert parameter type: %w", err)
		}
		value, err := EncodeGoValue(paramType, vv)
		if err != nil {
			return nil, err
		}
		ret = append(ret, value)
	}
	return ret, nil
}

func EncodeGoValue(t types.Type, v interface{}) (interface{}, error) {
	value, err := ValueFromGoValue(v)
	if err != nil {
		return nil, err
	}
	casted, err := CastValue(t, value)
	if err != nil {
		return nil, err
	}
	return EncodeValue(casted)
}

func EncodeValue(v Value) (interface{}, error) {
	if v == nil {
		return nil, nil
	}
	switch vv := v.(type) {
	case IntValue:
		return v.ToInt64()
	case FloatValue:
		return v.ToFloat64()
	case BoolValue:
		return v.ToBool()
	case *SafeValue:
		return EncodeValue(vv.value)
	}
	layout, err := valueLayoutFromValue(v)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(layout)
	if err != nil {
		return nil, fmt.Errorf("failed to encode value: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func LiteralFromValue(v Value) (string, error) {
	if v == nil {
		return "null", nil
	}
	switch vv := v.(type) {
	case IntValue:
		i64, err := v.ToInt64()
		if err != nil {
			return "", err
		}
		return fmt.Sprint(i64), nil
	case FloatValue:
		f64, err := v.ToFloat64()
		if err != nil {
			return "", err
		}
		value := strconv.FormatFloat(f64, 'g', -1, 64)
		if !strings.Contains(value, ".") && !strings.Contains(value, "e") {
			// append x.0 suffix to keep float value context
			value = fmt.Sprintf("%s.0", value)
		}
		return value, nil
	case BoolValue:
		b, err := v.ToBool()
		if err != nil {
			return "", err
		}
		return fmt.Sprint(b), nil
	case *SafeValue:
		return LiteralFromValue(vv.value)
	}
	layout, err := valueLayoutFromValue(v)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(layout)
	if err != nil {
		return "", fmt.Errorf("failed to encode value: %w", err)
	}
	return fmt.Sprintf("%q", base64.StdEncoding.EncodeToString(b)), nil
}

// LiteralFromZetaSQLValue formats a zetasql-wasm LiteralValue as a SQLite
// SQL literal in the fork's storage convention: scalar numerics/booleans
// are emitted directly, everything else (strings, bytes, arrays, structs,
// dates, timestamps) is round-tripped through valueLayoutFromValue and
// stored as a base64-encoded JSON blob that the SQLite-side custom
// functions decode at read time. NULL (Value == nil) becomes "null".
func LiteralFromZetaSQLValue(v *types.LiteralValue) (string, error) {
	val, err := ValueFromZetaSQLValue(v)
	if err != nil {
		return "", err
	}
	return LiteralFromValue(val)
}

// ValueFromZetaSQLValue lifts a wrapped LiteralValue into the fork's Value
// hierarchy. ARRAY / STRUCT values are reconstructed by recursing into
// their element LiteralValues; field names for STRUCT are read off the
// surrounding StructType. Returns nil for SQL NULL (proto oneof unset) and
// for kinds the wrap layer cannot model yet (LiteralValue.Value == nil).
//
// DATE / TIMESTAMP / ENUM go through zetasql-wasm typed accessors that
// hide the proto representation (int32 days / *timestamppb.Timestamp /
// proto enum value) behind an ergonomic surface. ENUM literals are
// lifted to StringValue carrying the declared name (e.g. "DAY" for
// DateTimePart) so the date-part bind functions get the form they
// already expect from .ToString().
func ValueFromZetaSQLValue(v *types.LiteralValue) (Value, error) {
	if v == nil || v.Value == nil {
		return nil, nil
	}
	if days, ok := v.AsDateDays(); ok {
		return dateValueFromLiteral(int64(days)), nil
	}
	if dt, ok := v.AsDatetime(); ok {
		return DatetimeValue(dt), nil
	}
	if t, ok := v.AsTimeOfDay(); ok {
		return TimeValue(t), nil
	}
	if ts, ok := v.AsTimestamp(); ok {
		return TimestampValue(ts), nil
	}
	if j, ok := v.AsJson(); ok {
		return JsonValue(j), nil
	}
	if name, ok := v.AsEnumName(); ok {
		return StringValue(name), nil
	}
	// INTERVAL has no typed accessor on LiteralValue yet (the upstream
	// proto stores it as the 16-byte SerializeAndAppendToBytes payload —
	// see zetasql/public/interval_value.cc). Detect it by Type.Kind so
	// the downstream UDFs see IntervalValue rather than the raw bytes
	// (without this, the default `[]byte` branch in ValueFromGoValue
	// lifts it as BytesValue and the date arithmetic / EXTRACT /
	// JUSTIFY_INTERVAL binds reject "zetasqlite.BytesValue").
	if v.Type != nil && v.Type.Kind() == types.Interval {
		if b, ok := v.Value.([]byte); ok {
			return intervalValueFromZetaSQLBytes(b)
		}
	}
	if b, ok := v.AsNumeric(); ok {
		return numericValueFromZetaSQLBytes(b), nil
	}
	if b, ok := v.AsBigNumeric(); ok {
		return bigNumericValueFromZetaSQLBytes(b), nil
	}
	switch elts := v.Value.(type) {
	case types.ArrayValue:
		arr := &ArrayValue{}
		for _, e := range elts {
			inner, err := ValueFromZetaSQLValue(e)
			if err != nil {
				return nil, err
			}
			arr.values = append(arr.values, inner)
		}
		return arr, nil
	case types.StructValue:
		st := v.Type.AsStruct()
		if st == nil || len(st.Fields) != len(elts) {
			return nil, fmt.Errorf("struct value/type field count mismatch")
		}
		s := &StructValue{m: map[string]Value{}}
		for i, e := range elts {
			inner, err := ValueFromZetaSQLValue(e)
			if err != nil {
				return nil, err
			}
			name := st.Fields[i].Name
			s.keys = append(s.keys, name)
			s.values = append(s.values, inner)
			s.m[name] = inner
		}
		return s, nil
	default:
		return ValueFromGoValue(v.Value)
	}
}

func intValueFromLiteral(lit string) (IntValue, error) {
	v, err := strconv.ParseInt(lit, 10, 64)
	if err != nil {
		return 0, err
	}
	return IntValue(v), nil
}

func boolValueFromLiteral(lit string) (BoolValue, error) {
	v, err := strconv.ParseBool(lit)
	if err != nil {
		return false, err
	}
	return BoolValue(v), nil
}

func floatValueFromLiteral(lit string) (FloatValue, error) {
	v, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return 0, err
	}
	return FloatValue(v), nil
}

func stringValueFromLiteral(lit string) (StringValue, error) {
	v, err := strconv.Unquote(lit)
	if err != nil {
		return "", fmt.Errorf("failed to unquote from string literal: %w", err)
	}
	return StringValue(v), nil
}

func bytesValueFromLiteral(lit string) BytesValue {
	// use a workaround because ToBytes doesn't work with certain values.
	unquoted, err := strconv.Unquote(lit[1:])
	if err != nil {
		return BytesValue(lit)
	}
	return BytesValue(unquoted)
}

func dateValueFromLiteral(days int64) DateValue {
	t := time.Unix(int64(time.Duration(days)*24*(time.Hour/time.Second)), 0)
	return DateValue(t)
}

const (
	secShift     = 0
	minShift     = 6
	hourShift    = 12
	dayShift     = 17
	monthShift   = 22
	yearShift    = 26
	microSecMask = 0xFFFFF
	secMask      = 0b111111
	minMask      = 0b111111 << minShift
	hourMask     = 0b11111 << hourShift
	dayMask      = 0b11111 << dayShift
	monthMask    = 0b1111 << monthShift
	yearMask     = 0x3FFF << yearShift
)

func datetimeValueFromLiteral(bit int64) DatetimeValue {
	b := bit >> 20
	year := (b & yearMask) >> yearShift
	month := (b & monthMask) >> monthShift
	day := (b & dayMask) >> dayShift
	hour := (b & hourMask) >> hourShift
	min := (b & minMask) >> minShift
	sec := (b & secMask) >> secShift
	microSec := (bit & microSecMask) >> 0
	t := time.Date(
		int(year),
		time.Month(month),
		int(day),
		int(hour),
		int(min),
		int(sec),
		int(microSec)*1000, time.UTC,
	)
	return DatetimeValue(t)
}

func timeValueFromLiteral(bit int64) TimeValue {
	b := bit >> 20
	hour := (b & hourMask) >> hourShift
	min := (b & minMask) >> minShift
	sec := (b & secMask) >> secShift
	microSec := (bit & microSecMask) >> 0
	t := time.Date(0, 0, 0, int(hour), int(min), int(sec), int(microSec)*1000, time.UTC)
	return TimeValue(t)
}

func timestampValueFromLiteral(t time.Time) (TimestampValue, error) {
	return TimestampValue(t), nil
}

var (
	numericLiteralPattern = regexp.MustCompile(`NUMERIC "(.+)"`)
)

// numericValueFromZetaSQLBytes decodes the proto-encoded NUMERIC payload
// that LiteralValue.AsNumeric returns: a little-endian two's-complement
// signed integer equal to the value scaled by 10^9. Empty slice = zero.
func numericValueFromZetaSQLBytes(b []byte) *NumericValue {
	return &NumericValue{Rat: ratFromScaledLEBytes(b, 9)}
}

// bigNumericValueFromZetaSQLBytes mirrors numericValueFromZetaSQLBytes for
// BIGNUMERIC literals; the wire encoding is identical, the scale is 10^38.
func bigNumericValueFromZetaSQLBytes(b []byte) *NumericValue {
	return &NumericValue{Rat: ratFromScaledLEBytes(b, 38), isBigNumeric: true}
}

// ratFromScaledLEBytes decodes a little-endian two's-complement signed
// integer into a *big.Rat divided by 10^scaleDigits. Empty slice = zero.
// It is the shared primitive behind the NUMERIC / BIGNUMERIC literal
// lifts; the scale belongs to those callers, not here.
func ratFromScaledLEBytes(b []byte, scaleDigits int) *big.Rat {
	if len(b) == 0 {
		return new(big.Rat)
	}
	be := make([]byte, len(b))
	for i, x := range b {
		be[len(b)-1-i] = x
	}
	z := new(big.Int).SetBytes(be)
	if be[0]&0x80 != 0 {
		twoPow := new(big.Int).Lsh(big.NewInt(1), uint(8*len(b)))
		z.Sub(z, twoPow)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scaleDigits)), nil)
	return new(big.Rat).SetFrac(z, scale)
}

func numericValueFromLiteral(lit string) (*NumericValue, error) {
	matches := numericLiteralPattern.FindAllStringSubmatch(lit, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("unexpected numeric literal: %s", lit)
	}
	if len(matches[0]) != 2 {
		return nil, fmt.Errorf("unexpected numeric literal: %s", lit)
	}
	numericLit := matches[0][1]
	r := new(big.Rat)
	r.SetString(numericLit)
	if strings.Contains(lit, "BIGNUMERIC") {
		return &NumericValue{Rat: r, isBigNumeric: true}, nil
	}
	return &NumericValue{Rat: r}, nil
}

func jsonValueFromLiteral(lit string) (JsonValue, error) {
	return JsonValue(lit), nil
}

var (
	intervalLiteralPattern = regexp.MustCompile(`INTERVAL "(.+)"`)
)

func intervalValueFromLiteral(lit string) (*IntervalValue, error) {
	matches := intervalLiteralPattern.FindAllStringSubmatch(lit, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("unexpected interval literal: %s", lit)
	}
	if len(matches[0]) != 2 {
		return nil, fmt.Errorf("unexpected interval literal: %s", lit)
	}
	intervalLit := matches[0][1]
	return parseInterval(intervalLit)
}

// intervalValueFromZetaSQLBytes lifts the 16-byte little-endian
// IntervalValue serialization (zetasql/public/interval_value.cc's
// SerializeAndAppendToBytes) into the fork's IntervalValue. Layout:
//
//	bytes[0:8]   int64  micros        (LE, signed)
//	bytes[8:12]  int32  days          (LE, signed)
//	bytes[12:16] uint32 months_nanos  (LE, packed)
//
// months_nanos packs the absolute month count in bits 13..30 with the
// sign in bit 31, and the nano-fraction part (0..999) in bits 0..9.
// The masks match the upstream constants (kMonthSignMask 0x80000000,
// kMonthsMask 0x7FFFE000, kMonthsShift 13, kNanosMask 0x000003FF).
//
// The decoded parts are folded into bigquery.IntervalValue's Y/M/D and
// H:M:S.nanos buckets with consistent signs within each sign-group
// (Y-M shares a sign, D is independent, H:M:S.nanos shares a sign), so
// the existing IntervalValue downstream (UDFs, formatter) sees the
// same shape it gets from parseInterval / CastValue's Interval branch.
func intervalValueFromZetaSQLBytes(b []byte) (*IntervalValue, error) {
	const expectedLen = 16
	if len(b) == 0 {
		// Upstream treats an empty serialization as the zero interval.
		return &IntervalValue{IntervalValue: &bigquery.IntervalValue{}}, nil
	}
	if len(b) != expectedLen {
		return nil, fmt.Errorf("interval bytes: expected %d bytes, got %d", expectedLen, len(b))
	}

	micros := int64(binary.LittleEndian.Uint64(b[0:8]))
	days := int32(binary.LittleEndian.Uint32(b[8:12]))
	monthsNanos := binary.LittleEndian.Uint32(b[12:16])

	monthsAbs := int64((monthsNanos & 0x7FFFE000) >> 13)
	months := monthsAbs
	if monthsNanos&0x80000000 != 0 {
		months = -monthsAbs
	}
	nanoFractions := int64(monthsNanos & 0x000003FF)

	// Signed integer division and remainder in Go truncate toward zero,
	// so direct `micros / N` / `micros % N` already give H/M/S/Nanos
	// the same sign as the source — the sign-group consistency rule
	// holds for free without an absMicros + timeSign indirection. The
	// nano_fractions field is always non-negative (0..999) and adds in
	// the same direction the micros remainder pushes; the negative
	// total case (micros=-1, nano_fractions=999 → -1 nano) falls out
	// naturally because microsRem*1000 is already negative when micros
	// is negative.
	bv := &bigquery.IntervalValue{
		Days:   days,
		Years:  int32(months / 12),
		Months: int32(months % 12),
		Hours:  int32(micros / 3_600_000_000),
	}
	microsRem := micros % 3_600_000_000
	bv.Minutes = int32(microsRem / 60_000_000)
	microsRem %= 60_000_000
	bv.Seconds = int32(microsRem / 1_000_000)
	microsRem %= 1_000_000
	bv.SubSecondNanos = int32(microsRem*1000 + nanoFractions)

	return &IntervalValue{IntervalValue: bv}, nil
}

// TODO(zetasql-wasm-migration): array/struct value decoders are part of the
// runtime-value bridge and stubbed alongside ValueFromZetaSQLValue.
func arrayValueFromLiteral(v *types.LiteralValue) (*ArrayValue, error) {
	_ = v
	return nil, fmt.Errorf("arrayValueFromLiteral: zetasql-wasm runtime value bridge not yet implemented")
}

func structValueFromLiteral(v *types.LiteralValue) (*StructValue, error) {
	_ = v
	return nil, fmt.Errorf("structValueFromLiteral: zetasql-wasm runtime value bridge not yet implemented")
}

func CastValue(t types.Type, v Value) (Value, error) {
	if v == nil {
		return nil, nil
	}
	switch t.Kind() {
	case types.Int32, types.Int64, types.Uint32, types.Uint64:
		i64, err := v.ToInt64()
		if err != nil {
			return nil, err
		}
		return IntValue(i64), nil
	case types.Bool:
		b, err := v.ToBool()
		if err != nil {
			return nil, err
		}
		return BoolValue(b), nil
	case types.Float, types.Double:
		f64, err := v.ToFloat64()
		if err != nil {
			return nil, err
		}
		return FloatValue(f64), nil
	case types.String, types.Enum:
		s, err := v.ToString()
		if err != nil {
			return nil, err
		}
		return StringValue(s), nil
	case types.Bytes:
		b, err := v.ToBytes()
		if err != nil {
			return nil, err
		}
		return BytesValue(b), nil
	case types.Date:
		t, err := v.ToTime()
		if err != nil {
			return nil, err
		}
		return DateValue(t), nil
	case types.Datetime:
		t, err := v.ToTime()
		if err != nil {
			return nil, err
		}
		return DatetimeValue(t), nil
	case types.Time:
		t, err := v.ToTime()
		if err != nil {
			return nil, err
		}
		return TimeValue(t), nil
	case types.Timestamp:
		t, err := v.ToTime()
		if err != nil {
			return nil, err
		}
		return TimestampValue(t), nil
	case types.Interval:
		s, err := v.ToString()
		if err != nil {
			return nil, err
		}
		return parseInterval(s)
	case types.Array:
		array, err := v.ToArray()
		if err != nil {
			return nil, err
		}
		elemType := t.AsArray().ElementType
		ret := &ArrayValue{}
		for _, value := range array.values {
			casted, err := CastValue(elemType, value)
			if err != nil {
				return nil, err
			}
			ret.values = append(ret.values, casted)
		}
		return ret, nil
	case types.Struct:
		if array, ok := v.(*ArrayValue); ok {
			ret := &StructValue{m: map[string]Value{}}
			for _, value := range array.values {
				st, err := value.ToStruct()
				if err != nil {
					return nil, err
				}
				ret.keys = append(ret.keys, st.keys...)
				ret.values = append(ret.values, st.values...)
				for i, k := range st.keys {
					ret.m[k] = st.values[i]
				}
			}
			return ret, nil
		}
		s, err := v.ToStruct()
		if err != nil {
			return nil, err
		}
		typ := t.AsStruct()
		anonymousStruct := true
		for _, key := range s.keys {
			if key != "" {
				anonymousStruct = false
			}
		}
		if anonymousStruct {
			return s, nil
		}
		ret := &StructValue{m: s.m}
		for i := 0; i < len(typ.Fields); i++ {
			key := typ.Fields[i].Name
			value, exists := s.m[key]
			if !exists {
				ret.keys = append(ret.keys, key)
				ret.values = append(ret.values, nil)
				continue
			}
			casted, err := CastValue(typ.Fields[i].Type, value)
			if err != nil {
				return nil, err
			}
			ret.keys = append(ret.keys, key)
			ret.values = append(ret.values, casted)
			ret.m[key] = casted
		}
		return ret, nil
	case types.Numeric:
		r, err := v.ToRat()
		if err != nil {
			return nil, err
		}
		return &NumericValue{Rat: r}, nil
	case types.BigNumeric:
		r, err := v.ToRat()
		if err != nil {
			return nil, err
		}
		return &NumericValue{Rat: r, isBigNumeric: true}, nil
	case types.Json:
		j, err := v.ToJSON()
		if err != nil {
			return nil, err
		}
		return JsonValue(j), nil
	case types.Geography:
		return v, nil
	}
	return nil, fmt.Errorf("unsupported cast %s value", t.Kind())
}

func ValueFromGoValue(v interface{}) (Value, error) {
	if isNullValue(v) {
		return nil, nil
	}
	return valueFromGoReflectValue(reflect.ValueOf(v))
}

// inferZetaSQLType returns the ZetaSQL Type that corresponds to v's Go type.
// This is used to register query-parameter types with the analyzer before
// the SQL is parsed, so that parameter references like @ids resolve.
//
// Untyped nil (and typed nil pointers) cannot be inferred — the caller has
// to resolve the type some other way (e.g. from the surrounding SQL or the
// declared parameter type) before passing the value in.
func inferZetaSQLType(v interface{}) (types.Type, error) {
	if v == nil {
		return nil, fmt.Errorf("cannot infer zetasql type from untyped nil")
	}
	return inferZetaSQLTypeFromReflect(reflect.ValueOf(v))
}

func inferZetaSQLTypeFromReflect(v reflect.Value) (types.Type, error) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return types.Int64Type(), nil
	case reflect.Float32, reflect.Float64:
		return types.DoubleType(), nil
	case reflect.Bool:
		return types.BoolType(), nil
	case reflect.String:
		return types.StringType(), nil
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return types.BytesType(), nil
		}
		// Element type is taken from the static type, not a sample element,
		// so empty slices still produce a well-typed ARRAY<...>.
		elemSample := reflect.New(v.Type().Elem()).Elem()
		elem, err := inferZetaSQLTypeFromReflect(elemSample)
		if err != nil {
			return nil, fmt.Errorf("failed to infer array element type: %w", err)
		}
		return types.NewArrayType(elem)
	case reflect.Ptr:
		if v.IsNil() {
			// Nil-typed pointers fall back to the pointee's static type.
			return inferZetaSQLTypeFromReflect(reflect.New(v.Type().Elem()).Elem())
		}
		return inferZetaSQLTypeFromReflect(v.Elem())
	case reflect.Interface:
		if v.IsNil() {
			return nil, fmt.Errorf("cannot infer zetasql type from nil interface")
		}
		return inferZetaSQLTypeFromReflect(reflect.ValueOf(v.Interface()))
	case reflect.Struct:
		if _, ok := v.Interface().(time.Time); ok {
			return types.TimestampType(), nil
		}
	}
	return nil, fmt.Errorf("cannot infer zetasql type from go value of kind %s", v.Kind())
}

func valueFromGoReflectValue(v reflect.Value) (Value, error) {
	kind := v.Type().Kind()
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return IntValue(v.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return IntValue(int64(v.Uint())), nil
	case reflect.Float32, reflect.Float64:
		return FloatValue(v.Float()), nil
	case reflect.Bool:
		return BoolValue(v.Bool()), nil
	case reflect.String:
		return StringValue(v.String()), nil
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return BytesValue(v.Bytes()), nil
		}
		ret := &ArrayValue{}
		for i := 0; i < v.Len(); i++ {
			elem, err := valueFromGoReflectValue(v.Index(i))
			if err != nil {
				return nil, err
			}
			ret.values = append(ret.values, elem)
		}
		return ret, nil
	case reflect.Map:
		ret := &StructValue{m: map[string]Value{}}
		iter := v.MapRange()
		for iter.Next() {
			key, err := valueFromGoReflectValue(iter.Key())
			if err != nil {
				return nil, err
			}
			k, err := key.ToString()
			if err != nil {
				return nil, err
			}
			value, err := valueFromGoReflectValue(iter.Value())
			if err != nil {
				return nil, err
			}
			ret.keys = append(ret.keys, k)
			ret.values = append(ret.values, value)
			ret.m[k] = value
		}
		return ret, nil
	case reflect.Struct:
		t, ok := v.Interface().(time.Time)
		if ok {
			return TimestampValue(t), nil
		}
		ret := &StructValue{m: map[string]Value{}}
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			key := typ.Field(i).Name
			value, err := valueFromGoReflectValue(v.Field(i))
			if err != nil {
				return nil, err
			}
			ret.keys = append(ret.keys, key)
			ret.values = append(ret.values, value)
			ret.m[key] = value
		}
		return ret, nil
	case reflect.Ptr:
		return valueFromGoReflectValue(v.Elem())
	case reflect.Interface:
		vv := v.Interface()
		if isNullValue(vv) {
			return nil, nil
		}
		return valueFromGoReflectValue(reflect.ValueOf(vv))
	}
	return nil, fmt.Errorf("cannot convert %s type to zetasqlite value type", kind)
}

func encodeNamedValue(v driver.NamedValue, param *ast.ParameterNode) (sql.NamedArg, error) {
	paramType, err := types.TypeFromProto(param.Type())
	if err != nil {
		return sql.NamedArg{}, fmt.Errorf("failed to convert parameter type: %w", err)
	}
	value, err := EncodeGoValue(paramType, v.Value)
	if err != nil {
		return sql.NamedArg{}, err
	}
	return sql.NamedArg{
		Name:  strings.ToLower(v.Name),
		Value: value,
	}, nil
}

func valueLayoutFromValue(v Value) (*ValueLayout, error) {
	switch vv := v.(type) {
	case StringValue:
		return &ValueLayout{
			Header: StringValueType,
			Body:   string(vv),
		}, nil
	case BytesValue:
		return &ValueLayout{
			Header: BytesValueType,
			Body:   base64.StdEncoding.EncodeToString([]byte(vv)),
		}, nil
	case *NumericValue:
		b, err := vv.Rat.MarshalText()
		if err != nil {
			return nil, err
		}
		if vv.isBigNumeric {
			return &ValueLayout{
				Header: BigNumericValueType,
				Body:   string(b),
			}, nil
		}
		return &ValueLayout{
			Header: NumericValueType,
			Body:   string(b),
		}, nil
	case DateValue:
		body, err := vv.ToString()
		if err != nil {
			return nil, err
		}
		return &ValueLayout{
			Header: DateValueType,
			Body:   body,
		}, nil
	case DatetimeValue:
		body, err := vv.ToString()
		if err != nil {
			return nil, err
		}
		return &ValueLayout{
			Header: DatetimeValueType,
			Body:   body,
		}, nil
	case TimeValue:
		body, err := vv.ToString()
		if err != nil {
			return nil, err
		}
		return &ValueLayout{
			Header: TimeValueType,
			Body:   body,
		}, nil
	case TimestampValue:
		return &ValueLayout{
			Header: TimestampValueType,
			Body:   fmt.Sprint(time.Time(vv).UnixMicro()),
		}, nil
	case *IntervalValue:
		s, err := vv.ToString()
		if err != nil {
			return nil, err
		}
		return &ValueLayout{
			Header: IntervalValueType,
			Body:   s,
		}, nil
	case JsonValue:
		return &ValueLayout{
			Header: JsonValueType,
			Body:   string(vv),
		}, nil
	case *ArrayValue:
		values := make([]interface{}, 0, len(vv.values))
		for _, v := range vv.values {
			if v == nil {
				values = append(values, nil)
				continue
			}
			value, err := EncodeValue(v)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		body, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		return &ValueLayout{
			Header: ArrayValueType,
			Body:   string(body),
		}, nil
	case *StructValue:
		values := make([]interface{}, 0, len(vv.values))
		for _, v := range vv.values {
			value, err := EncodeValue(v)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		body, err := json.Marshal(&StructValueLayout{
			Keys:   vv.keys,
			Values: values,
		})
		if err != nil {
			return nil, err
		}
		return &ValueLayout{
			Header: StructValueType,
			Body:   string(body),
		}, nil
	case *GeographyValue:
		s, err := vv.ToWKT()
		if err != nil {
			return nil, err
		}
		return &ValueLayout{
			Header: GeographyValueType,
			Body:   s,
		}, nil
	}
	return nil, fmt.Errorf("unexpected value type to get value layout: %T", v)
}
