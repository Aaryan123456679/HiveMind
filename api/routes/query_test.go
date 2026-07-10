package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeQueryPipeline is a test double for QueryPipeline, standing in for the real gRPC client
// this subtask's test spec says is "mocked at the gRPC boundary".
type fakeQueryPipeline struct {
	result      QueryResult
	err         error
	calledQuery string
	calledHist  []string
	callCount   int
}

func (f *fakeQueryPipeline) RunQuery(ctx context.Context, query string, history []string) (QueryResult, error) {
	f.callCount++
	f.calledQuery = query
	f.calledHist = history
	if f.err != nil {
		return QueryResult{}, f.err
	}
	return f.result, nil
}

func TestQueryRoute(t *testing.T) {
	t.Run("happy path returns 200 with pipeline result", func(t *testing.T) {
		pipeline := &fakeQueryPipeline{
			result: QueryResult{
				Answer:    "Invoice 4521 was disputed [billing/InvoiceDisputes.md].",
				Citations: []string{"billing/InvoiceDisputes.md"},
			},
		}
		handler := NewQueryHandler(pipeline)

		body, err := json.Marshal(QueryRequest{Query: "what happened with invoice 4521?", History: []string{"prior turn"}})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}
		if pipeline.callCount != 1 {
			t.Fatalf("expected pipeline.RunQuery to be called exactly once, got %d", pipeline.callCount)
		}
		if pipeline.calledQuery != "what happened with invoice 4521?" {
			t.Fatalf("unexpected query forwarded to pipeline: %q", pipeline.calledQuery)
		}
		if len(pipeline.calledHist) != 1 || pipeline.calledHist[0] != "prior turn" {
			t.Fatalf("unexpected history forwarded to pipeline: %v", pipeline.calledHist)
		}

		var got QueryResult
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if got.Answer != pipeline.result.Answer {
			t.Fatalf("unexpected answer: got %q, want %q", got.Answer, pipeline.result.Answer)
		}
		if len(got.Citations) != 1 || got.Citations[0] != "billing/InvoiceDisputes.md" {
			t.Fatalf("unexpected citations: %v", got.Citations)
		}
	})

	t.Run("GET is rejected with 405", func(t *testing.T) {
		pipeline := &fakeQueryPipeline{}
		handler := NewQueryHandler(pipeline)

		req := httptest.NewRequest(http.MethodGet, "/query", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status 405, got %d", rec.Code)
		}
		if pipeline.callCount != 0 {
			t.Fatalf("expected pipeline.RunQuery to not be called, got %d calls", pipeline.callCount)
		}
	})

	t.Run("empty query is rejected with 400", func(t *testing.T) {
		pipeline := &fakeQueryPipeline{}
		handler := NewQueryHandler(pipeline)

		body, _ := json.Marshal(QueryRequest{Query: "   "})
		req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
		if pipeline.callCount != 0 {
			t.Fatalf("expected pipeline.RunQuery to not be called, got %d calls", pipeline.callCount)
		}
	})

	t.Run("malformed JSON body is rejected with 400", func(t *testing.T) {
		pipeline := &fakeQueryPipeline{}
		handler := NewQueryHandler(pipeline)

		req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader([]byte("not json")))
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("pipeline error is surfaced as 500", func(t *testing.T) {
		pipeline := &fakeQueryPipeline{err: errors.New("query pipeline gRPC wiring not yet implemented")}
		handler := NewQueryHandler(pipeline)

		body, _ := json.Marshal(QueryRequest{Query: "a valid query"})
		req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected status 500, got %d", rec.Code)
		}
	})

	t.Run("RegisterRoutes wires /query onto the mux", func(t *testing.T) {
		pipeline := &fakeQueryPipeline{result: QueryResult{Answer: "ok", Citations: []string{}}}
		mux := http.NewServeMux()
		RegisterRoutes(mux, pipeline)

		body, _ := json.Marshal(QueryRequest{Query: "hello"})
		req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 via mux, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	})
}
