// Package queryclient (this file): GitHub issue #56 subtask 4.6.3.2's real gRPC client
// implementation of api/routes.QueryPipeline, replacing api/main.go's notImplementedPipeline
// stand-in (F-4.6.1-1).
//
// api/routes/query.go's QueryPipeline doc comment disclosed the gap this closes:
// proto/hivemind.proto's `service HiveMind` had no RPC for invoking the Python query
// pipeline (agents/query/pipeline.py's run_query_pipeline). Subtask 4.6.3.2 added `RunQuery`
// to that contract, with direction reversed from most of HiveMind's RPCs -- mirroring
// ProposeSplit (engine/split/proposer_grpc.go): Go is the CLIENT here, Python is the SERVER
// (agents/query/server.py, a new grpc.Server exposing exactly this one RPC). GRPCQueryPipeline
// below is the client-side half, structured identically to GRPCSplitProposer:
//   - it owns none of its dependencies' lifecycles (does not open/close the grpc.ClientConn
//     the caller builds hivemindv1.HiveMindClient from);
//   - a caller-supplied per-call timeout via context.WithTimeout, since RunQuery's RunQuery
//     signature (unlike SplitProposer's) already takes a context.Context, so no additional
//     context-less-interface constraint applies here;
//   - errors are wrapped with %w so callers can still inspect the underlying gRPC
//     status/code via status.Code(err) or errors.Is/As.
package queryclient

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/status"

	"github.com/Aaryan123456679/HiveMind/api/routes"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// GRPCQueryPipeline implements routes.QueryPipeline by calling the real RunQuery RPC over an
// already-established gRPC client connection to a running agents/query/server.py process.
type GRPCQueryPipeline struct {
	// client is the generated gRPC client stub used to issue RunQuery calls. Must never be
	// nil for a GRPCQueryPipeline constructed via NewGRPCQueryPipeline.
	client hivemindv1.HiveMindClient

	// timeout bounds each individual RunQuery call's wall-clock duration via
	// context.WithTimeout, added on top of whatever ambient deadline the caller's own
	// context.Context (routes.QueryPipeline.RunQuery's ctx parameter) may already carry. A
	// zero or negative value means no additional client-imposed deadline is added.
	timeout time.Duration
}

// compile-time assertion: GRPCQueryPipeline must satisfy routes.QueryPipeline.
var _ routes.QueryPipeline = (*GRPCQueryPipeline)(nil)

// NewGRPCQueryPipeline returns a GRPCQueryPipeline that issues RunQuery RPCs via client, each
// bounded by timeout (see GRPCQueryPipeline.timeout's doc comment for the timeout<=0 case).
// client must be non-nil; NewGRPCQueryPipeline does not validate this itself (mirrors
// engine/split.NewGRPCSplitProposer's convention of trusting constructor-time arguments from
// already-validated callers), but a nil client will cause RunQuery to panic on first call.
func NewGRPCQueryPipeline(client hivemindv1.HiveMindClient, timeout time.Duration) *GRPCQueryPipeline {
	return &GRPCQueryPipeline{client: client, timeout: timeout}
}

// RunQuery implements routes.QueryPipeline. It marshals query/history into a
// hivemindv1.RunQueryRequest, issues the RPC via p.client, and unmarshals a successful
// hivemindv1.RunQueryResponse into a routes.QueryResult.
//
// A non-nil error (transport failure, deadline exceeded, or a non-OK gRPC status returned by
// the server -- including codes.Unimplemented, which is what a bare
// hivemindv1.UnimplementedHiveMindServer embedder would return, i.e. what today's
// engine/rpc/server.go itself still returns for RunQuery, since that RPC's real
// implementation lives in agents/query/server.py, not the Go engine) always comes paired
// with a zero routes.QueryResult; api/routes.NewQueryHandler already treats any non-nil
// error as a 500, so no special-casing is needed at this layer.
func (p *GRPCQueryPipeline) RunQuery(ctx context.Context, query string, history []string) (routes.QueryResult, error) {
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	resp, err := p.client.RunQuery(ctx, &hivemindv1.RunQueryRequest{
		Query:   query,
		History: history,
	})
	if err != nil {
		return routes.QueryResult{}, fmt.Errorf("queryclient: GRPCQueryPipeline.RunQuery: RPC failed (%s): %w", status.Code(err), err)
	}

	return routes.QueryResult{
		Answer:    resp.GetAnswer(),
		Citations: resp.GetCitations(),
	}, nil
}
