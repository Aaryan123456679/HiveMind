package split

// SplitProposer decouples engine/split's split logic from whatever transport actually
// produces a proposed split of a file's content. Today there is no real implementation:
// the gRPC-backed ProposeSplit call to the ingestion agent (see agents/ingestion) lands in
// a later epic, once proto/ carries generated Go stubs and agents/ingestion exposes a
// callable service. Until then, engine/split depends only on this local interface, never
// on a gRPC client package directly -- see 2b.2.2 for a deterministic fixture-backed mock
// that unblocks split-sequence testing in the meantime.
//
// SplitProposer intentionally says nothing about how fileID/redirect-target allocation,
// content/stub file writes, B+Tree repointing, or graph edges happen: per
// engine/split/orchestrate.go's documented scope boundary (2b.1.3), all of that is issue
// #12's ("Atomic split-transaction execution") job, executed against the SplitPlan this
// interface returns. SplitProposer only proposes; it never mutates anything itself.
type SplitProposer interface {
	// ProposeSplit inspects fileContent and returns a proposed SplitPlan describing how
	// the content could be divided into multiple new files, plus a human-readable summary
	// suitable for a redirect stub. ProposeSplit must not mutate fileContent.
	//
	// A non-nil error indicates the proposal could not be produced (e.g. a transport
	// failure once a real gRPC-backed implementation exists); callers must not act on
	// SplitPlan when err is non-nil.
	ProposeSplit(fileContent []byte) (SplitPlan, error)
}

// SplitPlan is a proposed (not yet executed) split of a file's content. It carries no real
// fileIDs and performs no allocation: issue #12's execution logic is responsible for
// turning a SplitPlan into actual new content/stub files, redirect-target fileIDs
// (CatalogRecord.RedirectTargetIDs), B+Tree repointing, and graph edges.
type SplitPlan struct {
	// Files is the proposed set of new files the original content should be divided into.
	Files []SplitFileProposal

	// RedirectSummary is a short, human-readable description of the intended redirect
	// (e.g. for use as a stub-file body or log message once issue #12 executes the
	// split). It is deliberately just a string, not a structured redirect model: actual
	// redirect-target bookkeeping is issue #12's job against catalog.CatalogRecord, not
	// this proposal type's.
	RedirectSummary string
}

// SplitFileProposal is one proposed new logical file within a SplitPlan.
type SplitFileProposal struct {
	// NewPath is a proposal-time, human-readable logical path for the new file. It is not
	// a real fileID: issue #12 owns actual fileID allocation for whatever new file this
	// proposal becomes.
	NewPath string

	// SectionRanges lists the byte-offset ranges of the original fileContent that make up
	// this new file, in order. Supporting multiple, possibly non-contiguous ranges allows
	// a single proposed file to be assembled from several parts of the original content
	// (e.g. a shared header plus a body section).
	SectionRanges []SectionRange
}

// SectionRange is a half-open byte-offset range [Start, End) into an original
// fileContent slice, following ordinary Go slice-indexing convention.
type SectionRange struct {
	Start int
	End   int
}
