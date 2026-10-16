# Contributing

Use Go 1.25 and Node.js 22.12+ (Node.js 24 LTS is recommended).
Read [the product specification](docs/product-v0.1.md) before changing behavior.

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
