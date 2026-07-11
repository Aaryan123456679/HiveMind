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
// Fix-cycle update (issue #47, subtask 4.5.9.2, CHANGES_REQUESTED re-fix, this file's
// .cdr/runs/2026-07-11/110-verification): independent verification found that
// perTermPoolCap/mergedPoolCap (below) only bound candidatePool's *retained pool memory*,
// not its scan *cost* -- btree.PrefixScan (engine/btree/scan.go) already completes its full
// leaf-chain traversal and returns every matching entry before candidatePool ever gets to
// truncate the result, and nothing previously bounded either the NUMBER of distinct terms
// candidatePool's loop processes or de-duplicated repeated terms (so a query repeating one
// term N times issued N redundant full PrefixScan calls). This update adds dedupTerms
// (de-duplicates candidatePool's term list before the scan loop, so repeated terms are
// scanned once) and maxQueryTerms/validateQueryTermCount (a hard cap on the number of
// *distinct* terms a request may have, enforced in SearchCandidates, server.go, as request
// validation before any PrefixScan call is issued) -- see maxQueryTerms' doc comment for the
// chosen bound and rationale, and docs/LLD/query-agent.md / docs/LLD/btree.md's "Known
// risks" for the corrected writeup.
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

// perTermPoolCap bounds how many btree.PrefixScan entries a single query term may
// contribute to candidatePool's merged pool (task 4.5.9.2, issue #47). btree.PrefixScan
// itself has no per-call result limit (engine/btree/scan.go returns every matching entry),
// and candidatePool now issues one PrefixScan per query term instead of the pre-4.5.9.2
// single first-token scan.
//
// Correction (CHANGES_REQUESTED re-fix, .cdr/runs/2026-07-11/110-verification): this
// comment previously claimed this cap "caps worst-case fan-out cost for a pathological
// multi-term query" -- that overstated what it does. btree.PrefixScan (engine/btree/scan.go)
// already completes its full leaf-chain traversal and returns EVERY matching entry before
// candidatePool ever gets to truncate the result to perTermPoolCap entries, so this cap
// bounds only this term's *retained* contribution to the merged pool's memory footprint,
// not the *cost* (I/O, traversal work) of the PrefixScan call that produced it. The actual
// bound on worst-case scan cost is now dedupTerms (below), which de-duplicates
// candidatePool's term list before the scan loop so a repeated term is scanned once, not
// once per repetition, and maxQueryTerms/validateQueryTermCount (below), a hard cap on the
// number of *distinct* terms a request may have, enforced in SearchCandidates (server.go)
// before any PrefixScan call is issued at all.
const perTermPoolCap = 500

// mergedPoolCap bounds the total size of candidatePool's deduplicated, cross-term merged
// pool before it is handed to rankCandidates (task 4.5.9.2, issue #47). Even with
// perTermPoolCap limiting each individual term's contribution, a query with many distinct
// terms could still merge into a pool of unbounded size; mergedPoolCap is a second,
// coarser safety valve capping the merge's total growth. 2000 is chosen as a generous
// multiple of perTermPoolCap (accommodating several dozen distinct query terms' worth of
// non-overlapping matches before truncating).
//
// Correction (CHANGES_REQUESTED re-fix, .cdr/runs/2026-07-11/110-verification): same
// caveat as perTermPoolCap above -- this bounds retained pool memory, not scan cost, since
// every PrefixScan call it truncates the output of has already fully completed by the time
// this cap is applied. See dedupTerms/maxQueryTerms below for what actually bounds scan
// cost.
const mergedPoolCap = 2000

// dedupTerms returns terms with duplicate entries removed, preserving the first-seen order
// and original casing of each distinct term (added CHANGES_REQUESTED re-fix,
// .cdr/runs/2026-07-11/110-verification). candidatePool calls this before its scan loop
// (below) so that a query repeating the same term N times (e.g. a pathological or
// accidentally-repeated input) issues exactly one btree.PrefixScan for that term instead of
// N redundant full-cost scans -- mergedPoolCap's early-break could not catch this case on
// its own, since a duplicate term after the first contributes zero NEW entries to merged
// (all of them are already deduplicated by FileID/Path), so merged's length never grows
// from a repeat and the early-break above never fires for a duplicate-only tail.
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

// maxQueryTerms is a hard cap on the number of *distinct* terms (post-dedupTerms) a
// SearchCandidates query may contain, enforced by validateQueryTermCount (below), called
// from SearchCandidates (server.go) BEFORE candidatePool issues a single btree.PrefixScan
// call (added CHANGES_REQUESTED re-fix, .cdr/runs/2026-07-11/110-verification). This is the
// actual bound on candidatePool's worst-case scan *cost*: perTermPoolCap/mergedPoolCap
// above only ever bound retained pool memory, since btree.PrefixScan always completes its
// full traversal before either cap gets a chance to truncate anything -- so without this
// cap, a query with an arbitrarily large number of distinct terms could still force an
// arbitrarily large number of full-cost PrefixScan calls.
//
// 32 is chosen generously above realistic natural-language query patterns: this package's
// existing agents/ingestion callers and rankCandidates' term-overlap scoring are both built
// around short, few-word queries (a handful up to perhaps a dozen or two distinct words for
// an unusually long natural-language question), so 32 distinct terms comfortably covers
// realistic usage while still rejecting a pathological query (e.g. hundreds or thousands of
// distinct terms) outright, rather than silently truncating it to some arbitrary term
// subset (which could produce a confusingly incomplete result set with no indication to the
// caller that anything was dropped). Rejecting via codes.InvalidArgument (server.go) is
// preferred over silent truncation for the same reason SearchCandidates already rejects
// max_results < 0 rather than clamping it.
const maxQueryTerms = 32

// errTooManyQueryTerms is wrapped by validateQueryTermCount's returned error (added
// CHANGES_REQUESTED re-fix, .cdr/runs/2026-07-11/110-verification) so callers (server.go)
// can identify this specific validation failure via errors.Is if ever needed, mirroring the
// wrapped-sentinel-error convention used elsewhere in this package.
var errTooManyQueryTerms = errors.New("rpc: SearchCandidates: query exceeds max distinct term count")

// validateQueryTermCount returns a non-nil error if query would produce more than
// maxQueryTerms distinct terms once split and deduplicated exactly as candidatePool itself
// splits and deduplicates its terms (splitTerms + dedupTerms) -- added CHANGES_REQUESTED
// re-fix, .cdr/runs/2026-07-11/110-verification. Called from SearchCandidates (server.go)
// before candidatePool is invoked at all, so a query that would exceed the cap never causes
// candidatePool to issue a single btree.PrefixScan call.
func validateQueryTermCount(query string) error {
	n := len(dedupTerms(splitTerms(query)))
	if n > maxQueryTerms {
		return fmt.Errorf("%w: query has %d distinct terms, max %d", errTooManyQueryTerms, n, maxQueryTerms)
	}
	return nil
}

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
	// De-duplicate before the scan loop (added CHANGES_REQUESTED re-fix,
	// .cdr/runs/2026-07-11/110-verification) so a query repeating the same term N times
	// issues exactly one btree.PrefixScan for that term, not N -- see dedupTerms' doc
	// comment above for why mergedPoolCap's early-break could not catch this on its own.
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
