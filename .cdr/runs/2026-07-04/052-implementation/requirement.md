# Subtask 1.4.3 (issue #4) — Append path + sizeBytes update + threshold-check stub

Acceptance criteria: Appending markdown content to an existing file updates the
stored content and the catalog's `sizeBytes` field; when the resulting size
exceeds the configured split threshold (~8KB/~2000 tokens default), a
threshold-crossing signal is emitted (actual auto-split execution is out of
scope until Epic 2B).

Test spec: `go test ./engine/catalog/... -run TestContentAppend`: append
repeatedly, assert `sizeBytes` tracks content length and the threshold signal
fires exactly once on crossing the configured limit.

Impacted modules: engine/catalog/content.go, engine/catalog/content_test.go.
