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
The CLI-owned native subset is derived from the same effective member grants:
Claude gets `Read`/`Glob`/`Grep` for file grants and `WebSearch`/`WebFetch` only
for discovery scopes; Codex gets its combined Responses web tool only when both
web grants are present and the scoped provider is declared as supporting Responses web; Gemini native web stays disabled
because its stream-json `tool_result` does not expose the actual content.
`buildRequest` names the actually enabled native tools in the system prompt,
maps only those to their MCP equivalents as fallback-only, and branches research
guidance on actual native web availability. This steers tool choice but does not
block the MCP fallback, which must stay usable when a native tool fails.
`Bash`/`Edit`/`Write`, built-in task/agent/skill tools, hooks, extensions and
unrelated MCP connections remain disabled. Every per-attempt runtime file
(Claude MCP/settings, Codex model catalog and `CODEX_HOME`, Gemini settings and
policies) lives in a 0700 `.cocogem` directory with 0600 files, so both the
Claude native deny rules and the host MCP `tools.DeniedPath` boundary reject it
even when the workspace root contains the temporary directory. Deny rules proved effective only for the
working directory, not for `--add-dir` roots, so native file steps run from the
workspace root with `permissions.deny` mirroring host sensitive-path rules and
`CLAUDE_CODE_DISABLE_CLAUDE_MDS`/auto-memory disabled; web-only steps keep an
empty cwd. If the root would expose the settings directory the adapter keeps
file access on MCP. Native tool results are parsed only when an
actual call/result pair is present; a call without its completion fails the
turn closed, and an exact replay cannot reopen a completed call. Successful
content is captured as `native_file`/`native_web`/`native_search`, failures
stay visible errors, and the parser is a protocol guard rather than a
filesystem sandbox. Native output may carry only metadata or be empty; a
completed call remains a call fact but empty output never becomes a source. If the
installed CLI cannot enforce this boundary, the adapter reports unsupported
instead of relaxing permissions. Installation, adapter support, login status
and a verified actual call are distinct facts.

The superseded Anthropic API adapter has been removed. The web server does
not require separately configured model API keys. It forwards only allowlisted
connection values from native CLI configuration to private temporary settings
or the child environment; it never puts them into browser data or event logs.
Production execution uses installed native CLIs only. Synthetic model and source
fixtures remain isolated in automated tests and are not a runtime mode.

Use `golang.org/x/net/html` v0.58.0 (BSD-3-Clause)
for HTML parsing. The standard library does not provide an HTML parser.
Direct web transport validates public addresses at connection time and
revalidates redirects. An explicitly configured proxy (`COCOGEM_WEB_PROXY`
address, `env`, or `system`, which `make dev` selects by default) resolves
target names; local URL and redirect policy still apply, but the remote
address is inside that trust boundary. Search credentials never follow redirects.
The same resolved proxy is forwarded to native-web CLI children only; those
children handle their own DNS, redirects and provider-side safety checks, which
are not the Go transport/SSRF guarantee. `NO_PROXY` always keeps loopback MCP
direct and merges existing entries. This environment can also affect the CLI's
model connection while native web is enabled, and is documented as such.
File reads use `os.Root`, explicit symlink rejection and bounded captures.
Authenticated users bind an existing readable local directory in workspace
settings. The directory browser lists immediate non-hidden folders through an
authenticated API, with no file contents, a 1000-entry scan bound and explicit
truncation. Browsing and choosing only fill the form; saving changes the binding.
Optional startup roots restrict browsing and bindings to those directories and
their descendants. Bindings are canonical absolute paths; known sensitive paths,
symlink escapes and changed saved aliases are rejected. Active runs block rebinding.

## Local Web access

First initialization sets a workspace password. Subsequent visits unlock with
that password; startup links contain no secret. `auth.json` in the private data
directory stores a random salt and PBKDF2-HMAC-SHA256 derived key, never plaintext.
Initialization publishes the complete file without overwriting another setup.
Corrupt authentication files fail closed. Cookies are HttpOnly and SameSite=Strict;
server-side sessions expire after 24 hours and are invalidated by restart or logout.
Host, Origin and CSRF checks remain in place. Native CLI credentials are separate.

## UI transport

REST handles commands and snapshots; SSE carries persisted per-run events.
Readers capture a wakeup channel before reading SQLite so a commit cannot
fall between history replay and subscription. Clients deduplicate sequence IDs.

## Long tasks, attempts and restart recovery

Wall-clock limits are separate from progress detection. A run defaults to six
hours, a step to one hour, the first valid response to ten minutes and a
no-progress warning to fifteen minutes; all are duration environment settings.
Tool execution and protocol events count as progress, so a silent model call is
not killed merely for lacking text. Waiting for a dependency is reported as a
step state rather than consuming that step's execution deadline. Liveness
warnings are events, not inferred progress percentages.

Logical steps and execution attempts are stored separately. An attempt records
the executor, requested and resolved model, non-secret connection identity
(provider, endpoint, protocol, auth method or environment key names), adapter,
CLI version, program identity (resolved path, size, modification time and
inode), native session identifier and mode, owned process identity (PID,
process group, start time) and lifecycle. New attempts never rewrite prior
messages, sources or artifacts. Recovery compares the resolved model and
connection identity before continuing; a changed default model, provider or
endpoint leaves an interrupted checkpoint instead of silently sending persisted
context to a different service. Every attempt and recovery rechecks the CLI; a
replaced binary invalidates the old compatibility result.

Dispatch and recovery take a durable per-run claim owned by the server's PID
and process start time. A live owner is never stolen; a dead or reused PID is
reclaimed with a compare-and-swap. On restart, owned child process groups are
verified before signaling; termination waits for the whole recorded group,
escalates when a descendant ignores TERM, and leaves an uncertain checkpoint if
the group cannot be proven empty. Unfinished steps are then re-queued. A failed
delegated step does not end recovery by itself when a continuation can consume
that failure; the normal dependency scheduler remains authoritative. Completed
steps are not repeated; a run whose steps all committed but whose terminal
transaction was lost is settled without re-execution. A process whose identity
cannot be verified, or whose result is uncertain, leaves an explicit
`interrupted` checkpoint instead of being silently retried. User cancellation is
never revived. An explicit resume revalidates the source binding, member grants
and CLI capabilities before reopening an interrupted checkpoint.

## Isolation evidence

Help text is only an installation/candidate signal. Before a step or a recovery
can use an installed CLI, the production adapter runs that exact binary against
a local fake model endpoint and a scoped MCP bridge. The probe must complete
session initialization, the required tool inventory handshake, a requested host
tool call, visible text and a terminal event under the same isolation flags used
for real work. The result is cached by program identity; a replaced binary or a
failed probe blocks the call and is shown as unavailable, while an unprobed
installation is shown as a candidate rather than confirmed. Probe failures are
retried after a short cooldown. The probe uses dummy local credentials, an
isolated CLI config directory and a scoped working directory, and never contacts
the configured provider. Routing and connection probes clear native tool, root
and proxy fields at the adapter boundary, so only the scoped MCP tools can be
exercised there.

## Bounded collaboration

Built-in workflows have finite step dependencies. Free collaboration can submit
bounded `handoff_task` tool calls within the accepted thread roster. Plain agent
text never dispatches work. The host validates identities, current workspace
membership, permissions, references, independent review and budgets. Tasks and
return continuations are durable Steps in the same Run; callers finish their CLI
invocation instead of blocking. Continuations receive committed results, including
failures, through a fresh invocation. Cancellation stops the whole Run. Restart
reconciles in-flight attempts and continues unfinished steps from committed
context; it never re-attaches to a user-owned terminal session or silently
replays an uncertain external result.

Global member profiles and workspace grants are stored separately in SQLite.
Runs snapshot effective members and shared-skill versions. Each invocation gets
a generated roster and task state; live revocation is checked at dispatch.
Shared skill Markdown has one embedded source under `internal/skills/library`.
Only the enabled catalog is injected; `read_skill` retrieves entrypoints and
allowlisted references on demand through the existing MCP bridge. Skills describe
roles and capability-based selection without binding names, providers or models.

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
