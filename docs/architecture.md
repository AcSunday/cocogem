# v0.1 architecture decisions

## Local service and explicit ownership

The existing root CLI stays compatible. The Web server is a separate command,
using `net/http`, constructor injection and `internal` packages. Workspace
identity and the current workflow role are separate. Runs snapshot member
configuration so edits do not rewrite history.

## SQLite transactions

Keep `modernc.org/sqlite` v1.59.0 (BSD-3-Clause) pinned while upgrading the
toolchain to Go 1.26.9. A newer toolchain alone does not require a driver
upgrade. The pure Go driver
allows supported release targets without a cross-compilation C toolchain.
`database/sql` stays the query boundary; no ORM or external database service.

SQLite owns foreign keys, unique handles, idempotency and one-active-run
constraints. Stable scalar keys and status are SQL columns; versioned object
payloads use validated JSON. All state and corresponding events commit in the
same transaction. The process notification channel only wakes replay readers;
it is never the source of truth.

Contract tests use `github.com/santhosh-tekuri/jsonschema/v6` v6.0.3
(Apache-2.0) to validate real handler responses against generated OpenAPI
schemas. A maintained JSON Schema implementation avoids a partial custom
validator. The standard library supplies JSON parsing, not schema validation.

## Provider and event boundaries

The first provider uses Anthropic Messages API. The Go host executes only
validated, bounded tools. Provider SSE names stop inside the adapter. Native
CLI execution is retained as the existing command, not exposed as a Web tool.

Use `golang.org/x/net/html` v0.58.0 (BSD-3-Clause)
for HTML parsing. The standard library does not provide an HTML parser.
The transport validates public addresses at connection time, does not inherit
proxies, and revalidates redirects. Search credentials never follow redirects.
File reads use `os.Root`, explicit symlink rejection and bounded captures.

## UI transport

REST handles commands and snapshots; SSE carries persisted per-run events.
Readers capture a wakeup channel before reading SQLite so a commit cannot
fall between history replay and subscription. Clients deduplicate sequence IDs.

## Bounded collaboration

Built-in workflows have finite step dependencies. Agent text cannot authorize
another invocation. Natural-language routing proposes typed assignments;
the host validates IDs, scope and the accepted workflow before dispatch.
