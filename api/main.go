// Command api is the HTTP gateway for HiveMind: auth, request routing to the
// storage engine (engine/rpc) and the Python ML/agent service.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Aaryan123456679/HiveMind/api/queryclient"
	"github.com/Aaryan123456679/HiveMind/api/routes"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// notImplementedPipeline is a stand-in routes.QueryPipeline implementation, used only when
// QUERY_PIPELINE_ADDR (see queryPipeline below) is unset -- e.g. local/dev environments that
// have not stood up a real agents/query/server.py process. It is no longer the sole/default
// implementation as of GitHub issue #56 subtask 4.6.3.2 (see queryPipeline's doc comment):
// production deployments that set QUERY_PIPELINE_ADDR get a real
// queryclient.GRPCQueryPipeline instead.
type notImplementedPipeline struct{}

func (notImplementedPipeline) RunQuery(ctx context.Context, query string, history []string) (routes.QueryResult, error) {
	return routes.QueryResult{}, errors.New(
		"query pipeline gRPC wiring not configured (set QUERY_PIPELINE_ADDR to a running " +
			"agents/query/server.py process's address; see GitHub issue #56 subtask 4.6.3.2)",
	)
}

// queryPipeline returns the routes.QueryPipeline implementation newMux wires /query to.
//
// Per GitHub issue #56 subtask 4.6.3.2 (closing F-4.6.1-1, api/routes/query.go's
// QueryPipeline doc comment): if QUERY_PIPELINE_ADDR is set, this dials it as a real gRPC
// connection and returns a real queryclient.GRPCQueryPipeline calling the RunQuery RPC
// (proto/hivemind.proto) against a running agents/query/server.py process -- genuine
// end-user /query requests now execute the full Python query pipeline against live engine
// data, per the issue's acceptance criteria. If QUERY_PIPELINE_ADDR is unset (e.g. no such
// process has been stood up yet in this environment), notImplementedPipeline is used
// instead, exactly as before this subtask, so /query remains structurally reachable rather
// than main() refusing to start.
//
// grpc.NewClient (not grpc.Dial) is used deliberately: it does not block or fail at startup
// even if the target address isn't accepting connections yet (lazy connection, matching
// grpc-go's current non-blocking-by-default guidance) -- the same reason
// engine/split/proposer_grpc.go's precedent leaves connection establishment entirely to its
// caller rather than eagerly validating reachability here.
func queryPipeline() (routes.QueryPipeline, func() error) {
	addr := os.Getenv("QUERY_PIPELINE_ADDR")
	if addr == "" {
		return notImplementedPipeline{}, func() error { return nil }
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("api gateway: QUERY_PIPELINE_ADDR=%q: grpc.NewClient failed (%v); falling back to notImplementedPipeline", addr, err)
		return notImplementedPipeline{}, func() error { return nil }
	}

	client := hivemindv1.NewHiveMindClient(conn)
	return queryclient.NewGRPCQueryPipeline(client, 30*time.Second), conn.Close
}

func newMux(pipeline routes.QueryPipeline) *http.ServeMux {
	mux := http.NewServeMux()
	routes.RegisterRoutes(mux, pipeline)
	return mux
}

func main() {
	pipeline, closePipeline := queryPipeline()
	defer func() {
		if err := closePipeline(); err != nil {
			log.Printf("api gateway: closing query pipeline connection: %v", err)
		}
	}()

	addr := ":" + port()
	log.Printf("api gateway listening on %s", addr)
	if err := http.ListenAndServe(addr, newMux(pipeline)); err != nil {
		log.Fatal(err)
	}
}

func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}
