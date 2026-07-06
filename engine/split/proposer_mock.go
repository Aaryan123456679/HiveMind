package split

// MockSplitProposer is a deterministic, caller-configurable test double for
// SplitProposer. It exists to unblock split-sequence testing (e.g. issue #12's
// atomic split-transaction execution) before the real gRPC-backed ProposeSplit call to
// the ingestion agent exists. It is not itself a _test.go file so that other packages'
// tests (and later subtasks) can import and reuse it directly.
//
// MockSplitProposer never inspects or parses fileContent; it simply returns whatever
// fixture plan/error the caller registered for that exact fileContent, or a configured
// default if no fixture matches. This keeps its behavior fully deterministic and keyed
// only on caller-supplied data, never on any split-decision logic of its own.
type MockSplitProposer struct {
	// Plans maps a fileContent value (as a string) to the SplitPlan that ProposeSplit
	// should return when called with that exact fileContent.
	Plans map[string]SplitPlan

	// Errs maps a fileContent value (as a string) to the error that ProposeSplit should
	// return when called with that exact fileContent. If a key is present in both Errs
	// and Plans, Errs takes precedence and ProposeSplit returns a zero SplitPlan.
	Errs map[string]error

	// DefaultPlan is returned by ProposeSplit when fileContent has no matching entry in
	// Plans or Errs. It lets a test configure a single canned plan without needing to
	// key it to specific fileContent, matching the "unblock split-sequence testing"
	// acceptance criteria for callers that don't care about input-specific fixtures.
	DefaultPlan SplitPlan

	// DefaultErr is returned by ProposeSplit, in place of DefaultPlan, when fileContent
	// has no matching entry in Plans or Errs and DefaultErr is non-nil.
	DefaultErr error
}

// NewMockSplitProposer returns a MockSplitProposer that always returns defaultPlan for
// any fileContent not explicitly registered via Plans/Errs. This is the common case for
// tests that just need one fixed, deterministic plan regardless of input.
func NewMockSplitProposer(defaultPlan SplitPlan) *MockSplitProposer {
	return &MockSplitProposer{
		Plans:       make(map[string]SplitPlan),
		Errs:        make(map[string]error),
		DefaultPlan: defaultPlan,
	}
}

// WithPlan registers an exact fileContent -> SplitPlan fixture and returns the receiver
// for chaining, letting a test build up several fixture inputs deterministically.
func (m *MockSplitProposer) WithPlan(fileContent []byte, plan SplitPlan) *MockSplitProposer {
	if m.Plans == nil {
		m.Plans = make(map[string]SplitPlan)
	}
	m.Plans[string(fileContent)] = plan
	return m
}

// WithErr registers an exact fileContent -> error fixture and returns the receiver for
// chaining. A registered error takes precedence over any plan registered for the same
// fileContent.
func (m *MockSplitProposer) WithErr(fileContent []byte, err error) *MockSplitProposer {
	if m.Errs == nil {
		m.Errs = make(map[string]error)
	}
	m.Errs[string(fileContent)] = err
	return m
}

// ProposeSplit implements SplitProposer. It never mutates fileContent, and its output
// depends only on the fixtures the caller registered (plus DefaultPlan/DefaultErr),
// making it fully deterministic across repeated calls with the same input.
func (m *MockSplitProposer) ProposeSplit(fileContent []byte) (SplitPlan, error) {
	key := string(fileContent)

	if err, ok := m.Errs[key]; ok {
		return SplitPlan{}, err
	}
	if plan, ok := m.Plans[key]; ok {
		return plan, nil
	}
	if m.DefaultErr != nil {
		return SplitPlan{}, m.DefaultErr
	}
	return m.DefaultPlan, nil
}

// FixtureSplitPlan is a canned, well-formed SplitPlan usable directly as a
// MockSplitProposer default or fixture in tests that don't need to construct their own
// plan. Its SectionRanges are half-open and respect the documented
// 0 <= Start <= End <= len(fileContent) invariant for the fixture fileContent it's
// paired with: FixtureFileContent below is 24 bytes long, and every range here falls
// within [0, 24].
var FixtureSplitPlan = SplitPlan{
	Files: []SplitFileProposal{
		{
			NewPath: "fixture-part-1.md",
			SectionRanges: []SectionRange{
				{Start: 0, End: 12},
			},
		},
		{
			NewPath: "fixture-part-2.md",
			SectionRanges: []SectionRange{
				{Start: 12, End: 24},
			},
		},
	},
	RedirectSummary: "split into fixture-part-1.md and fixture-part-2.md",
}

// FixtureFileContent is the fileContent fixture paired with FixtureSplitPlan; it is
// exactly 24 bytes long so FixtureSplitPlan's SectionRanges exactly tile it.
var FixtureFileContent = []byte("fixture file content!!!!")
