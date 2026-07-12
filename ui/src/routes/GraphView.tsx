import { useEffect, useState } from "react";
import { fetchGraphNeighbors, type MockStatus } from "../api/mockClient";

// Placeholder for the /graph route (subtask 6.1.1, issue #30). Real graph adjacency/traversal
// visualization is subtask 6.1.3 -- this is scaffold only.
export default function GraphView() {
  const [status, setStatus] = useState<MockStatus | null>(null);

  useEffect(() => {
    fetchGraphNeighbors().then(setStatus);
  }, []);

  return (
    <section>
      <h1>Graph</h1>
      <p>Topic/file adjacency visualization will be implemented in a later subtask.</p>
      {status && <p data-testid="graph-status">{status.note}</p>}
    </section>
  );
}
