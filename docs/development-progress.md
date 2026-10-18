# v0.1 implementation progress

This file records verified feature commits and remaining release gates.
The product specification remains the acceptance source of truth.

| Feature | Status | Validation |
| --- | --- | --- |
| Existing CLI baseline | Preserved | `go test ./...` on original Go 1.22 |
| M0 engineering and contracts | Implemented | Go 1.26.9, public module, policies, generated OpenAPI/types, actual response conformance and drift checks |
| Persistent domain and records | Implemented | Naming negative cases; SQLite rollback, active-run uniqueness, terminal immutability, reopen and permissions; race tests and vet |
| M1 single-agent vertical flow | Implemented; live provider gate pending | Bounded Anthropic SSE, schema-validated host tools, persistent HTTP/SSE, authentication and demo |
| M2 names, routing and collaboration | Implemented | Renaming preserves identity; longest normalized names; semantic quote validation; finite parallel/serial workflows |
| M3 research, code analysis and artifacts | Implemented; live service gate pending | All three browser flows, real file tools, HTTP fixtures, captured sources, immutable versions, feedback and Markdown export |
| M4 recovery, security and interaction | Implemented | Cancellation, lost-response idempotency, long history, replay, restart, queue policy revalidation, commit-failure injection, backup/restore; WCAG automatic checks |
| M5 release candidate | Built; publication blocked | Clean checkout reproduced; macOS arm64 and Linux amd64 candidate smoke; notices/checksums; Go 1.26.9 security gate passed locally; live provider and remote CI still pending |

## Toolchain update on 2026-10-18

- Upgraded both Go modules to require Go 1.26.9; CI covers 1.26 and stable.
- `go mod verify`, `make test` and `make security` passed on the new toolchain.
- The scanner reports no reachable vulnerabilities; required-module-only
  findings remain documented in [the scan record](security-scan.md).

## Verified locally on 2026-10-16

- `make test`: Go vet/race/shuffle and frontend lint/types/tests, plus development-process ownership and license tests.
- `make check-contract`: generated OpenAPI/frontend types do not drift.
- `PLAYWRIGHT_CHANNEL=chrome make e2e`: real Go service, SQLite and native browser; all scenarios, sources, artifacts, retry/cancel, refresh, mobile navigation and axe WCAG checks.
- `go run ./cmd/smoke`: real process lifecycle, Research, captured evidence, export and stopped-database backup/restore.
- A separate clean local clone repeated setup, checks and browser tests without untracked project files.
- macOS arm64 archive executed with the same smoke. Linux amd64 archive executed inside an isolated, read-only, network-disabled Docker container on the local Linux arm64 engine; this is emulated amd64 execution, not a performance result.
- Linux container additionally passed `go test -race ./...` using the locally available Go 1.25.13 image. Application binaries were built with Go 1.25.14.
- Frontend production dependency audit: zero findings. Common-secret scan: passed.

## Remaining release gates

1. Anthropic and Brave credentials are absent in this session. Protocol/fixture tests pass, but paid live calls, actual account/model availability and quality are not verified.
2. GitHub-hosted CI has not run because commits have not been pushed. No tag, GitHub Release or remote publication was created.

All feature changes were committed separately after their relevant tests. Local
build artifacts are in ignored `dist/`; do not treat their existence as release
approval; live-provider validation and GitHub-hosted CI remain incomplete.
