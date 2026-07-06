package split

import (
	"errors"
	"reflect"
	"testing"
)

// TestMockSplitProposer asserts that MockSplitProposer satisfies SplitProposer and
// returns the expected fixed plan for known fixture input, per 2b.2.2's test spec.
func TestMockSplitProposer(t *testing.T) {
	t.Run("default plan for known fixture input", func(t *testing.T) {
		mock := NewMockSplitProposer(FixtureSplitPlan)

		var proposer SplitProposer = mock

		got, err := proposer.ProposeSplit(FixtureFileContent)
		if err != nil {
			t.Fatalf("ProposeSplit: unexpected error: %v", err)
		}
		if !reflect.DeepEqual(got, FixtureSplitPlan) {
			t.Fatalf("ProposeSplit: got plan %+v, want %+v", got, FixtureSplitPlan)
		}

		// Deterministic: calling again with the same input returns the identical plan.
		got2, err2 := proposer.ProposeSplit(FixtureFileContent)
		if err2 != nil {
			t.Fatalf("ProposeSplit (second call): unexpected error: %v", err2)
		}
		if !reflect.DeepEqual(got2, FixtureSplitPlan) {
			t.Fatalf("ProposeSplit (second call): got plan %+v, want %+v", got2, FixtureSplitPlan)
		}
	})

	t.Run("caller-registered plan keyed by exact fileContent", func(t *testing.T) {
		wantPlan := SplitPlan{
			Files: []SplitFileProposal{
				{
					NewPath: "custom-part-1.md",
					SectionRanges: []SectionRange{
						{Start: 0, End: 5},
					},
				},
			},
			RedirectSummary: "split into custom-part-1.md",
		}
		content := []byte("hello")

		mock := NewMockSplitProposer(FixtureSplitPlan).WithPlan(content, wantPlan)

		got, err := mock.ProposeSplit(content)
		if err != nil {
			t.Fatalf("ProposeSplit: unexpected error: %v", err)
		}
		if !reflect.DeepEqual(got, wantPlan) {
			t.Fatalf("ProposeSplit: got plan %+v, want %+v", got, wantPlan)
		}

		// A different, unregistered fileContent still falls back to the default plan.
		gotDefault, err := mock.ProposeSplit(FixtureFileContent)
		if err != nil {
			t.Fatalf("ProposeSplit (unregistered input): unexpected error: %v", err)
		}
		if !reflect.DeepEqual(gotDefault, FixtureSplitPlan) {
			t.Fatalf("ProposeSplit (unregistered input): got plan %+v, want %+v", gotDefault, FixtureSplitPlan)
		}
	})

	t.Run("caller-registered error keyed by exact fileContent takes precedence", func(t *testing.T) {
		wantErr := errors.New("propose split: fixture failure")
		content := []byte("bad content")

		mock := NewMockSplitProposer(FixtureSplitPlan).
			WithPlan(content, FixtureSplitPlan).
			WithErr(content, wantErr)

		gotPlan, err := mock.ProposeSplit(content)
		if !errors.Is(err, wantErr) {
			t.Fatalf("ProposeSplit: got error %v, want %v", err, wantErr)
		}
		if !reflect.DeepEqual(gotPlan, SplitPlan{}) {
			t.Fatalf("ProposeSplit: on error, got non-zero plan %+v", gotPlan)
		}
	})

	t.Run("default error when no fixture matches", func(t *testing.T) {
		wantErr := errors.New("propose split: no default plan configured")
		mock := &MockSplitProposer{DefaultErr: wantErr}

		gotPlan, err := mock.ProposeSplit([]byte("anything"))
		if !errors.Is(err, wantErr) {
			t.Fatalf("ProposeSplit: got error %v, want %v", err, wantErr)
		}
		if !reflect.DeepEqual(gotPlan, SplitPlan{}) {
			t.Fatalf("ProposeSplit: on error, got non-zero plan %+v", gotPlan)
		}
	})

	t.Run("fixture SectionRanges respect the [0, len(fileContent)] invariant", func(t *testing.T) {
		for _, f := range FixtureSplitPlan.Files {
			for _, r := range f.SectionRanges {
				if r.Start < 0 || r.Start > r.End || r.End > len(FixtureFileContent) {
					t.Fatalf("SectionRange %+v for %s violates 0 <= Start <= End <= len(fileContent)=%d",
						r, f.NewPath, len(FixtureFileContent))
				}
			}
		}
	})
}
