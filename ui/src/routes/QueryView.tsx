import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";

// The /query view (subtask 6.1.2, GitHub issue #30): submits a user's query to api/'s /query
// HTTP gateway route and renders the synthesized answer plus cited file paths.
//
// Wire contract (see api/routes/query.go's QueryRequest/QueryResult):
//   POST /query  { "query": string, "history"?: string[] }
//   200 ->       { "answer": string, "citations": string[] }
//   4xx/5xx ->   plain-text error body (http.Error)
//
// This is the first real network call in ui/ -- subtask 6.1.1's ui/src/api/mockClient.ts
// stubs never touched fetch/XHR, and no other HTTP client convention exists yet anywhere in
// ui/src/ (see this run's architecture-discovery.md). Deliberately calls the browser `fetch`
// API directly against the relative path "/query" (same-origin) rather than introducing a new
// client library or base-URL convention, since api/routes/query.go registers the handler at
// exactly "/query" with no path prefix and deploy/ reverse-proxy wiring does not exist yet.
export interface QueryResult {
  answer: string;
  citations: string[];
}

type Status = "idle" | "loading" | "error" | "success";

export default function QueryView() {
  const [queryText, setQueryText] = useState("");
  const [status, setStatus] = useState<Status>("idle");
  const [result, setResult] = useState<QueryResult | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const trimmed = queryText.trim();
    if (trimmed === "") {
      // Mirrors api/routes/query.go's own "query must not be empty" 400 guard -- no point
      // making the round trip client-side.
      return;
    }

    setStatus("loading");
    setErrorMessage(null);
    setResult(null);

    try {
      const response = await fetch("/query", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ query: trimmed }),
      });

      if (!response.ok) {
        const message = await response.text();
        setErrorMessage(message || `request failed with status ${response.status}`);
        setStatus("error");
        return;
      }

      const data = (await response.json()) as QueryResult;
      setResult(data);
      setStatus("success");
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : "unknown error");
      setStatus("error");
    }
  }

  return (
    <section>
      <h1>Query</h1>
      <form data-testid="query-form" onSubmit={handleSubmit}>
        <label htmlFor="query-input">Query</label>
        <input
          id="query-input"
          aria-label="Query"
          value={queryText}
          onChange={(event) => setQueryText(event.target.value)}
        />
        <button type="submit" disabled={status === "loading"}>
          Submit
        </button>
      </form>

      {status === "loading" && <p data-testid="query-loading">Loading...</p>}

      {status === "error" && errorMessage && (
        <p data-testid="query-error" role="alert">
          {errorMessage}
        </p>
      )}

      {status === "success" && result && (
        <div data-testid="query-result">
          <p data-testid="query-answer">{result.answer}</p>
          {result.citations.length > 0 ? (
            <ul data-testid="query-citations">
              {result.citations.map((citation) => (
                <li key={citation}>
                  <Link to={`/files?path=${encodeURIComponent(citation)}`}>{citation}</Link>
                </li>
              ))}
            </ul>
          ) : (
            <p data-testid="query-no-citations">No cited file paths were returned.</p>
          )}
        </div>
      )}
    </section>
  );
}
