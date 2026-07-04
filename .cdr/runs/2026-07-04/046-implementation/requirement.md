# Requirement — subtask 1.4.1 (issue #4, Phase 1 Storage core)

Title: Content store layout: content/<fileID>.v1.md create/write path, WAL-before-apply

Acceptance criteria: Creating a new topic file writes `content/<fileID>.v1.md` and logs the
corresponding catalog mutation to the WAL before the file is considered committed.

Test spec: `go test ./engine/catalog/... -run TestContentCreate -race`: create file, assert WAL
record precedes catalog visibility, assert content bytes on disk match input.

Impacted modules: `engine/catalog/content.go`, `engine/catalog/content_test.go` (new files —
verified they do not already exist).

Context: first of 4 subtasks under issue #4. task-1.3 (WAL, issue #3) is fully verified/merged;
`engine/wal/` (Writer/Replay/Checkpoint) is available to build on, read-only dependency (no
engine/wal changes expected).
