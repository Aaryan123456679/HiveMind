// Package rpc (this file): task-3.2.4's per-call latency/cost gRPC server interceptor
// (GitHub issue #16, Epic Phase 3). Pure observability layer -- it wraps handler
// invocation, measures wall-clock latency, records a best-effort payload byte-size figure,
// and forwards every request/response/error unchanged. It contains no business logic and
// never alters a handler's response semantics.
//
// Scope note: issue #16's task-3.2.4 acceptance criteria state "Every RPC call logs
// per-call latency on both sides, Python side additionally logs LLM token cost where
// applicable" (docs/LLD/rpc.md: "gRPC ... so both sides can attach interceptors logging
// per-call latency and (Python-side) LLM cost"). The Go engine side has no LLM calls, so
// "cost" as LLM-token-cost is a Python-only concept per the issue/LLD text -- it is not
// guessed at here. This file's required contribution is per-call latency. As a disclosed
// judgment call (not dictated by the issue text), RPCMetric also carries request/response
// proto payload byte sizes as the only notion of per-call "cost" that has real meaning on
// this pure-storage-engine Go side; it is clearly labeled as a payload-size figure, never
// conflated with the Python side's LLM-token cost.
//
// This file implements only the Go-side interceptor (engine/rpc/interceptor.go). The
// corresponding Python-side interceptor (agents/llm/interceptor.py or similar) is out of
// scope for this dispatch; see requirement.md under
// .cdr/runs/2026-07-09/008-implementation/ for the explicit scope-boundary justification.
package rpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// RPCMetric is a single per-call latency/cost record, in a stable typed shape a future
// benchmark harness (Epic 5, see docs/LLD/eval.md) can consume regardless of the concrete
// Recorder sink used to emit it.
type RPCMetric struct {
	// Method is the full gRPC method name, e.g. "/hivemind.v1.HiveMind/GetFile".
	Method string
	// Duration is the wall-clock time spent inside the handler (request received to
	// response/error returned), NOT including gRPC transport framing overhead.
	Duration time.Duration
	// RequestBytes is the wire-marshaled size of the request proto message, best-effort
	// (0 if the request is not a proto.Message, which should not happen for any
	// generated hivemindv1 request type).
	RequestBytes int
	// ResponseBytes is the wire-marshaled size of the response proto message, best-effort
	// (0 on error, since no response message exists in that case).
	ResponseBytes int
	// Code is the gRPC status code the call completed with (codes.OK on success).
	Code codes.Code
	// Err is the original handler error, if any (nil on success). Retained on the metric
	// for a Recorder that wants the underlying message, not just the code.
	Err error
}

// Recorder consumes RPCMetric records emitted by LatencyInterceptor. Implementations MUST
// be safe for concurrent use: LatencyInterceptor invokes Record from whichever goroutine
// the gRPC runtime is currently running the intercepted call on, and unary RPCs are served
// concurrently.
type Recorder interface {
	Record(RPCMetric)
}

// SlogRecorder is the default Recorder: it emits one structured log record per RPC call via
// log/slog, in a key=value (or JSON, depending on the configured slog.Handler) shape a
// benchmark harness can parse without a bespoke text format. log/slog's Logger is
// documented safe for concurrent use, so SlogRecorder needs no additional synchronization.
type SlogRecorder struct {
	logger *slog.Logger
}

// NewSlogRecorder returns a SlogRecorder backed by logger. A nil logger falls back to
// slog.Default().
func NewSlogRecorder(logger *slog.Logger) *SlogRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogRecorder{logger: logger}
}

// Record implements Recorder.
func (r *SlogRecorder) Record(m RPCMetric) {
	attrs := []any{
		slog.String("rpc_method", m.Method),
		slog.Duration("rpc_duration", m.Duration),
		slog.Int("rpc_request_bytes", m.RequestBytes),
		slog.Int("rpc_response_bytes", m.ResponseBytes),
		slog.String("rpc_code", m.Code.String()),
	}
	if m.Err != nil {
		r.logger.Warn("rpc_call", append(attrs, slog.String("rpc_error", m.Err.Error()))...)
		return
	}
	r.logger.Info("rpc_call", attrs...)
}

// Option configures LatencyInterceptor.
type Option func(*interceptorConfig)

type interceptorConfig struct {
	recorder Recorder
	now      func() time.Time
}

// WithRecorder overrides the Recorder every completed call's RPCMetric is sent to. The
// default (if unset) is NewSlogRecorder(nil).
func WithRecorder(rec Recorder) Option {
	return func(c *interceptorConfig) {
		c.recorder = rec
	}
}

// withNow overrides the clock function; test-only (unexported) seam, not part of the
// public API surface.
func withNow(now func() time.Time) Option {
	return func(c *interceptorConfig) {
		c.now = now
	}
}

// LatencyInterceptor returns a grpc.UnaryServerInterceptor that measures per-call
// wall-clock latency (and, as a disclosed proxy "cost" figure, request/response proto
// payload byte sizes) for every unary RPC, and forwards each resulting RPCMetric to the
// configured Recorder (default: SlogRecorder). It never alters the request, response, or
// error a wrapped handler produces -- it is purely an observability wrapper, wired into a
// *grpc.Server via grpc.UnaryInterceptor(rpc.LatencyInterceptor(...)) or
// grpc.ChainUnaryInterceptor(...).
func LatencyInterceptor(opts ...Option) grpc.UnaryServerInterceptor {
	cfg := &interceptorConfig{
		recorder: NewSlogRecorder(nil),
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := cfg.now()
		resp, err := handler(ctx, req)
		duration := cfg.now().Sub(start)

		m := RPCMetric{
			Method:       info.FullMethod,
			Duration:     duration,
			RequestBytes: protoSize(req),
			Code:         status.Code(err),
			Err:          err,
		}
		if err == nil {
			m.ResponseBytes = protoSize(resp)
		}
		cfg.recorder.Record(m)

		return resp, err
	}
}

// protoSize returns the wire-marshaled size of v if v is a proto.Message, or 0 otherwise
// (best-effort; every generated hivemindv1 request/response type is a proto.Message, so
// the 0 fallback is not expected to trigger in practice for this server's own RPCs).
func protoSize(v any) int {
	msg, ok := v.(proto.Message)
	if !ok || msg == nil {
		return 0
	}
	return proto.Size(msg)
}
