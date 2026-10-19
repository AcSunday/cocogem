# v0.1 architecture decisions

## Local service and explicit ownership

The native CLI lives in `cmd/cocogem` and `internal/cli`, keeping its
arguments, output and session format compatible. The Web server is a separate command,
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

The web runtime delegates model calls and the tool loop to the selected local
CLI. `internal/nativecli` launches a fresh session in an isolated directory,
passes the exact host-built context, validates native event termination and
reclaims the process group on cancellation. It never resumes a user-owned
interactive session or reads the terminal prototype's latest-session file.

Only this step's Go-owned tools are exposed through a short-lived loopback MCP
endpoint. Its random bearer token never enters prompts or event records.
Native general-purpose tools, hooks, extensions and unrelated MCP connections
must remain disabled. If the installed CLI cannot enforce this boundary, the
adapter reports unsupported instead of relaxing permissions. Installation,
adapter support, login status and a verified actual call are distinct facts.

The superseded Anthropic API adapter has been removed. The web server does
not require separately configured model API keys. It forwards only allowlisted
connection values from native CLI configuration to private temporary settings
or the child environment; it never puts them into browser data or event logs. The demo retains its explicit deterministic model
and Go-driven tool loop.

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

## Local connection configuration

Restricting tool configuration must not erase working model authentication.
Claude forwards the allowlisted Anthropic connection environment, default
model and effort from its native user settings. Codex uses a TOML parser to
extract only the selected provider, default model and connection fields;
nonsecret connection fields enter explicit CLI overrides, while token/header
values enter only the child's environment. No native profile is overwritten. Hooks, plugins, project instructions, unrelated
MCP servers and command-based authentication helpers are not inherited.
Existing config/auth files are not overwritten or copied into application data.
`github.com/pelletier/go-toml/v2` v2.4.3 (MIT) handles TOML; the standard library
has no TOML parser, and regex extraction would misread real configurations.

Every supported installed CLI has opt-in protocol checks against a local fake
model endpoint. These prove binary/configuration/MCP compatibility without
using account credentials; they do not prove live account or model quality.
