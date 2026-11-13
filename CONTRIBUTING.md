# Contributing

Use Go 1.26.10+ and Node.js 22.12+ (Node.js 24 LTS is recommended).
Read [the product document](docs/product.md) before changing behavior.
Follow its [project layout](docs/product.md#项目结构) when adding files.

Each pull request should implement one reviewable feature or fix, include tests,
and describe the user-visible result, validation, limitations and migrations.
Write commit messages, pull request titles and pull request descriptions in
English.

Format commit headlines and pull request titles as scoped Conventional Commits:
`<type>(<scope>): <imperative summary>` (lowercase type and scope, no trailing
period):

- `<type>`: `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `build`, `ci`, `chore`
- `<scope>`: primary package or subsystem touched, such as `workflow`, `httpapi`,
  `nativecli`, `project`, `execution`, `contextplan`, `store`, `tools`,
  `workspace`, `skills`, `agent`, `server`, `contract`, `web`, `docs`, or `ci`
- Examples: `feat(workflow): add peer-to-peer relay handoffs`,
  `fix(nativecli): route OpenCode gateway to the selected provider`,
  `fix(web): match the permission notice width to the retry notice`

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
Actual page-based collaboration uses existing CLI connection settings and only
public sample tasks. Report the actual CLI, provider outcome and limitations
separately in the pull request; never label synthetic tests as live verification.
