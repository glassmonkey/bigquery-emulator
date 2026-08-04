package zetasqlite

import (
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// makeStruct builds a *StructValue with the given field values. Field keys are
// auto-numbered ("0", "1", ...); the comparators under test (EQ / NOT_EQ /
// structEQ / *StructValue.EQ) walk the ordered values slice and ignore the
// keys, so the labels are immaterial. nil entries represent SQL NULL fields.
func makeStruct(values ...Value) *StructValue {
	keys := make([]string, len(values))
	for i := range values {
		keys[i] = strconv.Itoa(i)
	}
	return &StructValue{keys: keys, values: values}
}

// Test_Function_EQ_Struct exercises BigQuery STRUCT equality (`=`) with the
// three-valued logic documented at
// https://cloud.google.com/bigquery/docs/reference/standard-sql/operators#comparison_operators
//
// Cases are organised by the truth table axes: field count, NULL position,
// non-NULL field differences, and nesting. SQL-front-door coverage lives in
// TestQuery; this suite locks down the comparator contract directly,
// including branches (field-count mismatch) that the SQL type checker
// prevents from reaching the function through `=`.
func Test_Function_EQ_Struct(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		a    Value
		b    Value
		want Value
	}{
		{
			name: "all fields non-NULL and equal -> TRUE",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(1), IntValue(2)),
			want: BoolValue(true),
		},
		{
			name: "all fields non-NULL and a field differs -> FALSE",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(1), IntValue(3)),
			want: BoolValue(false),
		},
		{
			name: "both sides NULL at same position, other fields equal -> NULL",
			a:    makeStruct(IntValue(1), nil),
			b:    makeStruct(IntValue(1), nil),
			want: nil,
		},
		{
			name: "non-NULL fields differ wins over a NULL pair -> FALSE",
			a:    makeStruct(IntValue(1), nil),
			b:    makeStruct(IntValue(2), nil),
			want: BoolValue(false),
		},
		{
			name: "one side NULL where the other has a value, no other differences -> NULL",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(1), nil),
			want: nil,
		},
		{
			name: "one side NULL, but a separate non-NULL pair already differs -> FALSE",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(9), nil),
			want: BoolValue(false),
		},
		{
			name: "NULL at the leading field -> NULL",
			a:    makeStruct(nil, IntValue(2)),
			b:    makeStruct(nil, IntValue(2)),
			want: nil,
		},
		{
			name: "every field NULL on both sides -> NULL",
			a:    makeStruct(nil, nil),
			b:    makeStruct(nil, nil),
			want: nil,
		},
		{
			name: "field count mismatch -> FALSE (SQL type-checker would reject this, contract still defined)",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(1)),
			want: BoolValue(false),
		},
		{
			name: "single-field struct, equal -> TRUE",
			a:    makeStruct(IntValue(1)),
			b:    makeStruct(IntValue(1)),
			want: BoolValue(true),
		},
		{
			name: "three-field struct, last differs -> FALSE",
			a:    makeStruct(IntValue(1), IntValue(2), IntValue(3)),
			b:    makeStruct(IntValue(1), IntValue(2), IntValue(4)),
			want: BoolValue(false),
		},
		{
			name: "nested struct, inner all non-NULL equal -> TRUE",
			a:    makeStruct(makeStruct(IntValue(1), IntValue(2))),
			b:    makeStruct(makeStruct(IntValue(1), IntValue(2))),
			want: BoolValue(true),
		},
		{
			name: "nested struct, inner NULL field propagates -> NULL",
			a:    makeStruct(makeStruct(IntValue(1), nil)),
			b:    makeStruct(makeStruct(IntValue(1), nil)),
			want: nil,
		},
		{
			name: "nested struct, inner non-NULL differs -> FALSE",
			a:    makeStruct(makeStruct(IntValue(1), IntValue(2))),
			b:    makeStruct(makeStruct(IntValue(1), IntValue(3))),
			want: BoolValue(false),
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := EQ(tt.a, tt.b)
			if err != nil {
				t.Fatalf("EQ returned unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("EQ result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Test_Function_NOT_EQ_Struct verifies that NOT_EQ inverts EQ in BigQuery's
// three-valued logic: NOT NULL = NULL, NOT TRUE = FALSE, NOT FALSE = TRUE.
// Only struct inputs are covered here because the non-struct cases are the
// existing trivial Value.EQ contract and are already covered through TestQuery.
func Test_Function_NOT_EQ_Struct(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		a    Value
		b    Value
		want Value
	}{
		{
			name: "EQ true -> NOT_EQ false",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(1), IntValue(2)),
			want: BoolValue(false),
		},
		{
			name: "EQ false -> NOT_EQ true",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(1), IntValue(3)),
			want: BoolValue(true),
		},
		{
			name: "EQ NULL (NULL field) -> NOT_EQ NULL",
			a:    makeStruct(IntValue(1), nil),
			b:    makeStruct(IntValue(1), nil),
			want: nil,
		},
		{
			name: "EQ NULL (one side NULL) -> NOT_EQ NULL",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(1), nil),
			want: nil,
		},
		{
			name: "field count mismatch -> NOT_EQ TRUE",
			a:    makeStruct(IntValue(1), IntValue(2)),
			b:    makeStruct(IntValue(1)),
			want: BoolValue(true),
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NOT_EQ(tt.a, tt.b)
			if err != nil {
				t.Fatalf("NOT_EQ returned unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("NOT_EQ result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func Test_Function_BIT_LEFT_SHIFT(t *testing.T) {
	t.Parallel()

	t.Run("shift positive offset", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_LEFT_SHIFT(IntValue(1), IntValue(2))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		if v != 4 {
			t.Fatalf("unexpected result: got=%d want=%d", v, 4)
		}
	})

	t.Run("shift positive offset (triangulation)", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_LEFT_SHIFT(IntValue(3), IntValue(3))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		if v != 24 {
			t.Fatalf("unexpected result: got=%d want=%d", v, 24)
		}
	})

	t.Run("shift by zero is identity", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_LEFT_SHIFT(IntValue(7), IntValue(0))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		if v != 7 {
			t.Fatalf("unexpected result: got=%d want=%d", v, 7)
		}
	})

	t.Run("negative offset returns BigQuery-aligned error", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_LEFT_SHIFT(IntValue(1), IntValue(-1))
		if err == nil {
			t.Fatal("expected error for negative offset")
		}
		if got != nil {
			t.Fatalf("expected nil value on error, got: %v", got)
		}
		// Wording must stay byte-aligned with real BigQuery.
		if err.Error() != "Bitwise shift by negative offset." {
			t.Fatalf("unexpected error message: %q", err.Error())
		}
	})
}

func Test_Function_BIT_RIGHT_SHIFT(t *testing.T) {
	t.Parallel()

	t.Run("shift positive offset", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_RIGHT_SHIFT(IntValue(8), IntValue(1))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		if v != 4 {
			t.Fatalf("unexpected result: got=%d want=%d", v, 4)
		}
	})

	t.Run("shift positive offset (triangulation)", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_RIGHT_SHIFT(IntValue(15), IntValue(2))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		if v != 3 {
			t.Fatalf("unexpected result: got=%d want=%d", v, 3)
		}
	})

	t.Run("shift by zero is identity", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_RIGHT_SHIFT(IntValue(15), IntValue(0))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		if v != 15 {
			t.Fatalf("unexpected result: got=%d want=%d", v, 15)
		}
	})

	t.Run("negative offset returns BigQuery-aligned error", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_RIGHT_SHIFT(IntValue(8), IntValue(-1))
		if err == nil {
			t.Fatal("expected error for negative offset")
		}
		if got != nil {
			t.Fatalf("expected nil value on error, got: %v", got)
		}
		// Wording must stay byte-aligned with real BigQuery.
		if err.Error() != "Bitwise shift by negative offset." {
			t.Fatalf("unexpected error message: %q", err.Error())
		}
	})
}
