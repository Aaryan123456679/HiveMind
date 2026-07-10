// Package routes implements the api/ HTTP gateway's routes.
//
// Per issue #25 subtask 4.6.1 and docs/HLD.md's "API Gateway (Go, api/) -> gRPC -> ..."
// architecture description, this is the first route package added to api/ (api/main.go was
// previously an empty `func main() {}` with no router). See query.go's own doc comment for
// the /query route and the disclosed gRPC-boundary gap.
package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// QueryRequest is the JSON request body accepted by the /query route.
type QueryRequest struct {
	Query   string   `json:"query"`
	History []string `json:"history,omitempty"`
}

// QueryResult is the JSON response body returned by the /query route, mirroring the Python
// query pipeline's agents/query/pipeline.py QueryPipelineResult.synthesis shape
// (SynthesizerResult.answer / .citations).
type QueryResult struct {
	Answer    string   `json:"answer"`
	Citations []string `json:"citations"`
}

// QueryPipeline is the boundary this package's /query route depends on to run the full
// query -> intent_refiner -> topic_selector -> synthesizer -> answer chain implemented in
// agents/query/pipeline.py's run_query_pipeline.
//
// Real-wiring gap -- disclosed choice
// ------------------------------------
// proto/hivemind.proto's `service HiveMind` does not define an RPC for invoking the Python
// query pipeline (it defines PutSegment/GetFile/ReadPartial/GraphNeighbors/SearchCandidates/
// ProposeSplit/PutEdge/PutEntity/LookupEntity only) -- extending the shared proto contract and
// regenerating both Go and Python stubs is out of scope for this subtask. This interface is
// the seam a later subtask's real gRPC (or HTTP) client implementation should satisfy; this
// subtask's test spec ("agents/query pipeline mocked at the gRPC boundary") mocks exactly
// this interface. api/main.go currently wires in a stand-in implementation that returns a
// clear "not yet implemented" error rather than a fabricated network call.
type QueryPipeline interface {
	RunQuery(ctx context.Context, query string, history []string) (QueryResult, error)
}

// NewQueryHandler returns an http.HandlerFunc for the /query route backed by pipeline.
//
// Behavior:
//   - Only POST is accepted; any other method returns 405.
//   - The request body must be valid JSON matching QueryRequest; malformed JSON returns 400.
//   - Query must not be empty (after trimming whitespace); returns 400 if it is.
//   - pipeline.RunQuery's result is JSON-encoded as QueryResult with a 200 status.
//   - Any error from pipeline.RunQuery is surfaced as a 500 with the error's message as the
//     body (no internal detail beyond the error string is exposed).
func NewQueryHandler(pipeline QueryPipeline) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed: /query only accepts POST", http.StatusMethodNotAllowed)
			return
		}

		var req QueryRequest
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(req.Query) == "" {
			http.Error(w, "query must not be empty", http.StatusBadRequest)
			return
		}

		result, err := pipeline.RunQuery(r.Context(), req.Query, req.History)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(result); err != nil {
			// Encoding failures at this point cannot change the already-written status
			// code; nothing more can be done here beyond not panicking.
			return
		}
	}
}

// RegisterRoutes registers every route this package implements onto mux, backed by pipeline.
// Called from api/main.go's server setup.
func RegisterRoutes(mux *http.ServeMux, pipeline QueryPipeline) {
	mux.HandleFunc("/query", NewQueryHandler(pipeline))
}
