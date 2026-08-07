package zetasqlite

import (
	"bytes"
	"testing"
)

// Regression tests for bitwise operators applied to BYTES. Previously the
// emulator accepted queries like `SELECT b'\x0f' & b'\x03'` at analysis time
// but crashed at result decode with `strconv.ParseInt: parsing "\x0f"` because
// BIT_AND/OR/XOR/NOT/LEFT_SHIFT/RIGHT_SHIFT unconditionally called ToInt64().
// BigQuery applies these bytewise on equal-length BYTES and returns BYTES of
// the same length; shifts keep the operand length and zero-fill vacated bits.
func Test_Function_BIT_Bytes(t *testing.T) {
	t.Parallel()

	t.Run("binary ops (&, |, ^)", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name string
			fn   func(a, b Value) (Value, error)
			a    BytesValue
			b    BytesValue
			want BytesValue
		}{
			{"AND", BIT_AND, BytesValue{0x0f}, BytesValue{0x03}, BytesValue{0x03}},
			{"OR", BIT_OR, BytesValue{0x01}, BytesValue{0x02}, BytesValue{0x03}},
			{"XOR", BIT_XOR, BytesValue{0xff}, BytesValue{0x0f}, BytesValue{0xf0}},
			{"AND multi-byte", BIT_AND, BytesValue{0xff, 0x0f}, BytesValue{0x0f, 0xff}, BytesValue{0x0f, 0x0f}},
		}
		for _, tt := range cases {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				got, err := tt.fn(tt.a, tt.b)
				if err != nil {
					t.Fatal(err)
				}
				b, err := got.ToBytes()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(b, tt.want) {
					t.Fatalf("unexpected result: got=%x want=%x", b, tt.want)
				}
			})
		}
	})

	t.Run("NOT complements each byte", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_NOT(BytesValue{0x00, 0x0f})
		if err != nil {
			t.Fatal(err)
		}
		b, err := got.ToBytes()
		if err != nil {
			t.Fatal(err)
		}
		if want := []byte{0xff, 0xf0}; !bytes.Equal(b, want) {
			t.Fatalf("unexpected result: got=%x want=%x", b, want)
		}
	})

	t.Run("shifts keep length and cross byte boundaries", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name string
			fn   func(a, b Value) (Value, error)
			a    BytesValue
			n    int64
			want BytesValue
		}{
			{"<< single byte", BIT_LEFT_SHIFT, BytesValue{0x0f}, 4, BytesValue{0xf0}},
			{"<< across bytes", BIT_LEFT_SHIFT, BytesValue{0x01, 0x02}, 4, BytesValue{0x10, 0x20}},
			{">> single byte", BIT_RIGHT_SHIFT, BytesValue{0xf0}, 4, BytesValue{0x0f}},
			{">> across bytes", BIT_RIGHT_SHIFT, BytesValue{0x10, 0x20}, 4, BytesValue{0x01, 0x02}},
			{"<< beyond length zero-fills", BIT_LEFT_SHIFT, BytesValue{0x0f}, 16, BytesValue{0x00}},
		}
		for _, tt := range cases {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				got, err := tt.fn(tt.a, IntValue(tt.n))
				if err != nil {
					t.Fatal(err)
				}
				b, err := got.ToBytes()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(b, tt.want) {
					t.Fatalf("unexpected result: got=%x want=%x", b, tt.want)
				}
			})
		}
	})

	t.Run("unequal-length operands are rejected", func(t *testing.T) {
		t.Parallel()

		if _, err := BIT_AND(BytesValue{0x01}, BytesValue{0x01, 0x02}); err == nil {
			t.Fatal("expected error for unequal-length BYTES, got nil")
		}
	})

	t.Run("INT64 path is unchanged (regression guard)", func(t *testing.T) {
		t.Parallel()

		got, err := BIT_AND(IntValue(5), IntValue(3))
		if err != nil {
			t.Fatal(err)
		}
		v, err := got.ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		if v != 1 {
			t.Fatalf("unexpected result: got=%d want=1", v)
		}
	})
}

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
