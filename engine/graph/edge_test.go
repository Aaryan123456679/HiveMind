package graph

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEdgeTypes exercises subtask 3.1.4's edge-type creation/validation support
// (edge.go) for all four edge types: ENTITY_COOCCUR, LLM_ASSERTED, SPLIT_SIBLING,
// REDIRECT.
func TestEdgeTypes(t *testing.T) {
	t.Run("ValidEdgeType", func(t *testing.T) {
		valid := []EdgeType{EdgeSplitSibling, EdgeRedirect, EdgeEntityCooccur, EdgeLLMAsserted}
		for _, ty := range valid {
			if !ValidEdgeType(ty) {
				t.Errorf("ValidEdgeType(%v) = false, want true", ty)
			}
		}

		invalid := []EdgeType{EdgeTypeInvalid, EdgeType(5), EdgeType(200), EdgeType(255)}
		for _, ty := range invalid {
			if ValidEdgeType(ty) {
				t.Errorf("ValidEdgeType(%v) = true, want false", ty)
			}
		}
	})

	t.Run("NameParseRoundTrip", func(t *testing.T) {
		cases := []struct {
			ty   EdgeType
			name string
		}{
			{EdgeEntityCooccur, "ENTITY_COOCCUR"},
			{EdgeLLMAsserted, "LLM_ASSERTED"},
			{EdgeSplitSibling, "SPLIT_SIBLING"},
			{EdgeRedirect, "REDIRECT"},
		}
		for _, c := range cases {
			got, err := EdgeTypeName(c.ty)
			if err != nil {
				t.Fatalf("EdgeTypeName(%v): unexpected error: %v", c.ty, err)
			}
			if got != c.name {
				t.Errorf("EdgeTypeName(%v) = %q, want %q", c.ty, got, c.name)
			}

			parsed, err := ParseEdgeType(c.name)
			if err != nil {
				t.Fatalf("ParseEdgeType(%q): unexpected error: %v", c.name, err)
			}
			if parsed != c.ty {
				t.Errorf("ParseEdgeType(%q) = %v, want %v", c.name, parsed, c.ty)
			}
		}

		if _, err := EdgeTypeName(EdgeTypeInvalid); err == nil {
			t.Error("EdgeTypeName(EdgeTypeInvalid): expected error, got nil")
		}
		if _, err := EdgeTypeName(EdgeType(200)); err == nil {
			t.Error("EdgeTypeName(EdgeType(200)): expected error, got nil")
		}
		if _, err := ParseEdgeType("NOT_A_REAL_TYPE"); err == nil {
			t.Error(`ParseEdgeType("NOT_A_REAL_TYPE"): expected error, got nil`)
		}
		if _, err := ParseEdgeType(""); err == nil {
			t.Error(`ParseEdgeType(""): expected error, got nil`)
		}
	})

	t.Run("NewCSREdge", func(t *testing.T) {
		valid := []EdgeType{EdgeSplitSibling, EdgeRedirect, EdgeEntityCooccur, EdgeLLMAsserted}
		for _, ty := range valid {
			e, err := NewCSREdge(42, ty, 7, 1000)
			if err != nil {
				t.Fatalf("NewCSREdge(type=%v): unexpected error: %v", ty, err)
			}
			want := CSREdge{Target: 42, Type: ty, Weight: 7, LastUpdated: 1000}
			if e != want {
				t.Errorf("NewCSREdge(type=%v) = %+v, want %+v", ty, e, want)
			}
		}

		for _, ty := range []EdgeType{EdgeTypeInvalid, EdgeType(200)} {
			e, err := NewCSREdge(42, ty, 7, 1000)
			if err == nil {
				t.Errorf("NewCSREdge(type=%v): expected error, got nil (edge=%+v)", ty, e)
			}
			if e != (CSREdge{}) {
				t.Errorf("NewCSREdge(type=%v): expected zero-value edge on error, got %+v", ty, e)
			}
		}
	})

	t.Run("CSREdgeEncodeDecodeRoundTrip", func(t *testing.T) {
		valid := []EdgeType{EdgeSplitSibling, EdgeRedirect, EdgeEntityCooccur, EdgeLLMAsserted}
		for _, ty := range valid {
			original := CSREdge{Target: 99, Type: ty, Weight: 3, LastUpdated: 555}
			encoded := make([]byte, csrEdgeEncodedSize)
			original.encode(encoded)
			decoded, err := decodeCSREdge(encoded)
			if err != nil {
				t.Fatalf("decodeCSREdge(type=%v): unexpected error: %v", ty, err)
			}
			if decoded != original {
				t.Errorf("round trip for type %v: got %+v, want %+v", ty, decoded, original)
			}
		}

		// A manually corrupted encoded buffer with an undefined type byte must be
		// rejected at decode time, not silently accepted (see edge.go's decodeCSREdge
		// doc comment: CRC32 alone would not catch this class of bug, since it only
		// verifies the bytes actually written match, not that the type byte written
		// was ever valid).
		original := CSREdge{Target: 99, Type: EdgeSplitSibling, Weight: 3, LastUpdated: 555}
		corrupted := make([]byte, csrEdgeEncodedSize)
		original.encode(corrupted)
		corrupted[offCSREdgeType] = 200
		if _, err := decodeCSREdge(corrupted); err == nil {
			t.Error("decodeCSREdge with undefined type byte 200: expected error, got nil")
		}
	})

	t.Run("EdgeLogRejectsUndefinedType", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "edgelogs")
		l, err := OpenEdgeLog(root)
		if err != nil {
			t.Fatalf("OpenEdgeLog: %v", err)
		}
		defer l.Close()

		const sourceID = uint64(7)
		valid := []CSREdge{
			{Target: 1, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 10},
			{Target: 2, Type: EdgeRedirect, Weight: 1, LastUpdated: 11},
			{Target: 3, Type: EdgeEntityCooccur, Weight: 5, LastUpdated: 12},
			{Target: 4, Type: EdgeLLMAsserted, Weight: 1, LastUpdated: 13},
		}
		for _, e := range valid {
			if err := l.AppendEdge(sourceID, e); err != nil {
				t.Fatalf("AppendEdge(%+v): unexpected error: %v", e, err)
			}
		}

		got, err := l.ReadNode(sourceID)
		if err != nil {
			t.Fatalf("ReadNode: %v", err)
		}
		if len(got) != len(valid) {
			t.Fatalf("ReadNode after 4 valid appends: got %d edges, want %d: %+v", len(got), len(valid), got)
		}

		invalid := []CSREdge{
			{Target: 5, Type: EdgeTypeInvalid, Weight: 1, LastUpdated: 14},
			{Target: 6, Type: EdgeType(200), Weight: 1, LastUpdated: 15},
		}
		for _, e := range invalid {
			if err := l.AppendEdge(sourceID, e); err == nil {
				t.Errorf("AppendEdge(%+v): expected error for invalid type, got nil", e)
			}
		}

		// Nothing durable should have been written by the rejected appends.
		got, err = l.ReadNode(sourceID)
		if err != nil {
			t.Fatalf("ReadNode after rejected appends: %v", err)
		}
		if len(got) != len(valid) {
			t.Fatalf("ReadNode after rejected appends: got %d edges, want %d (rejected appends must not be durably written): %+v", len(got), len(valid), got)
		}
	})

	t.Run("WriteCSRRejectsUndefinedType", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "graph.dat")

		adjacency := map[uint64][]CSREdge{
			1: {{Target: 2, Type: EdgeType(200), Weight: 1, LastUpdated: 1}},
		}
		g := BuildCSR(adjacency)

		err := WriteCSR(path, g)
		if err == nil {
			t.Fatal("WriteCSR with an undefined edge type: expected error, got nil")
		}

		if _, statErr := os.Stat(path); statErr == nil {
			t.Error("WriteCSR with an undefined edge type must not create the output file, but it exists")
		} else if !os.IsNotExist(statErr) {
			t.Errorf("os.Stat(%s): unexpected error: %v", path, statErr)
		}
	})
}
