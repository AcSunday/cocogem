# Security

Cocogem v0.1 is a local, single-user application. It must listen only on
loopback and must not be exposed through public tunnels or reverse proxies.
Model services receive the questions and source excerpts used by a real run;
search services receive search queries. No secret detector can identify every
credential embedded in source code. Use only data you are allowed to share.

Report vulnerabilities through GitHub's private vulnerability reporting for
this repository when available. If it is unavailable, open an issue requesting
a private contact without including exploit details or sensitive data.
Do not post keys, captured files or personal data in public issues.

Security fixes must include regression tests. Provider integration tests must
use synthetic, public fixtures and must be explicitly enabled.

Web execution reuses local CLI authentication and allowlisted model connection configuration. A step must disable general
native tools and unrelated hooks/extensions/MCP servers, then use only its
scoped host MCP tool endpoint. It must fail closed if the CLI lacks these
controls. The standalone terminal prototype inherits the native CLI's own
permissions and is not the web execution boundary. Do not point web sessions
at an arbitrary shell wrapper or resume user-owned interactive sessions.

A CLI being installed or reporting a logged-in account does not establish that
its token is current. Live verification must check an actual invocation and
report authentication, quota or model failures without exposing raw secrets.
