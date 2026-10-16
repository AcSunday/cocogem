# v0.1 implementation progress

This file records verified feature commits and remaining release gates.
The product specification remains the acceptance source of truth.

| Feature | Status | Validation |
| --- | --- | --- |
| Existing CLI baseline | Preserved | `go test ./...` on original Go 1.22 |
| M0 engineering and contracts | In progress | Go 1.25.14, local Git, public module and policies |
| Persistent domain and records | Implemented | Naming negative cases; SQLite rollback, active-run uniqueness, terminal immutability, reopen and permissions; race tests and vet |
| M1 single-agent vertical flow | Pending | |
| M2 names, routing and collaboration | Pending | |
| M3 research, code analysis and artifacts | Pending | |
| M4 recovery, security and interaction | Pending | |
| M5 release candidate | Pending | |

External verification gates: Anthropic/Brave credentials are not configured in
this session; real-provider smoke tests and Linux platform execution need
separate evidence. Implementations and deterministic tests can proceed.
