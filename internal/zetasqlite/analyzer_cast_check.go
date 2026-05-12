package zetasqlite

import (
	"fmt"
	"strconv"
	"strings"

	ast "github.com/glassmonkey/zetasql-wasm/resolved_ast"
	"github.com/glassmonkey/zetasql-wasm/types"
)

// rejectInvalidLiteralCast walks the resolved AST and rejects CAST nodes
// whose source is a STRING literal that cannot be parsed as the target
// type. zetasql-wasm v0.13.0's analyzer happily produces a resolved Cast
// for CAST("apple" AS INT64); without this gate the parse failure
// surfaces at execution time as a raw strconv error rather than the
// analyzer-shaped error BigQuery / ZetaSQL upstream emit.
//
// Scope is narrow on purpose: only STRING-literal sources are folded,
// only INT64 is checked (the kind covered by the regression test), and
// SAFE_CAST (ReturnNullOnError) is skipped because its contract is
// "return NULL on parse failure".
func rejectInvalidLiteralCast(root ast.Node, sql string) error {
	var failure error
	_ = ast.Walk(root, func(n ast.Node) error {
		if failure != nil {
			return nil
		}
		cast, ok := n.(*ast.CastNode)
		if !ok || cast.ReturnNullOnError() {
			return nil
		}
		lit, ok := cast.Expr().(*ast.LiteralNode)
		if !ok {
			return nil
		}
		src := types.WrapLiteralValue(lit.Value())
		if src == nil {
			return nil
		}
		s, ok := src.AsString()
		if !ok {
			return nil
		}
		target := types.WrapType(cast.Type())
		if target == nil {
			return nil
		}
		if target.Kind() != types.Int64 {
			return nil
		}
		if _, err := parseInt64Literal(s); err == nil {
			return nil
		}
		offset := int(lit.ParseLocationRange().GetStart())
		line, col := byteOffsetToLineColumn(sql, offset)
		failure = fmt.Errorf(
			`INVALID_ARGUMENT: Could not cast literal %q to type %s [at %d:%d]`,
			s, target.Kind(), line, col,
		)
		return nil
	})
	return failure
}

// parseInt64Literal mirrors the runtime path (StringValue.ToInt64): an
// empty string is treated as zero, and a "0x" prefix flips strconv into
// base-0 mode so hex literals like "0x87a" parse. The analyzer must use
// the same rule as the runtime so casts that DO succeed at runtime are
// not rejected here.
func parseInt64Literal(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	base := 10
	if strings.Contains(strings.ToLower(s), "0x") {
		base = 0
	}
	return strconv.ParseInt(s, base, 64)
}

// byteOffsetToLineColumn turns a 0-indexed byte offset into the 1-indexed
// line/column pair used by BigQuery's [at L:C] error suffix. Out-of-range
// offsets clamp to the start so the caller still gets a usable position.
func byteOffsetToLineColumn(sql string, offset int) (int, int) {
	if offset < 0 || offset > len(sql) {
		return 1, 1
	}
	line := 1
	lineStart := 0
	for i := 0; i < offset; i++ {
		if sql[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	return line, offset - lineStart + 1
}
