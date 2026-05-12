package zetasqlite

import (
	"errors"
	"strings"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

// unwrapSQLiteErr peels off ncruces's "sqlite3: SQL logic error: "
// wrapper from UDF and constraint errors so callers see the bare
// message the fork's UDFs produced. mattn surfaced bare messages; 30+
// table-driven tests in query_test.go compare error strings by exact
// equality, so preserving the bare form is a behavior parity
// requirement, not cosmetic. Other ncruces error codes (BUSY, IOERR,
// ...) are returned unchanged so diagnostic context for non-UDF
// failures is not silently lost.
func unwrapSQLiteErr(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite3.Error
	if !errors.As(err, &sqliteErr) {
		return err
	}
	const prefix = "sqlite3: SQL logic error: "
	if msg, ok := strings.CutPrefix(err.Error(), prefix); ok {
		return errors.New(msg)
	}
	return err
}

// valueToInterface normalises an ncruces Value into the {int64, float64,
// bool, string, nil} shape that DecodeValue understands. The previous
// mattn driver did this conversion implicitly via reflection; ncruces
// hands us the raw Value and leaves the choice to us. Encoded
// complex values (NUMERIC, dates, structs, arrays, ...) are stored as
// base64 TEXT by EncodeValue, so BLOB should not occur on this path —
// if it ever does, decode as UTF-8 text and let DecodeValue surface the
// error.
func valueToInterface(v sqlite3.Value) interface{} {
	switch v.Type() {
	case sqlite3.NULL:
		return nil
	case sqlite3.INTEGER:
		return v.Int64()
	case sqlite3.FLOAT:
		return v.Float()
	case sqlite3.TEXT:
		return v.Text()
	case sqlite3.BLOB:
		return string(v.RawBlob())
	}
	return nil
}

// setContextResult routes EncodeValue's output ({int64, float64, bool,
// string, nil}) onto the matching ncruces Context.Result* setter.
func setContextResult(ctx sqlite3.Context, ret interface{}) {
	switch r := ret.(type) {
	case nil:
		ctx.ResultNull()
	case int64:
		ctx.ResultInt64(r)
	case float64:
		ctx.ResultFloat(r)
	case bool:
		ctx.ResultBool(r)
	case string:
		ctx.ResultText(r)
	case []byte:
		ctx.ResultBlob(r)
	default:
		ctx.ResultNull()
	}
}

// adaptScalar bridges fork-side `func(args ...interface{}) (interface{}, error)`
// — the shape produced by setupNormalFuncMap and the inline helpers in
// RegisterFunctions — onto ncruces's ScalarFunction signature.
func adaptScalar(fn SQLiteFunction) sqlite3.ScalarFunction {
	return func(ctx sqlite3.Context, args ...sqlite3.Value) {
		ifaces := make([]interface{}, len(args))
		for i, a := range args {
			ifaces[i] = valueToInterface(a)
		}
		ret, err := fn(ifaces...)
		if err != nil {
			ctx.ResultError(err)
			return
		}
		setContextResult(ctx, ret)
	}
}

// aggregatorAdapter wraps fork's *Aggregator so that ncruces's
// AggregateFunction interface — Step(ctx, args) and Value(ctx) — calls
// land on the Step/Done methods the fork already implements. ncruces
// invokes Step per row in the group and Value at the end; that matches
// the mattn lifecycle the fork was written against.
type aggregatorAdapter struct {
	inner *Aggregator
}

func (a *aggregatorAdapter) Step(ctx sqlite3.Context, args ...sqlite3.Value) {
	ifaces := make([]interface{}, len(args))
	for i, v := range args {
		ifaces[i] = valueToInterface(v)
	}
	if err := a.inner.Step(ifaces...); err != nil {
		ctx.ResultError(err)
	}
}

func (a *aggregatorAdapter) Value(ctx sqlite3.Context) {
	ret, err := a.inner.Done()
	if err != nil {
		ctx.ResultError(err)
		return
	}
	setContextResult(ctx, ret)
}

func adaptAggregator(ctor func() *Aggregator) sqlite3.AggregateConstructor {
	return func() sqlite3.AggregateFunction {
		return &aggregatorAdapter{inner: ctor()}
	}
}

// windowAggregatorAdapter is the same shape as aggregatorAdapter but
// for fork's *WindowAggregator. Both fork types expose Step/Done; the
// inner state (sliding-window vs plain-group) is hidden behind the
// shared method set.
type windowAggregatorAdapter struct {
	inner *WindowAggregator
}

func (a *windowAggregatorAdapter) Step(ctx sqlite3.Context, args ...sqlite3.Value) {
	ifaces := make([]interface{}, len(args))
	for i, v := range args {
		ifaces[i] = valueToInterface(v)
	}
	if err := a.inner.Step(ifaces...); err != nil {
		ctx.ResultError(err)
	}
}

func (a *windowAggregatorAdapter) Value(ctx sqlite3.Context) {
	ret, err := a.inner.Done()
	if err != nil {
		ctx.ResultError(err)
		return
	}
	setContextResult(ctx, ret)
}

func adaptWindowAggregator(ctor func() *WindowAggregator) sqlite3.AggregateConstructor {
	return func() sqlite3.AggregateFunction {
		return &windowAggregatorAdapter{inner: ctor()}
	}
}

// adaptCollation lifts a string-comparing collation onto ncruces's
// []byte-comparing CollatingFunction. The fork compares decoded
// ValueLayouts that were base64-encoded into strings, so a UTF-8 round
// trip through []byte is lossless.
func adaptCollation(fn func(a, b string) int) sqlite3.CollatingFunction {
	return func(a, b []byte) int {
		return fn(string(a), string(b))
	}
}
