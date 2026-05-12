package zetasqlite

import (
	"reflect"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/glassmonkey/zetasql-wasm/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestValueFromZetaSQLValue locks in the lift for kinds whose Go
// representation in *types.LiteralValue does not pass cleanly through
// reflect-based generic conversion: DATE (int32 days since epoch) and
// TIMESTAMP (*timestamppb.Timestamp). The lift goes through
// zetasql-wasm v0.8.0 typed accessors (AsDateDays / AsTimestamp), so
// the proto representation never leaks into this package.
func TestValueFromZetaSQLValue(t *testing.T) {
	t.Setenv("TZ", "UTC")

	tsTime := time.Date(2020, 9, 22, 12, 30, 0, 0, time.UTC)
	tsProto := timestamppb.New(tsTime)

	for _, tc := range []struct {
		name string
		sut  *types.LiteralValue
		want Value
	}{
		{
			name: "nil literal",
			sut:  nil,
			want: nil,
		},
		{
			name: "nil inner value (SQL NULL)",
			sut:  &types.LiteralValue{Type: types.DateType(), Value: nil},
			want: nil,
		},
		{
			name: "DATE int32 days lifted to DateValue",
			sut:  &types.LiteralValue{Type: types.DateType(), Value: int32(18527)},
			want: dateValueFromLiteral(18527),
		},
		{
			name: "TIMESTAMP proto lifted to TimestampValue",
			sut:  &types.LiteralValue{Type: types.TimestampType(), Value: tsProto},
			want: TimestampValue(tsTime),
		},
		{
			name: "INT64 falls through to IntValue",
			sut:  &types.LiteralValue{Type: types.Int64Type(), Value: int64(42)},
			want: IntValue(42),
		},
		{
			name: "STRING falls through to StringValue",
			sut:  &types.LiteralValue{Type: types.StringType(), Value: "hello"},
			want: StringValue("hello"),
		},
		{
			name: "BOOL falls through to BoolValue",
			sut:  &types.LiteralValue{Type: types.BoolType(), Value: true},
			want: BoolValue(true),
		},
		{
			// Front-door coverage for the Interval dispatch in
			// ValueFromZetaSQLValue: with Type.Kind == Interval the
			// 16-byte payload is routed through
			// intervalValueFromZetaSQLBytes. The bit-packing itself is
			// triangulated by TestIntervalValueFromZetaSQLBytes; this
			// case just locks the dispatch wiring with a single
			// realistic value (CAST('1-2 3 18:1:55' AS INTERVAL)).
			name: "INTERVAL bytes lifted to IntervalValue",
			sut: &types.LiteralValue{
				Type: types.TypeFromKind(types.Interval),
				Value: []byte{
					0xc0, 0x4a, 0x3c, 0x1d, 0x0f, 0x00, 0x00, 0x00,
					0x03, 0x00, 0x00, 0x00,
					0x00, 0xc0, 0x01, 0x00,
				},
			},
			want: &IntervalValue{IntervalValue: &bigquery.IntervalValue{
				Years: 1, Months: 2, Days: 3,
				Hours: 18, Minutes: 1, Seconds: 55,
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got, err := ValueFromZetaSQLValue(tc.sut)

			// Assert
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestIntervalValueFromZetaSQLBytes pins the bit-packing contract for
// the upstream 16-byte INTERVAL serialization
// (zetasql/public/interval_value.cc SerializeAndAppendToBytes):
//
//	bytes[0:8]   int64  micros        (LE, signed)
//	bytes[8:12]  int32  days          (LE, signed)
//	bytes[12:16] uint32 months_nanos  (LE; bit 31 = month sign,
//	                                   bits 13..30 = abs(months),
//	                                   bits 0..9 = nano_fractions 0..999)
//
// Cases triangulate two sign-groups (Y-M, H:M:S.nanos) and the
// nano_fractions edge: a positive interval mirroring an actual SQL
// CAST, a synthesized negative-micros + nano_fractions=999 to lock
// the signed-division shortcut (Go's `/` and `%` truncate toward
// zero), and a synthesized negative-months case to lock the Y-M sign
// path.
func TestIntervalValueFromZetaSQLBytes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sut     []byte
		want    *IntervalValue
		wantErr bool
	}{
		{
			name: "empty bytes return zero interval",
			sut:  []byte{},
			want: &IntervalValue{IntervalValue: &bigquery.IntervalValue{}},
		},
		{
			name:    "wrong length returns error",
			sut:     []byte{0x01, 0x02, 0x03},
			wantErr: true,
		},
		{
			// Mirrors CAST('1-2 3 18:1:55' AS INTERVAL):
			//   micros = 18*3600e6 + 1*60e6 + 55e6 = 64_915_000_000
			//   days   = 3
			//   months_nanos: abs(14) << 13 = 0x0001c000, sign+, nano=0
			name: "positive 1-2 3 18:1:55",
			sut: []byte{
				0xc0, 0x4a, 0x3c, 0x1d, 0x0f, 0x00, 0x00, 0x00,
				0x03, 0x00, 0x00, 0x00,
				0x00, 0xc0, 0x01, 0x00,
			},
			want: &IntervalValue{IntervalValue: &bigquery.IntervalValue{
				Years: 1, Months: 2, Days: 3,
				Hours: 18, Minutes: 1, Seconds: 55,
			}},
		},
		{
			// Regression-protects the signed-arithmetic shortcut:
			// micros=-1 (all 0xFF), nano_fractions=999 should yield
			// SubSecondNanos = -1 (= (-1 µs)*1000 ns/µs + 999 ns).
			// The earlier absMicros + timeSign formulation produced
			// -1999 here.
			name: "micros=-1 with nano_fractions=999 yields SubSecondNanos=-1",
			sut: []byte{
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
				0x00, 0x00, 0x00, 0x00,
				0xe7, 0x03, 0x00, 0x00,
			},
			want: &IntervalValue{IntervalValue: &bigquery.IntervalValue{SubSecondNanos: -1}},
		},
		{
			// Negative months: monthsAbs=13 in the packed bits with
			// sign bit set → months=-13 → Years=-1, Months=-1
			// (Y-M sign-group consistency falls out of Go's signed %).
			name: "months=-13 yields Years=-1 Months=-1",
			sut: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00,
				0x00, 0xa0, 0x01, 0x80,
			},
			want: &IntervalValue{IntervalValue: &bigquery.IntervalValue{Years: -1, Months: -1}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got, err := intervalValueFromZetaSQLBytes(tc.sut)

			// Assert
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
