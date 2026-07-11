package queryclient

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

	"github.com/Aaryan123456679/HiveMind/api/routes"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// fixtureRunQueryServer is a test-local, package-private gRPC server double for
// hivemindv1.HiveMindServer used to exercise GRPCQueryPipeline over a real gRPC connection
// without requiring a real running agents/query/server.py process. Mirrors
// engine/split/proposer_grpc_test.go's fixtureProposeSplitServer exactly.
type fixtureRunQueryServer struct {
	hivemindv1.UnimplementedHiveMindServer

	resp  *hivemindv1.RunQueryResponse
	err   error
	delay time.Duration

	gotRequest chan *hivemindv1.RunQueryRequest
}

func (f *fixtureRunQueryServer) RunQuery(ctx context.Context, req *hivemindv1.RunQueryRequest) (*hivemindv1.RunQueryResponse, error) {
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
// registers fixture as its hivemindv1.HiveMindServer, and returns a real
// hivemindv1.HiveMindClient dialed against it over real (loopback, in-memory) gRPC.
func startFixtureServer(t *testing.T, fixture hivemindv1.HiveMindServer) hivemindv1.HiveMindClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
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

func TestGRPCQueryPipeline(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		gotRequest := make(chan *hivemindv1.RunQueryRequest, 1)
		fixture := &fixtureRunQueryServer{
			gotRequest: gotRequest,
			resp: &hivemindv1.RunQueryResponse{
				Answer:    "Invoice 4521 was disputed for a duplicate charge.",
				Citations: []string{"billing/InvoiceDisputes.md"},
			},
		}
		client := startFixtureServer(t, fixture)
		pipeline := NewGRPCQueryPipeline(client, 5*time.Second)

		result, err := pipeline.RunQuery(context.Background(), "What happened with invoice 4521?", []string{"earlier turn"})
		if err != nil {
			t.Fatalf("RunQuery returned unexpected error: %v", err)
		}

		select {
		case req := <-gotRequest:
			if req.GetQuery() != "What happened with invoice 4521?" {
				t.Errorf("server received Query = %q, want %q", req.GetQuery(), "What happened with invoice 4521?")
			}
			if len(req.GetHistory()) != 1 || req.GetHistory()[0] != "earlier turn" {
				t.Errorf("server received History = %v, want [\"earlier turn\"]", req.GetHistory())
			}
		default:
			t.Fatal("server never received a RunQuery request")
		}

		want := routes.QueryResult{
			Answer:    "Invoice 4521 was disputed for a duplicate charge.",
			Citations: []string{"billing/InvoiceDisputes.md"},
		}
		if result.Answer != want.Answer || len(result.Citations) != len(want.Citations) || result.Citations[0] != want.Citations[0] {
			t.Errorf("RunQuery result = %+v, want %+v", result, want)
		}
	})

	t.Run("empty_response", func(t *testing.T) {
		fixture := &fixtureRunQueryServer{
			resp: &hivemindv1.RunQueryResponse{},
		}
		client := startFixtureServer(t, fixture)
		pipeline := NewGRPCQueryPipeline(client, 5*time.Second)

		result, err := pipeline.RunQuery(context.Background(), "no matches expected", nil)
		if err != nil {
			t.Fatalf("RunQuery returned unexpected error: %v", err)
		}
		if result.Answer != "" || len(result.Citations) != 0 {
			t.Errorf("RunQuery result = %+v, want zero value", result)
		}
	})

	t.Run("server_error", func(t *testing.T) {
		fixture := &fixtureRunQueryServer{
			err: status.Error(codes.Internal, "pipeline crashed"),
		}
		client := startFixtureServer(t, fixture)
		pipeline := NewGRPCQueryPipeline(client, 5*time.Second)

		result, err := pipeline.RunQuery(context.Background(), "boom", nil)
		if err == nil {
			t.Fatal("RunQuery returned nil error, want non-nil")
		}
		if result.Answer != "" || len(result.Citations) != 0 {
			t.Errorf("RunQuery result on error = %+v, want zero value", result)
		}
		if !strings.Contains(err.Error(), "pipeline crashed") {
			t.Errorf("RunQuery error = %v, want it to mention the underlying server error", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		fixture := &fixtureRunQueryServer{
			resp:  &hivemindv1.RunQueryResponse{},
			delay: 300 * time.Millisecond,
		}
		client := startFixtureServer(t, fixture)
		pipeline := NewGRPCQueryPipeline(client, 20*time.Millisecond)

		start := time.Now()
		result, err := pipeline.RunQuery(context.Background(), "slow query", nil)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("RunQuery returned nil error on timeout, want non-nil")
		}
		if result.Answer != "" || len(result.Citations) != 0 {
			t.Errorf("RunQuery result on timeout = %+v, want zero value", result)
		}
		if status.Code(err) != codes.DeadlineExceeded {
			t.Errorf("RunQuery error code = %v, want codes.DeadlineExceeded (err: %v)", status.Code(err), err)
		}
		if elapsed >= 300*time.Millisecond {
			t.Errorf("RunQuery took %v, want it to return well before the server's %v delay (timeout not enforced)", elapsed, 300*time.Millisecond)
		}
	})
}
