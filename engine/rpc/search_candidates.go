// Package rpc (this file): task-4.2.1's real term-overlap ranking for SearchCandidates
// (GitHub issue #21, Epic Phase 4). task-3.2.2 (issue #16, Phase 3) wired
// SearchCandidates's handler in server.go to engine/btree.PrefixScan but deliberately left
// every result's score as a constant placeholder (see that file's prior
// searchCandidateScore doc comment, removed by this change) -- "inventing [a ranking
// algorithm] here would be new business logic outside [3.2.2's] thin-adapter scope." This
// subtask is that deferred ranking algorithm.
//
// Scope: engine/btree exposes no document/topic-content text, only the (path, fileID)
// pairs PrefixScan returns (see engine/btree/scan.go's ScanEntry) -- candidates are keyed
// by file path, not arbitrary content. "Simple term-overlap ranking" per the issue's own
// wording therefore means overlap between the query's tokenized terms and the candidate
// path's own tokenized terms (its directory + filename components); there is no other text
// available to rank against at this layer. This mirrors the tokenization convention
// agents/ingestion/shortlist.py (task-3.4.2) already uses for its own (separate, local,
// BM25-based) ranking of the same kind of path strings -- kept intentionally consistent
// even though the two are independent implementations across the Go/Python boundary.
//
// Division of labor, preserved from task-3.4.2's already-documented design: this file's
// ranking is a *complement* to, not a replacement for, agents/ingestion/shortlist.py's
// local BM25 re-ranking -- that module calls SearchCandidates with query="" (empty
// prefix, matching every stored key) purely to bound its pool via max_results, then
// re-ranks locally against real document content, which this RPC still has no access to.
// This file's ranking activates only when the caller supplies actual query terms (e.g. a
// query-time topic-selector calling SearchCandidates directly, per the issue's "suitable
// for ... query-time topic selection" acceptance criterion); an empty query is defined to
// be a ranking no-op (every candidate scores 0, so a stable sort leaves PrefixScan's own
// sorted-path order untouched), which is exactly what shortlist()'s existing empty-query
// pool-retrieval usage already depends on.
//
// Task 4.5.9.2 (issue #47) update: candidatePool (below) replaced the original
// single-first-token PrefixScan pool selection with a per-query-term PrefixScan-and-merge
// strategy, per the decision recorded in docs/LLD/query-agent.md / docs/LLD/btree.md's
// "Known risks" (subtask 4.5.9.1). rankCandidates itself is unchanged by this update.
//
// Fix-cycle update (issue #47, subtask 4.5.9.2, CHANGES_REQUESTED re-fix, run
// 2026-07-11/110-verification): the initial 4.5.9.2 implementation's perTermPoolCap/
// mergedPoolCap bounded only RETAINED pool size, not scan COST -- btree.PrefixScan (see
// engine/btree/scan.go) already runs its full leaf-chain traversal and returns every
// matching entry before candidatePool ever gets to truncate the result, so those two caps
// did nothing to bound the number/cost of PrefixScan calls candidatePool's loop issues.
// Two changes close that gap:
//   - candidatePool now deduplicates query terms (dedupTerms) BEFORE the scan loop, not
//     just deduplicating the resulting entries after scanning -- a query repeating the
//     same term N times (e.g. a copy-pasted/garbled query) previously triggered N
//     redundant full-cost PrefixScan calls for the identical prefix, since mergedPoolCap's
//     early-break only fires once merged has actually grown, which duplicate-only terms
//     never do.
//   - maxQueryTerms now bounds the number of DISTINCT terms candidatePool will process at
//     all, checked in SearchCandidates (server.go) as request validation (mirroring
//     max_results' existing InvalidArgument pattern) BEFORE any PrefixScan call is issued --
//     this is what actually bounds worst-case scan COST (number of PrefixScan calls), as
//     opposed to perTermPoolCap/mergedPoolCap, which remain useful as a second, independent
//     bound on retained pool MEMORY once scanning has happened.
package rpc

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// termSplitRE splits a string into terms on any run of non-alphanumeric characters --
// the same simple convention agents/ingestion/shortlist.py's _TOKEN_SPLIT_RE uses for the
// same kind of path-string tokenization, so that e.g. "docs/beta/graph-database" splits
// into ["docs", "beta", "graph", "database"] (path separators, hyphens, dots, etc. all act
// as separators). This is the single, shared splitting convention used by BOTH
// candidatePool's PrefixScan-term assembly and rankCandidates' scoring (via splitTerms and
// tokenizeTerms below) -- task 4.5.9.2 (issue #47) deliberately unified these two call
// sites onto one regex so a punctuated/hyphenated query like "graph-database" is split
// identically at both pool-selection time and ranking time; see docs/LLD/query-agent.md's
// "Known risks" section for the full history (a naive whitespace-only split, as an earlier
// prefixTerm helper used, would disagree with this regex for such queries).
var termSplitRE = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// splitTerms splits s into its non-empty terms via termSplitRE, WITHOUT any case
// normalization. Returns nil for a string with no alphanumeric content (including the
// empty string) -- callers must treat a nil/empty term list as "no terms", not an error.
//
// splitTerms (not tokenizeTerms) is what candidatePool uses to build the literal terms it
// passes to btree.PrefixScan: PrefixScan's prefix match is plain, case-sensitive
// strings.HasPrefix against on-disk paths that preserve their original case (see
// engine/btree/scan.go), so lower-casing a scan prefix could silently drop real
// mixed-case-path matches (e.g. a query term "Graph" scanning prefix "graph" would miss a
// path starting "Graph/..."). tokenizeTerms' case-insensitivity is appropriate for
// termOverlapScore's in-memory scoring (which does its own case-insensitive comparison
// against already-lower-cased path terms), but is NOT appropriate for a literal on-disk
// scan prefix -- so the two callers share the same split regex/convention (this function)
// while differing only in whether the result is subsequently lower-cased.
func splitTerms(s string) []string {
	if s == "" {
		return nil
	}
	parts := termSplitRE.Split(s, -1)
	terms := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			terms = append(terms, p)
		}
	}
	return terms
}

// tokenizeTerms lower-cases s and splits it into its non-empty alphanumeric terms via
// splitTerms/termSplitRE. Returns nil for a string with no alphanumeric content (including
// the empty string) -- callers must treat a nil/empty term list as "no terms", not an
// error. Used only for scoring (termOverlapScore/rankCandidates), where case-insensitive
// comparison is correct; see splitTerms' doc comment for why candidatePool's PrefixScan
// term assembly deliberately does NOT go through this case-folding step.
func tokenizeTerms(s string) []string {
	return splitTerms(strings.ToLower(s))
}

// dedupTerms returns terms' distinct elements, preserving both first-seen order and each
// term's exact original case (candidatePool needs case preserved when it hands a term to
// btree.PrefixScan as a literal scan prefix -- see splitTerms' doc comment). This is a
// case-sensitive dedup deliberately: "Graph" and "graph" are different literal PrefixScan
// prefixes and are NOT collapsed here, even though they would collapse under
// tokenizeTerms' case-insensitive scoring.
//
// Fix-cycle addition (issue #47, subtask 4.5.9.2, CHANGES_REQUESTED re-fix): before this,
// candidatePool's loop issued one btree.PrefixScan per term OCCURRENCE in the split query,
// not per distinct term -- a query repeating the same term N times (e.g. "graph graph
// graph") triggered N redundant full-cost scans of the identical prefix. candidatePool now
// calls dedupTerms on splitTerms' output before the scan loop, so each distinct term is
// scanned at most once regardless of how many times it appears in the query.
func dedupTerms(terms []string) []string {
	seen := make(map[string]struct{}, len(terms))
	deduped := make([]string, 0, len(terms))
	for _, t := range terms {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		deduped = append(deduped, t)
	}
	return deduped
}

// maxQueryTerms bounds the number of DISTINCT query terms (post-dedupTerms) candidatePool
// will process at all -- checked as request validation in SearchCandidates (server.go),
// BEFORE any btree.PrefixScan call is issued, mirroring that handler's existing
// max_results InvalidArgument check.
//
// Fix-cycle addition (issue #47, subtask 4.5.9.2, CHANGES_REQUESTED re-fix): this is what
// actually bounds candidatePool's worst-case SCAN cost (the number of PrefixScan calls the
// loop below issues), which perTermPoolCap/mergedPoolCap never did on their own -- both of
// those only truncate/cap RESULTS a call already returned, after PrefixScan has already
// paid the full cost of its leaf-chain traversal (see engine/btree/scan.go). 32 is chosen
// as generously above any realistic natural-language query's term count -- even a long,
// verbose sentence-style query ("how do I configure the graph database replication
// settings for a multi-region deployment") tokenizes to well under 20 distinct terms --
// while still rejecting a pathological query (hundreds/thousands of distinct terms) before
// it can multiply PrefixScan's per-call cost by an unbounded term count. A query exceeding
// this is rejected outright (InvalidArgument), not silently truncated: silent truncation
// would make ranking quietly ignore some of the caller's real query terms, which is a
// worse failure mode for a search RPC than an explicit, actionable client error.
const maxQueryTerms = 32

// errTooManyQueryTerms is candidatePool's sentinel error for a query exceeding
// maxQueryTerms distinct terms; SearchCandidates (server.go) maps it to
// codes.InvalidArgument via errors.Is, distinguishing it from candidatePool's other
// (btree.PrefixScan I/O) errors, which map to codes.Internal.
var errTooManyQueryTerms = errors.New("rpc: SearchCandidates: query exceeds max distinct term count")

// validateQueryTermCount returns a non-nil error (wrapping errTooManyQueryTerms, checkable
// via errors.Is) if query splits (via splitTerms) into more than maxQueryTerms DISTINCT
// terms (post dedupTerms), else nil. Called by SearchCandidates (server.go) as request
// validation -- mirroring that handler's existing max_results InvalidArgument check --
// BEFORE candidatePool or any btree.PrefixScan call is ever reached, which is what actually
// bounds candidatePool's worst-case number of scans; see maxQueryTerms' doc comment.
func validateQueryTermCount(query string) error {
	n := len(dedupTerms(splitTerms(query)))
	if n > maxQueryTerms {
		return fmt.Errorf("%w: query has %d distinct terms, max %d", errTooManyQueryTerms, n, maxQueryTerms)
	}
	return nil
}

// perTermPoolCap bounds how many btree.PrefixScan entries a single query term may
// contribute to candidatePool's merged pool (task 4.5.9.2, issue #47).
//
// Fix-cycle correction (subtask 4.5.9.2, CHANGES_REQUESTED re-fix): this cap bounds only
// RETAINED pool memory, NOT scan cost -- btree.PrefixScan (see engine/btree/scan.go's
// leaf-chain-following implementation) already completes its full traversal and returns
// every matching entry before candidatePool ever gets to slice the result down to
// perTermPoolCap entries, so this constant does nothing to reduce the I/O/traversal cost a
// pathological (very common, very short) prefix term can incur. What actually bounds
// worst-case scan COST now is maxQueryTerms (checked in server.go before any PrefixScan
// call) plus dedupTerms (collapsing repeated terms in the query to one scan each) --
// perTermPoolCap remains a useful, independent second bound on how much of any one term's
// (already-paid-for) result candidatePool retains afterward. 500 is a deliberately
// conservative, cheap-to-compute value for that retained-memory purpose: real single-term
// queries are expected to stay far under this in practice, so it should not visibly change
// behavior for them.
const perTermPoolCap = 500

// mergedPoolCap bounds the total size of candidatePool's deduplicated, cross-term merged
// pool before it is handed to rankCandidates (task 4.5.9.2, issue #47). Even with
// perTermPoolCap limiting each individual term's contribution, a query with many distinct
// terms could still merge into a pool of unbounded size; mergedPoolCap is a second,
// coarser safety valve capping the merge's total RETAINED size (see perTermPoolCap's doc
// comment above for why "retained", not "scanned" -- the same correction applies here:
// this does not reduce how many PrefixScan calls are made or their individual cost, only
// how much of their combined output candidatePool keeps). 2000 is chosen as a generous
// multiple of perTermPoolCap, comfortably above maxQueryTerms (32) distinct terms' worth of
// perTermPoolCap-sized contributions in the overlapping/typical case, while still bounding
// worst-case retained memory for a pathological input.
const mergedPoolCap = 2000

// candidatePool assembles SearchCandidates' (server.go) candidate pool for query: it
// splits query into terms (splitTerms, NOT tokenizeTerms -- see splitTerms' doc comment
// for why case must be preserved here), issues one btree.PrefixScan per term, and merges
// the resulting ScanEntry pools into a single slice, deduplicated by FileID (falling back
// to Path if two entries somehow share FileID but differ in Path, or vice versa), in
// first-seen order across terms in split order. This is task 4.5.9.2's (issue #47)
// implementation of the option-(b) strategy decided and documented in subtask 4.5.9.1
// (docs/LLD/query-agent.md, docs/LLD/btree.md "Known risks"): rankCandidates itself is
// completely unmodified by this change, since it already scores each candidate against the
// FULL query term set regardless of which scan produced it -- only pool ASSEMBLY changes.
//
// A query with zero terms (splitTerms(query) == nil, e.g. the empty string) is a special
// case handled separately: candidatePool issues exactly one btree.PrefixScan with prefix
// "", returning the FULL uncapped pool. This preserves byte-for-byte the pre-4.5.9.2
// behavior for agents/ingestion/shortlist.py's existing query="" pool-retrieval usage
// (task-3.4.2), which depends on receiving the entire tree's contents (subject only to
// SearchCandidates' max_results, applied after rankCandidates' no-op empty-query scoring)
// for its own local BM25 re-ranking -- neither perTermPoolCap nor mergedPoolCap apply to
// this case, since it is not a multi-term fan-out and was already unbounded before this
// change.
func candidatePool(store *btree.NodeStore, rootNodeID uint64, query string) ([]btree.ScanEntry, error) {
	terms := splitTerms(query)
	if len(terms) == 0 {
		// Zero terms includes not just query == "" but any query with no alphanumeric
		// content (e.g. all-whitespace or all-punctuation) -- the pre-4.5.9.2 prefixTerm
		// helper collapsed both of those to "" (strings.Fields returns none), so this
		// scans with the literal empty prefix "" (not the raw query string) to match that
		// exact prior behavior, not just the query == "" case.
		return btree.PrefixScan(store, rootNodeID, "")
	}

	// dedupTerms collapses repeated terms (e.g. query "graph graph graph") to one scan
	// each -- see dedupTerms' doc comment. maxQueryTerms is enforced by the caller
	// (SearchCandidates, server.go) as request validation BEFORE this function is even
	// called, so terms here is already guaranteed len(terms) <= maxQueryTerms; the
	// invariant is not re-checked here to avoid a second, redundant validation path (see
	// server.go's SearchCandidates doc comment).
	terms = dedupTerms(terms)

	seenFileID := make(map[uint64]struct{})
	seenPath := make(map[string]struct{})
	merged := make([]btree.ScanEntry, 0, len(terms))

	for _, term := range terms {
		if len(merged) >= mergedPoolCap {
			break
		}

		entries, err := btree.PrefixScan(store, rootNodeID, term)
		if err != nil {
			return nil, err
		}

		if len(entries) > perTermPoolCap {
			entries = entries[:perTermPoolCap]
		}

		for _, e := range entries {
			if _, ok := seenFileID[e.FileID]; ok {
				continue
			}
			if _, ok := seenPath[e.Path]; ok {
				continue
			}
			seenFileID[e.FileID] = struct{}{}
			seenPath[e.Path] = struct{}{}
			merged = append(merged, e)

			if len(merged) >= mergedPoolCap {
				break
			}
		}
	}

	return merged, nil
}

// termOverlapScore computes a simple term-overlap relevance score for one candidate path
// against a pre-tokenized, already-deduplicated set of query terms: the fraction of
// distinct query terms that also appear anywhere among path's own tokenized terms, in
// [0, 1]. Returns 0 if queryTermSet is empty -- the documented no-op case (see this file's
// package doc comment) relied on by agents/ingestion/shortlist.py's empty-query pool
// retrieval.
func termOverlapScore(queryTermSet map[string]struct{}, path string) float32 {
	if len(queryTermSet) == 0 {
		return 0
	}

	pathTerms := make(map[string]struct{})
	for _, t := range tokenizeTerms(path) {
		pathTerms[t] = struct{}{}
	}

	matched := 0
	for qt := range queryTermSet {
		if _, ok := pathTerms[qt]; ok {
			matched++
		}
	}

	return float32(matched) / float32(len(queryTermSet))
}

// rankCandidates converts entries (as returned by btree.PrefixScan, in ascending
// sorted-path order) into a ranked []*hivemindv1.CandidateTopic slice: each entry's score
// is its termOverlapScore against query's tokenized terms, and the slice is stably sorted
// by score descending. Stability means ties (including the all-zero-score case for an
// empty query, or for a query whose terms match none of entries' paths) fall back to
// entries' original PrefixScan order, which is the documented no-op behavior the empty-
// query case depends on.
//
// rankCandidates does not cap the result to any max_results bound -- callers must rank the
// full candidate pool before truncating to top-K (truncating first could silently drop a
// higher-scoring match that happened to sort later in raw PrefixScan order), see
// SearchCandidates in server.go.
func rankCandidates(query string, entries []btree.ScanEntry) []*hivemindv1.CandidateTopic {
	queryTermSet := make(map[string]struct{})
	for _, t := range tokenizeTerms(query) {
		queryTermSet[t] = struct{}{}
	}

	candidates := make([]*hivemindv1.CandidateTopic, len(entries))
	for i, e := range entries {
		candidates[i] = &hivemindv1.CandidateTopic{
			FileId: e.FileID,
			Path:   e.Path,
			Score:  termOverlapScore(queryTermSet, e.Path),
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].GetScore() > candidates[j].GetScore()
	})

	return candidates
}
