import { useEffect, useState } from "react";
import { fetchIngestStatus, type MockStatus } from "../api/mockClient";

// Placeholder for the /ingest route (subtask 6.1.1, issue #30). Real document-upload UI is
// out of scope for this subtask -- this is scaffold only.
export default function IngestView() {
  const [status, setStatus] = useState<MockStatus | null>(null);

  useEffect(() => {
    fetchIngestStatus().then(setStatus);
  }, []);

  return (
    <section>
      <h1>Ingest</h1>
      <p>Document upload and ingestion status will be implemented in a later subtask.</p>
      {status && <p data-testid="ingest-status">{status.note}</p>}
    </section>
  );
}
