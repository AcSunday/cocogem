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
