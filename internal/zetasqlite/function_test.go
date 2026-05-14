package zetasqlite

import "testing"

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

		_, err := BIT_LEFT_SHIFT(IntValue(1), IntValue(-1))
		if err == nil {
			t.Fatal("expected error for negative offset")
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

		_, err := BIT_RIGHT_SHIFT(IntValue(8), IntValue(-1))
		if err == nil {
			t.Fatal("expected error for negative offset")
		}
		// Wording must stay byte-aligned with real BigQuery.
		if err.Error() != "Bitwise shift by negative offset." {
			t.Fatalf("unexpected error message: %q", err.Error())
		}
	})
}
