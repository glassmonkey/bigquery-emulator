// Package tracing wraps an OpenTelemetry tracer in the same
// context-injected style as internal/logger so handler and
// library code can pull a tracer out of ctx and Start spans
// without having to know whether tracing is configured.
//
// The HTTP middleware injects a per-server tracer via WithTracer.
// Code that runs outside the HTTP path (zetasqlite internals,
// unit tests, library-mode embedders that skip the middleware)
// gets a no-op tracer from FromContext, so Start never panics
// or returns nil.
package tracing

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type tracerKey struct{}

var noopTracer = noop.NewTracerProvider().Tracer("")

// FromContext returns the tracer attached to ctx via WithTracer,
// or a no-op tracer if none is set.
func FromContext(ctx context.Context) trace.Tracer {
	if t, ok := ctx.Value(tracerKey{}).(trace.Tracer); ok && t != nil {
		return t
	}
	return noopTracer
}

// WithTracer attaches t to ctx. The HTTP middleware calls this
// once at the request boundary.
func WithTracer(ctx context.Context, t trace.Tracer) context.Context {
	return context.WithValue(ctx, tracerKey{}, t)
}

// EndFunc closes the span opened by Start. Pass a pointer to a
// named-return error to record it on the span; pass nil for
// void code paths.
type EndFunc func(errPtr *error)

// Start opens a span named `name` from the tracer in ctx and
// returns the child ctx together with an EndFunc that records
// the error (if non-nil) and ends the span. The intended use is
//
//	func (s *Server) doThing(ctx context.Context, ...) (err error) {
//	    ctx, end := tracing.Start(ctx, "server.doThing")
//	    defer end(&err)
//	    // ...
//	}
//
// `defer end(&err)` captures whatever the function ends up
// returning; passing nil records nothing and just ends the span.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, EndFunc) {
	ctx, span := FromContext(ctx).Start(ctx, name, opts...)
	return ctx, func(errPtr *error) {
		if errPtr != nil && *errPtr != nil {
			span.RecordError(*errPtr)
			span.SetStatus(codes.Error, (*errPtr).Error())
		}
		span.End()
	}
}
