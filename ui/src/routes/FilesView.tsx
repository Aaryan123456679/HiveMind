import { useEffect, useState } from "react";
import { fetchFilesCatalog, type MockStatus } from "../api/mockClient";

// Placeholder for the /files route (subtask 6.1.1, issue #30). Real catalog listing is
// subtask 6.1.4, where this will be superseded by a combined FilesAdminView.tsx per the
// issue's "Files/admin view" subtask description -- this is scaffold only.
export default function FilesView() {
  const [status, setStatus] = useState<MockStatus | null>(null);

  useEffect(() => {
    fetchFilesCatalog().then(setStatus);
  }, []);

  return (
    <section>
      <h1>Files</h1>
      <p>Catalog browsing will be implemented in a later subtask.</p>
      {status && <p data-testid="files-status">{status.note}</p>}
    </section>
  );
}
