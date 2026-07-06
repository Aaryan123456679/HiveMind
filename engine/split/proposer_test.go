package split

import (
	"errors"
	"reflect"
	"testing"
)

// fakeSplitProposer is a trivial test-only SplitProposer used solely to assert the
// interface contract compiles and behaves as documented. It is not the deterministic
// fixture mock added by 2b.2.2 (engine/split/proposer_mock.go).
type fakeSplitProposer struct {
	plan SplitPlan
	err  error
}

func (f fakeSplitProposer) ProposeSplit(fileContent []byte) (SplitPlan, error) {
	if f.err != nil {
		return SplitPlan{}, f.err
	}
	return f.plan, nil
}

// TestSplitProposerInterface asserts the SplitProposer contract via a trivial fake: any
// type exposing ProposeSplit(fileContent []byte) (SplitPlan, error) satisfies the
// interface, a successful call returns the expected plan unchanged, and a failing call
// propagates its error without a usable plan.
func TestSplitProposerInterface(t *testing.T) {
	wantPlan := SplitPlan{
		Files: []SplitFileProposal{
			{
				NewPath: "notes-part-1.md",
				SectionRanges: []SectionRange{
					{Start: 0, End: 100},
				},
			},
			{
				NewPath: "notes-part-2.md",
				SectionRanges: []SectionRange{
					{Start: 100, End: 250},
					{Start: 400, End: 500},
				},
			},
		},
		RedirectSummary: "split into notes-part-1.md and notes-part-2.md",
	}

	var proposer SplitProposer = fakeSplitProposer{plan: wantPlan}

	got, err := proposer.ProposeSplit([]byte("original file content"))
	if err != nil {
		t.Fatalf("ProposeSplit: unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, wantPlan) {
		t.Fatalf("ProposeSplit: got plan %+v, want %+v", got, wantPlan)
	}

	wantErr := errors.New("propose split: transport failed")
	var failingProposer SplitProposer = fakeSplitProposer{err: wantErr}

	gotPlan, err := failingProposer.ProposeSplit([]byte("original file content"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("ProposeSplit: got error %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(gotPlan, SplitPlan{}) {
		t.Fatalf("ProposeSplit: on error, got non-zero plan %+v", gotPlan)
	}
}
