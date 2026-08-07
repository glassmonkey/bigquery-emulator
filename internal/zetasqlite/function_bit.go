package zetasqlite

import (
	"fmt"
	"math/bits"
)

func BIT_COUNT(v Value) (Value, error) {
	switch v.(type) {
	case BytesValue:
		b, err := v.ToBytes()
		if err != nil {
			return nil, err
		}
		var sum int64
		for _, vv := range b {
			sum += int64(bits.OnesCount8(vv))
		}
		return IntValue(sum), nil
	default:
		vv, err := v.ToInt64()
		if err != nil {
			return nil, err
		}
		return IntValue(bits.OnesCount64(uint64(vv))), nil
	}
}

// bitwiseBytes applies fn bytewise to two BYTES operands. BigQuery's &, |, ^
// require both BYTES to be the same length and raise a runtime error otherwise;
// the result is BYTES of that same length.
func bitwiseBytes(op string, a, b BytesValue, fn func(x, y byte) byte) (Value, error) {
	if len(a) != len(b) {
		return nil, fmt.Errorf("Bitwise binary operator %s requires equal-length bytes. Got %d bytes and %d bytes", op, len(a), len(b))
	}
	out := make([]byte, len(a))
	for i := range a {
		out[i] = fn(a[i], b[i])
	}
	return BytesValue(out), nil
}

// shiftBytesLeft shifts a big-endian BYTES value left by n bits. Bits shifted
// off the high end are discarded and vacated low bits are zero-filled; the
// result keeps the same length as the input, matching BigQuery's << on BYTES.
func shiftBytesLeft(b BytesValue, n int) BytesValue {
	out := make([]byte, len(b))
	byteShift := n / 8
	bitShift := uint(n % 8)
	for i := 0; i < len(b); i++ {
		src := i + byteShift
		if src >= len(b) {
			continue
		}
		v := b[src] << bitShift
		if bitShift > 0 && src+1 < len(b) {
			v |= b[src+1] >> (8 - bitShift)
		}
		out[i] = v
	}
	return BytesValue(out)
}

// shiftBytesRight is the >> counterpart of shiftBytesLeft: zero-filled from the
// high end, same length as the input.
func shiftBytesRight(b BytesValue, n int) BytesValue {
	out := make([]byte, len(b))
	byteShift := n / 8
	bitShift := uint(n % 8)
	for i := len(b) - 1; i >= 0; i-- {
		src := i - byteShift
		if src < 0 {
			continue
		}
		v := b[src] >> bitShift
		if bitShift > 0 && src-1 >= 0 {
			v |= b[src-1] << (8 - bitShift)
		}
		out[i] = v
	}
	return BytesValue(out)
}

func BIT_NOT(a Value) (Value, error) {
	if av, ok := a.(BytesValue); ok {
		out := make([]byte, len(av))
		for i, x := range av {
			out[i] = ^x
		}
		return BytesValue(out), nil
	}
	v, err := a.ToInt64()
	if err != nil {
		return nil, err
	}
	return IntValue(^v), nil
}

func BIT_LEFT_SHIFT(a, b Value) (Value, error) {
	vb, err := b.ToInt64()
	if err != nil {
		return nil, err
	}
	if vb < 0 {
		// Wording matches the real BigQuery error verbatim; keep aligned so callers
		// that switch on the BQ error string stay transparent. BIT_RIGHT_SHIFT below
		// shares the same invariant.
		return nil, fmt.Errorf("Bitwise shift by negative offset.")
	}
	if av, ok := a.(BytesValue); ok {
		return shiftBytesLeft(av, int(vb)), nil
	}
	va, err := a.ToInt64()
	if err != nil {
		return nil, err
	}
	return IntValue(va << vb), nil
}

func BIT_RIGHT_SHIFT(a, b Value) (Value, error) {
	vb, err := b.ToInt64()
	if err != nil {
		return nil, err
	}
	if vb < 0 {
		return nil, fmt.Errorf("Bitwise shift by negative offset.")
	}
	if av, ok := a.(BytesValue); ok {
		return shiftBytesRight(av, int(vb)), nil
	}
	va, err := a.ToInt64()
	if err != nil {
		return nil, err
	}
	// BigQuery's `>>` is a *logical* (unsigned) right shift — the upper bits
	// are zero-filled regardless of sign. Go's int64 `>>` is arithmetic
	// (signed) and sign-extends, so the negative-base result diverges from
	// BigQuery. Reinterpret as uint64 to match BigQuery semantics.
	return IntValue(int64(uint64(va) >> vb)), nil
}

func BIT_AND(a, b Value) (Value, error) {
	if av, ok := a.(BytesValue); ok {
		bv, ok := b.(BytesValue)
		if !ok {
			return nil, fmt.Errorf("BIT_AND: mismatched operand types %T and %T", a, b)
		}
		return bitwiseBytes("&", av, bv, func(x, y byte) byte { return x & y })
	}
	va, err := a.ToInt64()
	if err != nil {
		return nil, err
	}
	vb, err := b.ToInt64()
	if err != nil {
		return nil, err
	}
	return IntValue(va & vb), nil
}

func BIT_OR(a, b Value) (Value, error) {
	if av, ok := a.(BytesValue); ok {
		bv, ok := b.(BytesValue)
		if !ok {
			return nil, fmt.Errorf("BIT_OR: mismatched operand types %T and %T", a, b)
		}
		return bitwiseBytes("|", av, bv, func(x, y byte) byte { return x | y })
	}
	va, err := a.ToInt64()
	if err != nil {
		return nil, err
	}
	vb, err := b.ToInt64()
	if err != nil {
		return nil, err
	}
	return IntValue(va | vb), nil
}

func BIT_XOR(a, b Value) (Value, error) {
	if av, ok := a.(BytesValue); ok {
		bv, ok := b.(BytesValue)
		if !ok {
			return nil, fmt.Errorf("BIT_XOR: mismatched operand types %T and %T", a, b)
		}
		return bitwiseBytes("^", av, bv, func(x, y byte) byte { return x ^ y })
	}
	va, err := a.ToInt64()
	if err != nil {
		return nil, err
	}
	vb, err := b.ToInt64()
	if err != nil {
		return nil, err
	}
	return IntValue(va ^ vb), nil
}
