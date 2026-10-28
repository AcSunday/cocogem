# Contributing

Use Go 1.26.10+ and Node.js 22.12+ (Node.js 24 LTS is recommended).
Read [the product specification](docs/product-v0.1.md) before changing behavior.
Follow [the project layout](docs/project-layout.md) when adding files.

Each pull request should implement one reviewable feature or fix, include tests,
and describe the user-visible result, validation, limitations and migrations.
Use Conventional Commits, for example `feat: add workspace creation`.

Keep provider protocols inside adapters. Treat model output, files, URLs and
browser requests as untrusted input. Never include keys or private source data
in fixtures, screenshots, logs or commits. CI must run without provider keys.

Run the feature's tests before committing, and the full `make test` and
`make build` checks before submitting. API changes must update OpenAPI,
generated frontend types and contract tests in the same pull request.

Public releases and remote pushes are separate maintainer actions. AI review
does not replace automated tests or independent review of security changes.

Native CLI adapters have three different verification levels. `make test` uses
only temporary subprocess fixtures; `make e2e` uses an isolated, test-only Go
HTTP/SSE harness with synthetic model and source fixtures. No fixture model or
offline runtime mode is exposed by the application.
To exercise installed Codex/Gemini binaries against a local fake model and
scoped MCP, run:

```sh
COCOGEM_CHECK_INSTALLED_CODEX=1 COCOGEM_CHECK_INSTALLED_GEMINI=1 \
  go test ./internal/nativecli -run TestInstalled -v
```

These opt-in checks use synthetic connection configuration and a loopback fake
model endpoint. They must not be reported as live provider quality tests.
Actual page-based collaboration uses the maintainer's CLI connection settings
and only public sample tasks; record it separately in the alignment review.
