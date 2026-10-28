# Security

Cocogem v0.1 is a local, single-user application. It must listen only on
loopback and must not be exposed through public tunnels or reverse proxies.
Model services receive the questions and source excerpts used by a real run;
search services receive search queries. No secret detector can identify every
credential embedded in source code. Use only data you are allowed to share.

The Web UI uses a password chosen on first initialization. Password hashes use
PBKDF2-HMAC-SHA256 with a random salt and 600,000 iterations in a private
`auth.json` file. Back up that file with the data directory. Browser sessions
expire after 24 hours or server restart; locking a browser revokes its session.
Model CLI authentication remains independent and reuses existing local settings.

An unlocked local user can bind a readable directory in workspace settings.
Optional `--allow-root` arguments restrict bindings to those roots and their
descendants. The authenticated directory browser obeys the same scope and only
lists accessible folder names and paths, skipping hidden, known sensitive and
symlink entries. It never returns file contents or changes a workspace binding.
Paths are canonicalized before storage; known sensitive directories
and symlink escapes are rejected. File tools remain read-only and scoped to the
workspace binding. Active runs block changes to that binding.

Report vulnerabilities through GitHub's private vulnerability reporting for
this repository when available. If it is unavailable, open an issue requesting
a private contact without including exploit details or sensitive data.
Do not post keys, captured files or personal data in public issues.

Security fixes must include regression tests. Provider integration tests must
use synthetic, public fixtures and must be explicitly enabled.

Web execution reuses local CLI authentication and allowlisted model connection configuration. A step exposes only the native read-only subset derived from the member's effective grants and the tools required by its exact scope, plus its scoped host MCP endpoint; raw shell, edit/write, built-in task/agent/skill tools and unrelated hooks/extensions/MCP servers stay disabled. Native tool names are enforced by the CLI and rechecked by the stream parser; actual call/result pairing is required before any source is saved. Claude settings live in a separate private directory; native file steps run from the workspace root so cwd-relative deny rules are effective, with project-memory discovery disabled, while web-only steps keep an empty cwd. Sensitive paths are denied, and a root that would expose the settings directory falls back to MCP file tools. Native web traffic is governed by the CLI/provider network policy, not the host URL/DNS/SSRF checks; if a public-web proxy is configured it is passed only to native-web children with loopback MCP and existing `NO_PROXY` entries preserved. It must fail closed if the CLI lacks these controls. The standalone terminal prototype inherits the native CLI's own permissions and is not the web execution boundary. Do not point web sessions at an arbitrary shell wrapper or resume user-owned interactive sessions.

A CLI being installed or reporting a logged-in account does not establish that
its token is current. Live verification must check an actual invocation and
report authentication, quota or model failures without exposing raw secrets.
