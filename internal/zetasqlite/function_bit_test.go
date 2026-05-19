package zetasqlite

import "testing"

// Regression tests for https://github.com/glassmonkey/bigquery-emulator/issues/113
//
// BigQuery's `>>` is a *logical* (unsigned) right shift — the upper bits are
// zero-filled regardless of sign. Go's int64 `>>` is arithmetic (signed) and
// sign-extends. Expected values are byte-verified against the real BigQuery
// API (project `sandbox-248114`).
func Test_Function_BIT_RIGHT_SHIFT_NegativeBase(t *testing.T) {
	t.Parallel()

	t.Run("small negative base", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_RIGHT_SHIFT(IntValue(-8), IntValue(2))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		const want = int64(4611686018427387902)
		if v != want {
			t.Fatalf("unexpected result: got=%d want=%d", v, want)
		}
	})

	t.Run("-1 fills upper bits with zeros", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_RIGHT_SHIFT(IntValue(-1), IntValue(1))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		const want = int64(9223372036854775807) // INT64 max
		if v != want {
			t.Fatalf("unexpected result: got=%d want=%d", v, want)
		}
	})

	t.Run("INT64 min", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_RIGHT_SHIFT(IntValue(-9223372036854775808), IntValue(1))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		const want = int64(4611686018427387904)
		if v != want {
			t.Fatalf("unexpected result: got=%d want=%d", v, want)
		}
	})

	t.Run("positive base remains arithmetic-equivalent (regression guard)", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_RIGHT_SHIFT(IntValue(8), IntValue(2))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		if v != 2 {
			t.Fatalf("unexpected result: got=%d want=%d", v, 2)
		}
	})
}
