// Command api is the HTTP gateway for HiveMind: auth, request routing to the
// storage engine (engine/rpc) and the Python ML/agent service.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/Aaryan123456679/HiveMind/api/routes"
)

// notImplementedPipeline is a stand-in routes.QueryPipeline implementation.
//
// Per issue #25 subtask 4.6.1's disclosed decision (see api/routes/query.go's QueryPipeline
// doc comment): proto/hivemind.proto does not yet define an RPC for invoking the Python query
// pipeline (agents/query/pipeline.py's run_query_pipeline), so no real gRPC client to that
// process is built here. This stand-in keeps /query structurally and HTTP-reachable (the
// route is registered and answers requests) while making the gap explicit via its error
// message, rather than fabricating a fake network call.
type notImplementedPipeline struct{}

func (notImplementedPipeline) RunQuery(ctx context.Context, query string, history []string) (routes.QueryResult, error) {
	return routes.QueryResult{}, errors.New(
		"query pipeline gRPC wiring not yet implemented (see issue #25 subtask 4.6.1's " +
			"disclosed gap: proto/hivemind.proto has no RPC for the Python query pipeline yet)",
	)
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	routes.RegisterRoutes(mux, notImplementedPipeline{})
	return mux
}

func main() {
	addr := ":" + port()
	log.Printf("api gateway listening on %s", addr)
	if err := http.ListenAndServe(addr, newMux()); err != nil {
		log.Fatal(err)
	}
}

func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}
