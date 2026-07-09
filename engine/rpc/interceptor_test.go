package rpc

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// fakeRecorder is a test-only Recorder that appends every RPCMetric it receives to an
// internal slice, guarded by a mutex since LatencyInterceptor may be invoked concurrently
// by the gRPC runtime for concurrent unary calls.
type fakeRecorder struct {
	mu      sync.Mutex
	metrics []RPCMetric
}

func (r *fakeRecorder) Record(m RPCMetric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = append(r.metrics, m)
}

func (r *fakeRecorder) snapshot() []RPCMetric {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RPCMetric, len(r.metrics))
	copy(out, r.metrics)
	return out
}

// startInterceptedServer spins up a real grpc.Server, wired with
// grpc.UnaryInterceptor(LatencyInterceptor(...)), bound to an in-process bufconn listener,
// serving the given production *Server (srv) as the registered hivemindv1.HiveMindServer.
// It returns a real hivemindv1.HiveMindClient dialed against it over a real (in-memory)
// gRPC connection -- mirroring engine/split/proposer_grpc_test.go's genuine-wire-transport
// test style (real grpc.Server + real grpc.NewClient, not direct handler-function calls).
func startInterceptedServer(t *testing.T, srv *Server, rec Recorder) hivemindv1.HiveMindClient {
	t.Helper()

	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)

	gsrv := grpc.NewServer(grpc.UnaryInterceptor(LatencyInterceptor(WithRecorder(rec))))
	hivemindv1.RegisterHiveMindServer(gsrv, srv)

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- gsrv.Serve(lis)
	}()
	t.Cleanup(func() {
		gsrv.GracefulStop()
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
		t.Fatalf("startInterceptedServer: grpc.NewClient failed: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	return hivemindv1.NewHiveMindClient(conn)
}

// TestLatencyInterceptor is the exact test name named by issue #16 task-3.2.4's test spec:
// go test ./engine/rpc/... -run TestLatencyInterceptor. It exercises LatencyInterceptor
// over a REAL gRPC connection (bufconn + real grpc.Server + real dialed client), not via
// direct interceptor-function calls, matching this repo's established genuine-wire-
// transport test convention (engine/split/proposer_grpc_test.go, task-3.2.3).
func TestLatencyInterceptor(t *testing.T) {
	t.Run("success_call_records_latency_and_leaves_response_unchanged", func(t *testing.T) {
		f := newFixture(t)
		rec := &fakeRecorder{}
		client := startInterceptedServer(t, f.srv, rec)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, err := client.GetFile(ctx, &hivemindv1.GetFileRequest{FileId: f.alphaID})
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		wantContent := "# Alpha\n\nintro text\n\n## Alpha Details\n\nmore text\n"
		if string(resp.GetContent()) != wantContent {
			t.Errorf("GetFile content = %q, want %q (interceptor must not alter response)", resp.GetContent(), wantContent)
		}
		if resp.GetVersion() != 1 {
			t.Errorf("GetFile version = %d, want 1", resp.GetVersion())
		}

		metrics := rec.snapshot()
		if len(metrics) != 1 {
			t.Fatalf("got %d recorded metrics, want exactly 1", len(metrics))
		}
		m := metrics[0]
		if m.Method != "/hivemind.v1.HiveMind/GetFile" {
			t.Errorf("Method = %q, want /hivemind.v1.HiveMind/GetFile", m.Method)
		}
		if m.Duration <= 0 {
			t.Errorf("Duration = %v, want > 0", m.Duration)
		}
		if m.Code != codes.OK {
			t.Errorf("Code = %v, want OK", m.Code)
		}
		if m.RequestBytes <= 0 {
			t.Errorf("RequestBytes = %d, want > 0 (non-empty GetFileRequest)", m.RequestBytes)
		}
		if m.ResponseBytes <= 0 {
			t.Errorf("ResponseBytes = %d, want > 0 (non-empty GetFileResponse)", m.ResponseBytes)
		}
		if m.Err != nil {
			t.Errorf("Err = %v, want nil on success", m.Err)
		}
	})

	t.Run("error_call_records_latency_with_error_code_and_leaves_error_unchanged", func(t *testing.T) {
		f := newFixture(t)
		rec := &fakeRecorder{}
		client := startInterceptedServer(t, f.srv, rec)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.GetFile(ctx, &hivemindv1.GetFileRequest{FileId: 99999})
		if err == nil {
			t.Fatalf("GetFile(nonexistent): got nil error, want NotFound")
		}
		if status.Code(err) != codes.NotFound {
			t.Errorf("GetFile(nonexistent) code = %v, want NotFound (interceptor must not alter error semantics)", status.Code(err))
		}

		metrics := rec.snapshot()
		if len(metrics) != 1 {
			t.Fatalf("got %d recorded metrics, want exactly 1", len(metrics))
		}
		m := metrics[0]
		if m.Duration <= 0 {
			t.Errorf("Duration = %v, want > 0", m.Duration)
		}
		if m.Code != codes.NotFound {
			t.Errorf("Code = %v, want NotFound", m.Code)
		}
		if m.ResponseBytes != 0 {
			t.Errorf("ResponseBytes = %d, want 0 on error", m.ResponseBytes)
		}
		if m.Err == nil {
			t.Errorf("Err = nil, want non-nil on error path")
		}
	})

	t.Run("concurrent_calls_each_produce_exactly_one_record_race_clean", func(t *testing.T) {
		f := newFixture(t)
		rec := &fakeRecorder{}
		client := startInterceptedServer(t, f.srv, rec)

		const n = 32
		var wg sync.WaitGroup
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := client.GetFile(ctx, &hivemindv1.GetFileRequest{FileId: f.alphaID}); err != nil {
					t.Errorf("concurrent GetFile: %v", err)
				}
			}()
		}
		wg.Wait()

		metrics := rec.snapshot()
		if len(metrics) != n {
			t.Fatalf("got %d recorded metrics, want exactly %d", len(metrics), n)
		}
		for _, m := range metrics {
			if m.Code != codes.OK {
				t.Errorf("concurrent call Code = %v, want OK", m.Code)
			}
			if m.Duration <= 0 {
				t.Errorf("concurrent call Duration = %v, want > 0", m.Duration)
			}
		}
	})
}
