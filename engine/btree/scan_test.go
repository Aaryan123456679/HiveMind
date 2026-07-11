package btree

import (
	"reflect"
	"sort"
	"testing"
)

// buildScanTree inserts every (path, fileID) pair in entries (in the given
// order, which need not be sorted -- Insert is responsible for maintaining
// sort order) via the real Insert path (newTestStoreAndAllocator, defined in
// insert_test.go), and returns the store and final root node ID.
func buildScanTree(t *testing.T, entries []struct {
	path   string
	fileID uint64
}) (*NodeStore, uint64) {
	t.Helper()

	store, alloc := newTestStoreAndAllocator(t)

	var rootID uint64 = reservedNodeID
	for _, e := range entries {
		var err error
		rootID, err = Insert(store, alloc, rootID, e.path, e.fileID)
		if err != nil {
			t.Fatalf("Insert(%q, %d): unexpected error: %v", e.path, e.fileID, err)
		}
	}
	return store, rootID
}

// sortedSubset returns the (path, fileID) pairs from entries whose path has
// prefix as a string prefix, sorted ascending by path -- i.e. the expected
// result of PrefixScan(prefix) given entries was fully inserted.
func sortedSubset(entries []struct {
	path   string
	fileID uint64
}, prefix string) []ScanEntry {
	var want []ScanEntry
	for _, e := range entries {
		if len(e.path) >= len(prefix) && e.path[:len(prefix)] == prefix {
			want = append(want, ScanEntry{Path: e.path, FileID: e.fileID})
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
	return want
}

// TestPrefixScan is this subtask's required test spec: insert a mixed set of
// topic paths (spanning several distinct prefixes), and assert that
// PrefixScan returns exactly the expected subset, in sorted order, for
// several different prefixes -- including one matching zero keys.
func TestPrefixScan(t *testing.T) {
	entries := []struct {
		path   string
		fileID uint64
	}{
		{"auth/login", 1},
		{"auth/logout", 2},
		{"auth/oauth", 3},
		{"auth/register", 4},
		{"billing/invoice", 5},
		{"billing/receipt", 6},
		{"docs/readme", 7},
		{"zzz/top", 8},
	}

	store, rootID := buildScanTree(t, entries)

	cases := []string{
		"auth/",
		"billing/",
		"docs/",
		"zzz/",
		"nonexistent/", // matches zero keys
		"",             // empty prefix matches everything
	}

	for _, prefix := range cases {
		t.Run(prefix, func(t *testing.T) {
			got, err := PrefixScan(store, rootID, prefix)
			if err != nil {
				t.Fatalf("PrefixScan(%q): unexpected error: %v", prefix, err)
			}
			want := sortedSubset(entries, prefix)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("PrefixScan(%q) = %+v, want %+v", prefix, got, want)
			}
		})
	}
}

// TestPrefixScanNoMatches covers a prefix that matches zero inserted keys as
// its own dedicated test (in addition to being one of TestPrefixScan's
// sub-cases above): the result must be an empty slice and a nil error, not
// an error.
func TestPrefixScanNoMatches(t *testing.T) {
	entries := []struct {
		path   string
		fileID uint64
	}{
		{"auth/login", 1},
		{"billing/invoice", 2},
	}
	store, rootID := buildScanTree(t, entries)

	got, err := PrefixScan(store, rootID, "search/")
	if err != nil {
		t.Fatalf("PrefixScan: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("PrefixScan(\"search/\") = %+v, want empty", got)
	}
}

// TestPrefixScanAcrossLeafBoundary forces enough leaf splits that keys
// sharing a single prefix end up spread across multiple leaves, and asserts
// PrefixScan correctly follows NextLeaf to collect all of them, in order,
// without duplicates or omissions, and without incorrectly including keys
// from a following, differently-prefixed leaf.
func TestPrefixScanAcrossLeafBoundary(t *testing.T) {
	var entries []struct {
		path   string
		fileID uint64
	}
	// Enough keys sharing the "topic/" prefix to force multiple leaf splits
	// (NodeSize is 4096 bytes; three-digit zero-padded suffixes keep keys
	// short but numerous enough to overflow several leaves).
	for i := 0; i < 400; i++ {
		entries = append(entries, struct {
			path   string
			fileID uint64
		}{path: sprintfTopic(i), fileID: uint64(1000 + i)})
	}
	// A few keys sorting after all "topic/..." keys, to confirm the scan
	// stops at the correct point instead of spilling into the next prefix.
	entries = append(entries,
		struct {
			path   string
			fileID uint64
		}{path: "zzzafter/one", fileID: 9001},
		struct {
			path   string
			fileID uint64
		}{path: "zzzafter/two", fileID: 9002},
	)

	store, rootID := buildScanTree(t, entries)

	got, err := PrefixScan(store, rootID, "topic/")
	if err != nil {
		t.Fatalf("PrefixScan: unexpected error: %v", err)
	}
	want := sortedSubset(entries, "topic/")
	if len(want) != 400 {
		t.Fatalf("test setup error: want 400 topic/ entries, computed %d", len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PrefixScan(\"topic/\") returned %d entries, want %d entries (mismatch)", len(got), len(want))
	}
}

// TestPrefixScanPrefixIsCompleteKey covers the case where the prefix string
// is itself a complete, previously-inserted key, AND other keys extend it
// (share it as a strict prefix): PrefixScan must include the exact-match key
// itself alongside the keys that extend it.
func TestPrefixScanPrefixIsCompleteKey(t *testing.T) {
	entries := []struct {
		path   string
		fileID uint64
	}{
		{"auth", 1},            // the prefix itself, stored as a complete key
		{"auth/login", 2},      // extends "auth"
		{"auth/oauth", 3},      // extends "auth"
		{"authorize/grant", 4}, // shares "auth" as a byte-prefix but is a distinct topic
		{"billing/invoice", 5},
	}
	store, rootID := buildScanTree(t, entries)

	got, err := PrefixScan(store, rootID, "auth")
	if err != nil {
		t.Fatalf("PrefixScan: unexpected error: %v", err)
	}
	want := sortedSubset(entries, "auth")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PrefixScan(\"auth\") = %+v, want %+v", got, want)
	}
	// Sanity: want must include all four "auth"-prefixed entries (auth,
	// auth/login, auth/oauth, authorize/grant), not just the exact match or
	// only the "auth/" children.
	if len(want) != 4 {
		t.Fatalf("test setup error: want 4 auth-prefixed entries, computed %d", len(want))
	}
}

// TestPrefixScanEmptyTree is subtask 4.5.1.6's (issue #38) required test spec
// for PrefixScan against a genuinely empty tree -- one that has never had
// anything inserted into it (rootNodeID == reservedNodeID), as distinct from
// TestPrefixScanNoMatches above (a real, populated tree in which a given
// prefix simply matches zero keys). Per scan.go's PrefixScan doc comment,
// this case is intentionally NOT special-cased: it behaves like Lookup does
// for the same rootNodeID, surfacing a non-nil error from the underlying
// ReadNode(reservedNodeID) call. This is documented, existing behavior being
// locked down with a test, not a behavioral change.
func TestPrefixScanEmptyTree(t *testing.T) {
	store, _ := newTestStoreAndAllocator(t)

	got, err := PrefixScan(store, reservedNodeID, "auth/")
	if err == nil {
		t.Fatalf("PrefixScan against a genuinely empty tree (reservedNodeID root): expected a non-nil error, got nil (result=%+v)", got)
	}
	if len(got) != 0 {
		t.Fatalf("PrefixScan against a genuinely empty tree: got non-empty result %+v alongside the expected error", got)
	}
}

// sprintfTopic deterministically generates a "topic/NNN" key with a
// zero-padded, fixed-width numeric suffix so the generated keys sort in the
// same order they are generated (needed so want/got comparisons are
// order-stable).
func sprintfTopic(i int) string {
	const digits = "0123456789"
	b := make([]byte, 0, len("topic/")+3)
	b = append(b, "topic/"...)
	b = append(b, digits[(i/100)%10], digits[(i/10)%10], digits[i%10])
	return string(b)
}
