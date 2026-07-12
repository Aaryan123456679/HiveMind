import { useEffect, useState } from "react";
import { fetchQueryResult, type MockStatus } from "../api/mockClient";

// Placeholder for the /query route (subtask 6.1.1, issue #30). Real query submission +
// synthesized-answer/citation rendering is subtask 6.1.2 -- this is scaffold only.
export default function QueryView() {
  const [status, setStatus] = useState<MockStatus | null>(null);

  useEffect(() => {
    fetchQueryResult().then(setStatus);
  }, []);

  return (
    <section>
      <h1>Query</h1>
      <p>Query submission and synthesized answers will be implemented in a later subtask.</p>
      {status && <p data-testid="query-status">{status.note}</p>}
    </section>
  );
}
