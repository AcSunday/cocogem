# v0.1 architecture decisions

## Local service and explicit ownership

The existing root CLI stays compatible. The Web server is a separate command,
using `net/http`, constructor injection and `internal` packages. Workspace
identity and the current workflow role are separate. Runs snapshot member
configuration so edits do not rewrite history.

## SQLite transactions

Use `modernc.org/sqlite` v1.59.0 (BSD-3-Clause), the latest checked version
compatible with Go 1.25. Later v1.60.1 requires Go 1.26. The pure Go driver
allows supported release targets without a cross-compilation C toolchain.
`database/sql` stays the query boundary; no ORM or external database service.

SQLite owns foreign keys, unique handles, idempotency and one-active-run
constraints. Stable scalar keys and status are SQL columns; versioned object
payloads use validated JSON. All state and corresponding events commit in the
same transaction. The process notification channel only wakes replay readers;
it is never the source of truth.

## Provider and event boundaries

The first provider uses Anthropic Messages API. The Go host executes only
validated, bounded tools. Provider SSE names stop inside the adapter. Native
CLI execution is retained as the existing command, not exposed as a Web tool.

## UI transport

REST handles commands and snapshots; SSE carries persisted per-run events.
Readers capture a wakeup channel before reading SQLite so a commit cannot
fall between history replay and subscription. Clients deduplicate sequence IDs.

## Bounded collaboration

Built-in workflows have finite step dependencies. Agent text cannot authorize
another invocation. Natural-language routing proposes typed assignments;
the host validates IDs, scope and the accepted workflow before dispatch.
