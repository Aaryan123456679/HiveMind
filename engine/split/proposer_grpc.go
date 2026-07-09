// Package split (this file): task-3.2.3's real gRPC-backed SplitProposer implementation
// (GitHub issue #16, Epic Phase 3). GRPCSplitProposer implements the split.SplitProposer
// interface (proposer.go) by calling the real hivemindv1.HiveMindClient.ProposeSplit RPC
// (proto/hivemind.proto, generated engine/rpc/gen/) against the Python ingestion agent
// service, replacing task-2b.2.2's MockSplitProposer for non-test callers.
//
// Scope: this file is CLIENT-side transport wiring only -- request marshaling, response
// unmarshaling, and error/timeout handling around a real gRPC call. It intentionally does
// not implement (and never will implement) the ProposeSplit *server*: that is Python
// agents/ingestion/'s job (see docs/LLD/rpc.md, docs/LLD/ingestion-agent.md), out of scope
// for this subtask and tracked separately (issue #18's "real LLM client integration").
// engine/rpc/server.go's own ProposeSplit handler deliberately remains the generated
// UnimplementedHiveMindServer default; this file never talks to that handler in production
// use (it dials whatever Python agent service is configured), and calling it against
// today's Go server would correctly surface codes.Unimplemented as an error via
// GRPCSplitProposer.ProposeSplit, not a panic or silent success.
//
// proposer_mock.go (MockSplitProposer, issue #11/2b.2.2) is left untouched by this file --
// it remains the pure-unit-test double; GRPCSplitProposer is purely additive, a second
// implementation of the same split.SplitProposer interface.
package split

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/status"

	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// GRPCSplitProposer implements SplitProposer by calling the real ProposeSplit RPC over an
// already-established gRPC client connection. It owns none of the underlying connection's
// lifecycle: callers construct (and later close) the grpc.ClientConn used to build the
// hivemindv1.HiveMindClient passed to NewGRPCSplitProposer, mirroring the "does not own its
// dependencies" convention established by engine/rpc.NewServer.
type GRPCSplitProposer struct {
	// client is the generated gRPC client stub used to issue ProposeSplit calls. It is
	// never nil for a GRPCSplitProposer constructed via NewGRPCSplitProposer.
	client hivemindv1.HiveMindClient

	// timeout bounds each individual ProposeSplit call's wall-clock duration via
	// context.WithTimeout. A zero or negative value means no client-imposed deadline is
	// added -- the call is bounded only by whatever ambient deadline (if any) a future
	// context-aware SplitProposer signature might carry, or by the server/transport's own
	// behavior. See ProposeSplit's doc comment: SplitProposer's interface signature takes
	// no context.Context (proposer.go, task-2b.2.1's decoupling design), so this timeout is
	// the only per-call deadline control available at this layer.
	timeout time.Duration
}

// compile-time assertion: GRPCSplitProposer must satisfy SplitProposer.
var _ SplitProposer = (*GRPCSplitProposer)(nil)

// NewGRPCSplitProposer returns a GRPCSplitProposer that issues ProposeSplit RPCs via client,
// each bounded by timeout (see GRPCSplitProposer.timeout's doc comment for the timeout<=0
// case). client must be non-nil; NewGRPCSplitProposer does not validate this itself (mirrors
// engine/rpc.NewServer's convention of trusting constructor-time arguments from
// already-validated callers), but a nil client will cause ProposeSplit to panic on first
// call, so callers must never pass nil.
func NewGRPCSplitProposer(client hivemindv1.HiveMindClient, timeout time.Duration) *GRPCSplitProposer {
	return &GRPCSplitProposer{client: client, timeout: timeout}
}

// ProposeSplit implements SplitProposer. It marshals fileContent into a
// hivemindv1.ProposeSplitRequest, issues the RPC via p.client, and unmarshals a successful
// hivemindv1.ProposeSplitResponse into a SplitPlan. It never mutates fileContent (the
// generated proto request type copies the byte slice into its own message state via the
// gRPC codec; ProposeSplit itself performs no in-place mutation either way).
//
// A non-nil error (transport failure, deadline exceeded, or a non-OK gRPC status returned by
// the server -- including codes.Unimplemented, which is what today's engine/rpc/server.go
// still returns for ProposeSplit, since the real LLM-backed server is issue #18's job) always
// comes paired with a zero SplitPlan; callers must not act on the returned SplitPlan in that
// case, per SplitProposer's documented contract.
func (p *GRPCSplitProposer) ProposeSplit(fileContent []byte) (SplitPlan, error) {
	ctx := context.Background()
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	resp, err := p.client.ProposeSplit(ctx, &hivemindv1.ProposeSplitRequest{FileContent: fileContent})
	if err != nil {
		return SplitPlan{}, fmt.Errorf("split: GRPCSplitProposer.ProposeSplit: RPC failed (%s): %w", status.Code(err), err)
	}

	return translateProposeSplitResponse(resp), nil
}

// translateProposeSplitResponse converts a wire-format hivemindv1.ProposeSplitResponse into
// the internal SplitPlan type ProposeSplit's callers expect, following
// proto/hivemind.proto's documented field-for-field correspondence with
// SplitPlan/SplitFileProposal/SectionRange.
func translateProposeSplitResponse(resp *hivemindv1.ProposeSplitResponse) SplitPlan {
	plan := SplitPlan{
		Files:           make([]SplitFileProposal, 0, len(resp.GetFiles())),
		RedirectSummary: resp.GetRedirectSummary(),
	}

	for _, f := range resp.GetFiles() {
		if f == nil {
			continue
		}
		proposal := SplitFileProposal{
			NewPath:       f.GetNewPath(),
			SectionRanges: make([]SectionRange, 0, len(f.GetSectionRanges())),
		}
		for _, r := range f.GetSectionRanges() {
			if r == nil {
				continue
			}
			// Start/End are int64 on the wire (proto3 has no native platform-sized int
			// type) but int internally (SplitPlan predates any RPC transport, see
			// proposer.go). The conversion below is safe for every offset this engine
			// can actually produce: SectionRange bounds a single in-memory fileContent
			// slice, whose length is itself bounded by Go's int (== platform word
			// size), so any Start/End value that ever legitimately reaches the wire
			// already fit in int on the sending side. The only theoretical truncation
			// risk is a 32-bit build target receiving a >2^31 offset from a 64-bit
			// peer; this engine does not target 32-bit platforms (see AGENT.md build
			// conventions), so that case is flagged here rather than defended against.
			proposal.SectionRanges = append(proposal.SectionRanges, SectionRange{
				Start: int(r.GetStart()),
				End:   int(r.GetEnd()),
			})
		}
		plan.Files = append(plan.Files, proposal)
	}

	return plan
}
