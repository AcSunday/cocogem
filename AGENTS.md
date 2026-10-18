# Development rules

- Product scope and acceptance criteria: `docs/product-v0.1.md`.
- Use `cc-skills-golang:golang-how-to` for Go work and load applicable skills.
- Go 1.26.9 or newer in the 1.26 line; standard library HTTP and explicit constructor injection.
- Preserve the existing root CLI behavior and its tests.
- Implement each feature with behavioral tests; commit only after its checks pass.
- Commit messages follow Conventional Commits (`feat: ...`, `fix: ...`, `test: ...`).
- Keep API/OpenAPI, frontend generated types, fixtures and tests in sync.
- Never commit credentials, captured user data, databases or build output.
- Do not mark real-provider/platform verification complete based on fake tests.
