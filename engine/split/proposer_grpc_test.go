package split

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// fixtureProposeSplitServer is a test-local, package-private gRPC server double for
// hivemindv1.HiveMindServer used to exercise GRPCSplitProposer over a real gRPC connection
// without requiring a real LLM-backed ingestion agent. It is deliberately NOT the same as
// engine/rpc/server.go's production Server (which never implements ProposeSplit at all,
// falling back to UnimplementedHiveMindServer's Unimplemented default) -- this is a
// test-only fixture that overrides exactly ProposeSplit with caller-configured canned
// behavior, embedding UnimplementedHiveMindServer for every other (unused) RPC.
type fixtureProposeSplitServer struct {
	hivemindv1.UnimplementedHiveMindServer

	resp       *hivemindv1.ProposeSplitResponse
	err        error
	delay      time.Duration
	gotRequest chan *hivemindv1.ProposeSplitRequest
}

func (f *fixtureProposeSplitServer) ProposeSplit(ctx context.Context, req *hivemindv1.ProposeSplitRequest) (*hivemindv1.ProposeSplitResponse, error) {
	if f.gotRequest != nil {
		select {
		case f.gotRequest <- req:
		default:
		}
	}

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// startFixtureServer spins up a real grpc.Server bound to an in-process bufconn listener,
// registers fixture as its HiveMindServer, and returns a real hivemindv1.HiveMindClient
// dialed against it over a real (loopback, in-memory) gRPC connection. Both the server and
// the client connection are torn down via t.Cleanup, so this is safe under -race and leaves
// no goroutines running past the calling test.
func startFixtureServer(t *testing.T, fixture *fixtureProposeSplitServer) hivemindv1.HiveMindClient {
	t.Helper()

	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)

	srv := grpc.NewServer()
	hivemindv1.RegisterHiveMindServer(srv, fixture)

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.GracefulStop()
		<-serveErrCh
	})

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("startFixtureServer: grpc.NewClient failed: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	return hivemindv1.NewHiveMindClient(conn)
}

// TestGRPCSplitProposer is the exact test name named by issue #16 task-3.2.3's test spec:
// go test ./engine/split/... -run TestGRPCSplitProposer.
func TestGRPCSplitProposer(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		gotRequest := make(chan *hivemindv1.ProposeSplitRequest, 1)
		fixture := &fixtureProposeSplitServer{
			gotRequest: gotRequest,
			resp: &hivemindv1.ProposeSplitResponse{
				Files: []*hivemindv1.SplitFileProposal{
					{
						NewPath: "fixture-part-1.md",
						SectionRanges: []*hivemindv1.SectionRange{
							{Start: 0, End: 12},
						},
					},
					{
						NewPath: "fixture-part-2.md",
						SectionRanges: []*hivemindv1.SectionRange{
							{Start: 12, End: 24},
						},
					},
				},
				RedirectSummary: "split into fixture-part-1.md and fixture-part-2.md",
			},
		}
		client := startFixtureServer(t, fixture)
		proposer := NewGRPCSplitProposer(client, 5*time.Second)

		content := []byte("fixture file content!!!!")
		plan, err := proposer.ProposeSplit(content)
		if err != nil {
			t.Fatalf("ProposeSplit returned unexpected error: %v", err)
		}

		select {
		case req := <-gotRequest:
			if string(req.GetFileContent()) != string(content) {
				t.Errorf("server received FileContent = %q, want %q", req.GetFileContent(), content)
			}
		default:
			t.Fatal("server never received a ProposeSplit request")
		}

		wantPlan := SplitPlan{
			Files: []SplitFileProposal{
				{NewPath: "fixture-part-1.md", SectionRanges: []SectionRange{{Start: 0, End: 12}}},
				{NewPath: "fixture-part-2.md", SectionRanges: []SectionRange{{Start: 12, End: 24}}},
			},
			RedirectSummary: "split into fixture-part-1.md and fixture-part-2.md",
		}
		if !splitPlanEqual(plan, wantPlan) {
			t.Errorf("ProposeSplit plan = %+v, want %+v", plan, wantPlan)
		}
	})

	t.Run("empty_response", func(t *testing.T) {
		fixture := &fixtureProposeSplitServer{
			resp: &hivemindv1.ProposeSplitResponse{},
		}
		client := startFixtureServer(t, fixture)
		proposer := NewGRPCSplitProposer(client, 5*time.Second)

		plan, err := proposer.ProposeSplit([]byte("no split needed"))
		if err != nil {
			t.Fatalf("ProposeSplit returned unexpected error: %v", err)
		}
		if len(plan.Files) != 0 {
			t.Errorf("ProposeSplit Files = %v, want empty", plan.Files)
		}
		if plan.RedirectSummary != "" {
			t.Errorf("ProposeSplit RedirectSummary = %q, want empty", plan.RedirectSummary)
		}
	})

	t.Run("server_error", func(t *testing.T) {
		fixture := &fixtureProposeSplitServer{
			err: status.Error(codes.InvalidArgument, "fileContent must not be empty"),
		}
		client := startFixtureServer(t, fixture)
		proposer := NewGRPCSplitProposer(client, 5*time.Second)

		plan, err := proposer.ProposeSplit(nil)
		if err == nil {
			t.Fatal("ProposeSplit returned nil error, want non-nil")
		}
		if !isZeroSplitPlan(plan) {
			t.Errorf("ProposeSplit plan on error = %+v, want zero value", plan)
		}
		if status.Code(err) != codes.InvalidArgument && !strings.Contains(err.Error(), "InvalidArgument") {
			t.Errorf("ProposeSplit error = %v, want it to surface codes.InvalidArgument", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		fixture := &fixtureProposeSplitServer{
			resp:  &hivemindv1.ProposeSplitResponse{},
			delay: 300 * time.Millisecond,
		}
		client := startFixtureServer(t, fixture)
		proposer := NewGRPCSplitProposer(client, 20*time.Millisecond)

		start := time.Now()
		plan, err := proposer.ProposeSplit([]byte("slow content"))
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("ProposeSplit returned nil error on timeout, want non-nil")
		}
		if !isZeroSplitPlan(plan) {
			t.Errorf("ProposeSplit plan on timeout = %+v, want zero value", plan)
		}
		if status.Code(err) != codes.DeadlineExceeded {
			t.Errorf("ProposeSplit error code = %v, want codes.DeadlineExceeded (err: %v)", status.Code(err), err)
		}
		// Must fail well before the fixture server's artificial 300ms delay elapses --
		// otherwise the client-side timeout isn't actually being enforced.
		if elapsed >= 300*time.Millisecond {
			t.Errorf("ProposeSplit took %v, want it to return well before the server's %v delay (timeout not enforced)", elapsed, 300*time.Millisecond)
		}
	})
}

// isZeroSplitPlan reports whether plan is the zero SplitPlan (no files, no redirect
// summary). SplitPlan embeds a slice field, so it is not comparable with == directly.
func isZeroSplitPlan(plan SplitPlan) bool {
	return len(plan.Files) == 0 && plan.RedirectSummary == ""
}

// splitPlanEqual reports whether two SplitPlan values are deeply equal, field by field. A
// hand-rolled comparison (rather than reflect.DeepEqual) keeps failure messages predictable
// and avoids any ambiguity around nil-vs-empty slices, which SplitPlan's own doc comments
// don't specify either way.
func splitPlanEqual(a, b SplitPlan) bool {
	if a.RedirectSummary != b.RedirectSummary {
		return false
	}
	if len(a.Files) != len(b.Files) {
		return false
	}
	for i := range a.Files {
		if a.Files[i].NewPath != b.Files[i].NewPath {
			return false
		}
		if len(a.Files[i].SectionRanges) != len(b.Files[i].SectionRanges) {
			return false
		}
		for j := range a.Files[i].SectionRanges {
			if a.Files[i].SectionRanges[j] != b.Files[i].SectionRanges[j] {
				return false
			}
		}
	}
	return true
}
