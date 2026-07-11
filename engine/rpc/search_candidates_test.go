package rpc

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// rankingFixture is a minimal, self-contained analogue of server_test.go's newFixture,
// built independently for this file so search_candidates_test.go does not share mutable
// test state with server_test.go's TestRPCServerHandlers. It seeds a real *btree.NodeStore
// (not a mock) with several topic paths chosen so that pure lexicographic-path order and
// term-overlap-ranked order diverge.
type rankingFixture struct {
	srv       *Server
	btreeRoot uint64
}

func newRankingFixture(t *testing.T, paths []string) *rankingFixture {
	t.Helper()
	root := t.TempDir()

	fm, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("catalog.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("FileManager.Close: %v", err)
		}
	})
	cat := catalog.NewCatalog(fm)

	idAlloc, err := catalog.NewIDAllocator(fm)
	if err != nil {
		t.Fatalf("catalog.NewIDAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := idAlloc.Close(); err != nil {
			t.Errorf("IDAllocator.Close: %v", err)
		}
	})

	w, err := wal.OpenWriter(filepath.Join(root, "wal"), 1<<20)
	if err != nil {
		t.Fatalf("wal.OpenWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("wal.Writer.Close: %v", err)
		}
	})

	cs, err := catalog.OpenContentStore(root, cat, w)
	if err != nil {
		t.Fatalf("catalog.OpenContentStore: %v", err)
	}

	idxFile, err := btree.OpenIndexFile(filepath.Join(root, "topics.idx"))
	if err != nil {
		t.Fatalf("btree.OpenIndexFile: %v", err)
	}
	t.Cleanup(func() {
		if err := idxFile.Close(); err != nil {
			t.Errorf("index file Close: %v", err)
		}
	})

	store := btree.NewNodeStore(idxFile)
	nodeAlloc, err := btree.NewNodeAllocator(store)
	if err != nil {
		t.Fatalf("btree.NewNodeAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := nodeAlloc.Close(); err != nil {
			t.Errorf("NodeAllocator.Close: %v", err)
		}
	})

	rootNodeID := uint64(0)
	for _, path := range paths {
		fileID, err := idAlloc.Next()
		if err != nil {
			t.Fatalf("IDAllocator.Next: %v", err)
		}
		rec := wal.NewBTreeInsertRecord(path, fileID)
		if _, err := wal.AppendAndApply(w, rec, func() error {
			newRoot, err := btree.Insert(store, nodeAlloc, rootNodeID, path, fileID)
			if err != nil {
				return err
			}
			rootNodeID = newRoot
			return nil
		}); err != nil {
			t.Fatalf("insertPath(%q): %v", path, err)
		}
	}

	pathIndex := btree.NewTree(store, nodeAlloc, rootNodeID)

	srv, err := NewServer(cat, cs, idAlloc, nil, pathIndex, nil, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	return &rankingFixture{srv: srv, btreeRoot: rootNodeID}
}

func candidatePaths(candidates []*hivemindv1.CandidateTopic) []string {
	paths := make([]string, len(candidates))
	for i, c := range candidates {
		paths[i] = c.GetPath()
	}
	return paths
}

// TestSearchCandidates exercises task-4.2.1's term-overlap ranking on top of the
// pre-existing (task-3.2.2) btree-prefix-scan delegation. Per the issue's literal test
// spec: "fixture-populated btree, assert ranked candidate results for known query terms
// match expected ordering."
func TestSearchCandidates(t *testing.T) {
	t.Run("RanksByTermOverlap", func(t *testing.T) {
		// All three paths share the literal prefix "graph" (server.go's SearchCandidates
		// passes only the query's FIRST whitespace-separated token to btree.PrefixScan --
		// see prefixTerm's doc comment), so the query "graph database extra" prefix-matches
		// all three via its first token "graph" alone, while its remaining terms
		// ("database", "extra") differentiate the ranking among them: pure lexicographic
		// path order here ("graph-database-extra/x" < "graph-database/y" <
		// "graph-theory/z") happens to already match the expected overlap-ranked order for
		// this fixture, so a second ordering is added below (DivergesFromLexicographicOrder)
		// that deliberately breaks that coincidence.
		f := newRankingFixture(t, []string{
			"graph-database-extra/x", // 3/3 query terms present
			"graph-database/y",       // 2/3 query terms present
			"graph-theory/z",         // 1/3 query terms present
		})

		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
			Query: "graph database extra",
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}

		want := []string{"graph-database-extra/x", "graph-database/y", "graph-theory/z"}
		got := candidatePaths(resp.GetCandidates())
		if len(got) != len(want) {
			t.Fatalf("SearchCandidates: got %d candidates, want %d (paths=%v)", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("SearchCandidates order = %v, want %v", got, want)
			}
		}

		scores := resp.GetCandidates()
		if !(scores[0].GetScore() > scores[1].GetScore() && scores[1].GetScore() > scores[2].GetScore()) {
			t.Fatalf("SearchCandidates: scores not strictly descending: %v, %v, %v",
				scores[0].GetScore(), scores[1].GetScore(), scores[2].GetScore())
		}
		if scores[1].GetScore() == 0 {
			t.Fatalf("SearchCandidates: partial-overlap candidate %q got zero score, want > 0", scores[1].GetPath())
		}
	})

	t.Run("DivergesFromLexicographicOrder", func(t *testing.T) {
		// Deliberately chosen so lexicographic-path order ("graph/aardvark-only" <
		// "graph/full-match" < "graph/zzz-only") does NOT match the expected term-overlap
		// order, proving the ranking is real and not an accidental artifact of PrefixScan's
		// already-sorted output.
		f := newRankingFixture(t, []string{
			"graph/aardvark-only", // "graph" only: 1/2 query terms
			"graph/full-match",    // "graph","full": 2/2 query terms
			"graph/zzz-only",      // "graph" only: 1/2 query terms
		})

		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
			Query: "graph full",
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}

		got := candidatePaths(resp.GetCandidates())
		if len(got) != 3 {
			t.Fatalf("SearchCandidates: got %d candidates, want 3 (paths=%v)", len(got), got)
		}
		if got[0] != "graph/full-match" {
			t.Fatalf("SearchCandidates: top result = %q, want %q (paths=%v)", got[0], "graph/full-match", got)
		}
		if resp.GetCandidates()[0].GetScore() != 1.0 {
			t.Fatalf("SearchCandidates: top result score = %v, want 1.0 (full overlap)", resp.GetCandidates()[0].GetScore())
		}
	})

	t.Run("EmptyQueryPreservesPrefixScanOrder", func(t *testing.T) {
		paths := []string{"docs/alpha/intro", "docs/beta/intro", "docs/gamma/intro"}
		f := newRankingFixture(t, paths)

		want, err := btree.PrefixScan(f.srv.pathIndex.Store, f.srv.pathIndex.Root(), "")
		if err != nil {
			t.Fatalf("btree.PrefixScan (direct): %v", err)
		}

		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
			Query: "",
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}

		got := resp.GetCandidates()
		if len(got) != len(want) {
			t.Fatalf("SearchCandidates: got %d candidates, want %d", len(got), len(want))
		}
		for i, e := range want {
			if got[i].GetPath() != e.Path || got[i].GetFileId() != e.FileID {
				t.Fatalf("SearchCandidates (empty query): candidate[%d] = {%d, %q}, want {%d, %q} (PrefixScan order must be preserved -- ranking is a documented no-op for an empty query)",
					i, got[i].GetFileId(), got[i].GetPath(), e.FileID, e.Path)
			}
			if got[i].GetScore() != 0 {
				t.Fatalf("SearchCandidates (empty query): candidate[%d] score = %v, want 0", i, got[i].GetScore())
			}
		}
	})

	t.Run("MaxResultsCapsRankedList", func(t *testing.T) {
		// Same divergent-order fixture as DivergesFromLexicographicOrder: the highest-
		// scoring match ("graph/full-match") sorts lexicographically BETWEEN the two
		// lower-scoring matches, so a naive cap-before-rank implementation (truncating
		// PrefixScan's raw sorted-path output before scoring) would keep "graph/aardvark-
		// only" instead. Asserting the single returned candidate is the best-scoring one
		// (not the lexicographically-first one) proves capping happens AFTER ranking.
		f := newRankingFixture(t, []string{
			"graph/aardvark-only",
			"graph/full-match",
			"graph/zzz-only",
		})

		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
			Query:      "graph full",
			MaxResults: 1,
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}
		got := resp.GetCandidates()
		if len(got) != 1 {
			t.Fatalf("SearchCandidates: got %d candidates, want 1 (capped)", len(got))
		}
		if got[0].GetPath() != "graph/full-match" {
			t.Fatalf("SearchCandidates: capped result = %q, want %q (rank-then-cap, not cap-then-rank)", got[0].GetPath(), "graph/full-match")
		}
	})
}

// TestSearchCandidatesMultiWordQuery is task 4.5.9.2's (issue #47) regression test,
// following the issue's own literal test-spec pattern ("./engine/rpc/... -run
// TestSearchCandidatesMultiWordQuery": multi-word query returns non-empty,
// correctly-ranked results, including at least one path not prefix-matching the query's
// first token). Before this subtask, SearchCandidates' pool selection used ONLY the
// query's first whitespace token as a literal btree.PrefixScan prefix (prefixTerm,
// removed by this change); a genuine natural-language query like "how do I configure the
// graph database" prefix-scans on "how", which matches nothing, so the pre-4.5.9.2 pool
// (and therefore the final result) would have been empty regardless of ranking.
func TestSearchCandidatesMultiWordQuery(t *testing.T) {
	f := newRankingFixture(t, []string{
		"graph-database/handbook", // "graph" AND "database": 2 of the query's real terms
		"graph-theory/intro",      // "graph" only: 1 of the query's real terms
		"database-design/notes",   // "database" only, and does NOT prefix-match "graph"
		// -- proving the merged pool includes a match found via a
		// non-first, non-"graph" scan term, not just the first-token-prefix-matching set.
		"unrelated/other", // matches none of the query's real terms; must not appear.
	})

	resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
		Query: "how do I configure the graph database",
	})
	if err != nil {
		t.Fatalf("SearchCandidates: %v", err)
	}

	got := resp.GetCandidates()
	if len(got) == 0 {
		t.Fatalf("SearchCandidates: got 0 candidates for multi-word query, want > 0 (pre-4.5.9.2 first-token-only pool selection would have returned 0 here)")
	}

	paths := candidatePaths(got)
	for _, want := range []string{"graph-database/handbook", "graph-theory/intro", "database-design/notes"} {
		found := false
		for _, p := range paths {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("SearchCandidates: candidates %v missing expected path %q", paths, want)
		}
	}
	for _, p := range paths {
		if p == "unrelated/other" {
			t.Fatalf("SearchCandidates: candidates %v unexpectedly include %q, which matches none of the query's real terms", paths, p)
		}
	}

	if got[0].GetPath() != "graph-database/handbook" {
		t.Fatalf("SearchCandidates: top-ranked result = %q, want %q (matches both \"graph\" and \"database\", the highest term-overlap)", got[0].GetPath(), "graph-database/handbook")
	}
	if !(got[0].GetScore() > got[1].GetScore() && got[0].GetScore() > got[2].GetScore()) {
		t.Fatalf("SearchCandidates: top result score %v not strictly greater than the other two (%v, %v)", got[0].GetScore(), got[1].GetScore(), got[2].GetScore())
	}

	// database-design/notes does not prefix-match "graph" (the query's first REAL/
	// content term once stopword-like leading terms "how do I configure the" are
	// skipped) -- it is only found via the "database" PrefixScan, proving the merge
	// covers a term other than the pool-dominant one, not merely the first matching term.
	foundNonGraphPrefixed := false
	for _, p := range paths {
		if p == "database-design/notes" {
			foundNonGraphPrefixed = true
		}
	}
	if !foundNonGraphPrefixed {
		t.Fatalf("SearchCandidates: candidates %v missing %q (a path found only via a non-\"graph\" scan term)", paths, "database-design/notes")
	}
}

// TestDedupTermsCollapsesRepeatedTerms is a direct, white-box unit test of dedupTerms
// (fix-cycle addition, issue #47, subtask 4.5.9.2, CHANGES_REQUESTED re-fix,
// .cdr/runs/2026-07-11/110-verification): candidatePool's scan loop now ranges over
// dedupTerms(splitTerms(query)), not the raw split terms, so a query repeating the same
// term N times must scan it exactly once, not N times. This test asserts dedupTerms itself
// collapses repeats while preserving first-seen order and exact case -- the property
// candidatePool's loop then relies on to avoid redundant btree.PrefixScan calls.
func TestDedupTermsCollapsesRepeatedTerms(t *testing.T) {
	got := dedupTerms([]string{"graph", "graph", "database", "graph", "Graph", "database"})
	want := []string{"graph", "database", "Graph"}
	if len(got) != len(want) {
		t.Fatalf("dedupTerms: got %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupTerms: got %v, want %v (order/case must be preserved)", got, want)
		}
	}
}

// TestSearchCandidatesRepeatedTermScansOnce is an end-to-end regression test proving
// candidatePool's scan loop treats a query's repeated term as a SINGLE distinct term, not
// one occurrence per repetition (fix-cycle addition, issue #47, subtask 4.5.9.2,
// CHANGES_REQUESTED re-fix). Before dedupTerms was added, candidatePool's loop ranged over
// splitTerms' raw (non-deduplicated) output, so a query repeating one term
// maxQueryTerms+1 times would previously have been processed as maxQueryTerms+1
// occurrences (maxQueryTerms+1 redundant, full-cost PrefixScan calls for the identical
// prefix). This test issues a query with the same term repeated far more than
// maxQueryTerms times: since candidatePool now deduplicates by DISTINCT term before both
// the maxQueryTerms request-validation check (server.go's validateQueryTermCount) and the
// scan loop itself, this single-distinct-term query must be accepted (not rejected as
// exceeding maxQueryTerms) and must still return the expected match.
func TestSearchCandidatesRepeatedTermScansOnce(t *testing.T) {
	f := newRankingFixture(t, []string{"graph-database/handbook", "unrelated/other"})

	repeated := strings.Repeat("graph ", maxQueryTerms*3) // one distinct term, repeated
	// far more times than maxQueryTerms -- would be rejected by validateQueryTermCount if
	// dedup were not applied before the distinct-term-count check.
	resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
		Query: repeated,
	})
	if err != nil {
		t.Fatalf("SearchCandidates: %v (a repeated single term must be deduplicated to one distinct term before maxQueryTerms is checked, not rejected as too many terms)", err)
	}

	paths := candidatePaths(resp.GetCandidates())
	found := false
	for _, p := range paths {
		if p == "graph-database/handbook" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SearchCandidates: candidates %v missing %q for repeated-term query", paths, "graph-database/handbook")
	}
}

// TestSearchCandidatesRejectsTooManyDistinctQueryTerms is the regression test for
// maxQueryTerms/validateQueryTermCount (fix-cycle addition, issue #47, subtask 4.5.9.2,
// CHANGES_REQUESTED re-fix, .cdr/runs/2026-07-11/110-verification): a query with more than
// maxQueryTerms DISTINCT terms is rejected with InvalidArgument BEFORE candidatePool issues
// a single btree.PrefixScan call -- this is what actually bounds SearchCandidates' worst-
// case scan cost for a query with a pathological number of distinct terms (as opposed to
// perTermPoolCap/mergedPoolCap, which only bound retained pool memory after scanning has
// already happened).
func TestSearchCandidatesRejectsTooManyDistinctQueryTerms(t *testing.T) {
	f := newRankingFixture(t, []string{"graph-database/handbook"})

	terms := make([]string, maxQueryTerms+1)
	for i := range terms {
		// Distinct terms: term0, term1, ... termN -- guarantees exactly maxQueryTerms+1
		// DISTINCT terms after dedupTerms, not just maxQueryTerms+1 occurrences.
		terms[i] = "term" + strconv.Itoa(i)
	}
	query := strings.Join(terms, " ")

	_, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
		Query: query,
	})
	if err == nil {
		t.Fatalf("SearchCandidates: got nil error for a query with %d distinct terms (max %d), want InvalidArgument", len(terms), maxQueryTerms)
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SearchCandidates: got error code %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}

	// A query at exactly maxQueryTerms distinct terms must still be accepted.
	okQuery := strings.Join(terms[:maxQueryTerms], " ")
	if _, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
		Query: okQuery,
	}); err != nil {
		t.Fatalf("SearchCandidates: query with exactly maxQueryTerms (%d) distinct terms unexpectedly rejected: %v", maxQueryTerms, err)
	}
}
