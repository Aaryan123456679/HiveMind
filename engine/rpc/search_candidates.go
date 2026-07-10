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
package rpc

import (
	"regexp"
	"sort"
	"strings"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// prefixTerm returns the first whitespace-separated token of query, trimmed of any
// leading/trailing whitespace, or "" if query has no non-whitespace content. This is the
// literal string SearchCandidates (server.go) passes to btree.PrefixScan as its prefix --
// btree exposes no multi-term/fuzzy query primitive, so only a single leading token can
// ever act as a literal prefix. A query with no interior whitespace (every pre-existing
// caller, including agents/ingestion/shortlist.py's query="" pool-retrieval usage and
// task-3.2.2's original single-token queries) is its own only token, so prefixTerm is the
// identity function for all of them -- this function only changes behavior for a query
// that actually contains multiple whitespace-separated terms, which no pre-existing caller
// produces.
func prefixTerm(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// termSplitRE splits a string into terms on any run of non-alphanumeric characters --
// the same simple convention agents/ingestion/shortlist.py's _TOKEN_SPLIT_RE uses for the
// same kind of path-string tokenization, so that e.g. "docs/beta/graph-database" splits
// into ["docs", "beta", "graph", "database"] (path separators, hyphens, dots, etc. all act
// as separators).
var termSplitRE = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// tokenizeTerms lower-cases s and splits it into its non-empty alphanumeric terms via
// termSplitRE. Returns nil for a string with no alphanumeric content (including the empty
// string) -- callers must treat a nil/empty term list as "no terms", not an error.
func tokenizeTerms(s string) []string {
	if s == "" {
		return nil
	}
	parts := termSplitRE.Split(strings.ToLower(s), -1)
	terms := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			terms = append(terms, p)
		}
	}
	return terms
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
