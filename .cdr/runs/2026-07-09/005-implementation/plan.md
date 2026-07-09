# Plan (task-3.2.3)

1. Create `engine/split/proposer_grpc.go`:
   - Package `split`, import `context`, `fmt`, `time`, `google.golang.org/grpc/codes`,
     `google.golang.org/grpc/status`, `hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"`.
   - `type GRPCSplitProposer struct { client hivemindv1.HiveMindClient; timeout time.Duration }`.
   - `func NewGRPCSplitProposer(client hivemindv1.HiveMindClient, timeout time.Duration) *GRPCSplitProposer`
     -- timeout <= 0 means no client-imposed deadline (caller's ctx / default context.Background
     govern instead); doc comment explains this explicitly.
   - `func (p *GRPCSplitProposer) ProposeSplit(fileContent []byte) (SplitPlan, error)`:
     - Build `ctx` via `context.Background()`, wrap with `context.WithTimeout` if
       `p.timeout > 0`, always `defer cancel()`.
     - Call `p.client.ProposeSplit(ctx, &hivemindv1.ProposeSplitRequest{FileContent: fileContent})`.
     - On error: wrap with `status.FromError` to extract the gRPC code for a descriptive
       message, return `SplitPlan{}, fmt.Errorf(...)`.
     - On success: translate `resp.Files` ([]*SplitFileProposal proto) -> []SplitFileProposal
       (internal), `resp.RedirectSummary` -> `SplitPlan.RedirectSummary`. Each proto
       `SectionRange{Start,End int64}` -> internal `SectionRange{Start,End int}` via explicit
       int64->int conversion (documented as safe given SplitPlan's existing byte-offset
       invariant: offsets are always within a single file's content, far below int64 range on
       all supported platforms, and within `int`'s range on real 64-bit build targets this repo
       targets -- add a doc comment flagging the theoretical 32-bit truncation edge case rather
       than silently ignoring it).
   - Ensure `*GRPCSplitProposer` satisfies `SplitProposer` via a compile-time assertion
     (`var _ SplitProposer = (*GRPCSplitProposer)(nil)`).

2. Create `engine/split/proposer_grpc_test.go`:
   - `package split` (white-box, consistent with repo convention of same-package tests in
     engine/rpc/server_test.go).
   - Test-local fixture server type implementing `hivemindv1.HiveMindServer` (embed
     `hivemindv1.UnimplementedHiveMindServer`, override only `ProposeSplit`) with
     caller-configurable canned response/error/delay, distinct from the real (still
     Unimplemented) production server.
   - Helper to spin up `bufconn.Listener` + `grpc.NewServer()` registering the fixture server,
     `grpc.NewClient`/`grpc.Dial` with `grpc.WithContextDialer(bufDialer)` +
     `grpc.WithTransportCredentials(insecure.NewCredentials())`, return
     `hivemindv1.HiveMindClient` + cleanup func registered via `t.Cleanup`.
   - `TestGRPCSplitProposer` (exact name from issue's test spec) table/subtests:
     a. Happy path: fixture server returns a multi-file plan + redirect summary; assert
        `ProposeSplit` returns the correctly translated `SplitPlan` (files, paths, section
        ranges, redirect summary) with no error.
     b. Empty response: fixture returns zero files; assert empty-but-non-nil-safe SplitPlan,
        no error.
     c. Server error: fixture returns a `status.Error(codes.InvalidArgument, ...)`; assert
        `ProposeSplit` returns non-nil error, zero SplitPlan, and the error surfaces the gRPC
        code (string-contains check).
     d. Timeout: fixture handler sleeps past a short configured client timeout (e.g. 50ms
        handler delay vs 10ms client timeout); assert `ProposeSplit` returns a non-nil error in
        bounded wall-clock time (not a hang) and that the error is a deadline-exceeded style
        error (`status.Code(err) == codes.DeadlineExceeded` or context deadline wrapped).
   - Add `-race`-safe usage (bufconn + goroutine-run server, proper `t.Cleanup` shutdown:
     `srv.GracefulStop()` before test end, `conn.Close()`).

3. Run `gofmt -l`, `go vet ./...`, `go build ./...` from `engine/`.
4. Run `go test ./engine/split/... -run TestGRPCSplitProposer -race -timeout 30s -v`.
5. Do NOT modify `proposer.go`, `proposer_mock.go`, `engine/rpc/server.go`, `proto/*`, or any
   generated file.
6. Update `docs/LLD/rpc.md` Status line only if needed to reflect 3.2.3 completion (check repo
   convention from 3.2.1/3.2.2 commits: prior commits DID update rpc.md's status header --
   confirm and apply consistent update in this commit).
7. Self-consistency check, then one local commit (feat), then handoff.json.
