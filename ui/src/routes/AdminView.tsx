import { useEffect, useState } from "react";
import { fetchAdminStats, type MockStatus } from "../api/mockClient";

// Placeholder for the /admin route (subtask 6.1.1, issue #30). Real corpus-level stats and
// ingestion status are subtask 6.1.4, where this will be superseded by a combined
// FilesAdminView.tsx per the issue's "Files/admin view" subtask description -- this is
// scaffold only.
export default function AdminView() {
  const [status, setStatus] = useState<MockStatus | null>(null);

  useEffect(() => {
    fetchAdminStats().then(setStatus);
  }, []);

  return (
    <section>
      <h1>Admin</h1>
      <p>Corpus-level stats and ingestion status will be implemented in a later subtask.</p>
      {status && <p data-testid="admin-status">{status.note}</p>}
    </section>
  );
}
