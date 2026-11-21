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
and symlink escapes are rejected. Execution permission is one global two-tier
policy (`workspace_autonomous` by default, or `full_access`), synchronized
between the Composer and Settings → Permissions, plus a per-turn `Plan-Only`
(`plan_only: true`) read-only lock. Legacy member tool arrays and workspace
Grants do not form hidden permission settings for new execution.
The current task, step role, enablement and exact operation scope still apply;
independent reviewers and critics run in a disposable `verify_dir` copy of the
target tree with a fresh native session (`fresh_only`), allowing verification
commands, tests and page previews inside `verify_dir` while asserting the
author's candidate or project `TreeDigest` remains byte-identical.
Sandbox-autonomous (`workspace_autonomous`) operations use a verified
adapter/OS boundary, and extra scopes require a pre-effect decision. Codex
sandbox-autonomous execution uses a restricted named filesystem profile with an
explicit root deny. Native approvals that cannot represent a one-call outside
scope stay denied; scoped host tools handle those operations. A permission
overlay for an entire native turn is not a once grant.
Once approvals bind the exact invocation and arguments; conversation grants are
shared by eligible members of that thread using a three-tier action level
(`read < write < execute`) plus an exact file or physical directory subtree
scope (never storing broad roots such as `/`, `$HOME`, `/Users`, or top-level
`/tmp`, while allowing `/tmp/<subdir>`). Sensitive credential paths are limited
to one-shot `Risk="sensitive_path"` approvals with no conversation grant.
Revocation and deletion invalidate authority.
Verified engineering delivery still requires matching project-tree evidence.
Ordinary unpromoted single-member chats (`thread.TaskID == ""`) write directly
to the bound `Workspace.Root` inside the OS sandbox, multi-member parallel
fan-out (`@A @B`) and `Plan-Only` turns are read-only, and durable tasks
(including `promote_to_task` upgrades) and parallel delegated subtasks derive an
isolated Git worktree (or bounded file copy for non-Git directories) that keeps
the bound project root untouched until explicit Apply. macOS execution
boundaries have separate behavioral tests; unsupported platforms fail closed
rather than silently trusting native execution.
Active runs and unapplied candidates block changes to the directory binding. See the
[product document](docs/product.md#工具与执行边界) for the full tool boundaries.

Report vulnerabilities through GitHub's private vulnerability reporting for
this repository when available. If it is unavailable, open an issue requesting
a private contact without including exploit details or sensitive data.
Do not post keys, captured files or personal data in public issues.

Security fixes must include regression tests. Provider integration tests must
use synthetic, public fixtures and must be explicitly enabled.

Web execution reuses local CLI authentication and the frozen model connection configuration. Each step exposes its task/role catalog and scoped host MCP endpoint. Plan-only and read-only steps withhold write and command tools; sandbox-autonomous mode (`workspace_autonomous`) enables native tools through the adapter's execution gate and verified OS boundary, requesting explicit approval before outside-scope or sensitive-path operations. Full access retains trusted local native execution and does not promise sandbox isolation or complete effect auditing. The host revalidates live authority and records single-use consumption. A trusted invocation is not automatically retried after a transient failure within that invocation, but restart recovery may requeue an unfinished step after its process has exited or been stopped even when the result was not committed. It therefore cannot guarantee that an unrecorded native effect will not recur. Waiting for a decision holds no database write transaction and is separate from tool execution timeouts.

When `COCOGEM_EXTERNAL_EGRESS_HOSTS` enables the host's external HTTP write
handler, the initial URL must use HTTPS and match a configured host. The
default HTTP client can follow redirects without checking subsequent targets
against that list. Do not treat the initial host check as an enforced boundary
for the entire redirect chain. Full-access native network operations do not use
this host handler.

Private Claude control hooks, Codex app-server approvals, OpenCode execution hooks and Gemini/Pi tool gates are part of the adapter; unrelated user hooks, extensions and MCP servers are not copied into the step. Native call/result identities must match before observations are recorded. Local model configuration is forwarded without granting the model arbitrary access to all user configuration or history. File/path checks, command execution profiles and mutating-request approvals have separate behavioral evidence. Public retrieval, dependency downloads and an approved external write are distinct operations. Installation, protocol fixtures, OS checks and real-provider journeys are recorded separately in the [acceptance record](docs/2026-11-08-permissions-and-ui-acceptance.md); an unknown adapter/OS combination is not a verified boundary.

The Web service is the only supported product surface. Do not point it at an arbitrary shell wrapper or resume user-owned interactive sessions. Managed resumable sessions live in each CLI's own home directory (except disposable reviewer `verify_dir` runs, which are ephemeral), and reuse requires the exact managed session identity and unchanged model, connection, role, work root, context and global permission epoch. Revoking authority marks only the Cocogem session ledger `revoked` and clears the native session ID without deleting or rewriting the user's native CLI directories.

A CLI being installed or reporting a logged-in account does not establish that
its token is current. Live verification must check an actual invocation and
report authentication, quota or model failures without exposing raw secrets.
