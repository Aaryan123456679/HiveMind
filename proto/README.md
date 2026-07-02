# proto/

Shared `.proto` definitions for the gRPC boundary between the Go storage
engine (`engine/rpc`) and the Python ML/agent service (`agents/`).

Planned services: `Catalog`, `Graph`, `Search`, `ProposeSplit`. Generated
Go/Python stubs are not checked in — regenerate via `protoc` (tooling to be
added alongside the first real `.proto` file).
