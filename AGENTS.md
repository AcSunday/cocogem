# Development rules

- Product scope and acceptance criteria: `docs/product-v0.1.md`.
- Use `cc-skills-golang:golang-how-to` for Go work and load applicable skills.
- Go 1.26.9 or newer in the 1.26 line; standard library HTTP and explicit constructor injection.
- Keep command entry points in `cmd/` and implementation in `internal/`.
- Preserve native CLI behavior and tests in `internal/cli/`; run it with `go run ./cmd/cocogem`.
- The local Claude/Codex/Gemini CLIs are already usable. Reuse their existing API key, endpoint, provider and model settings; do not require login, OAuth or extra Cocogem keys. Diagnose configuration forwarding and the actual error before suggesting credential changes.
- Implement each feature with behavioral tests; commit only after its checks pass.
- Commit messages follow Conventional Commits (`feat: ...`, `fix: ...`, `test: ...`).
- Follow `docs/project-layout.md` and co-locate tests with their implementations.
- Keep API/OpenAPI, frontend generated types, fixtures and tests in sync.
- Never commit credentials, captured user data, databases or build output.
- Do not mark real-provider/platform verification complete based on fake tests.

- Web access uses a password set during first initialization and a local session afterward. This is independent of native CLI credentials; never require CLI login for Web access.

- Composer: Enter sends, Shift+Enter inserts a newline; preserve IME confirmation and @ completion. Free collaboration uses @ or natural-language routing without an automatic-assignment control. Do not reintroduce a data-sharing checkbox or consent gate.

- Global member profiles are reusable across workspaces; effective tools are the intersection of global allowances and workspace grants. Preserve member IDs and immutable history during migrations.
- Free collaboration uses the existing scoped MCP bridge for bounded handoff_task/read_skill/get_task calls. Plain assistant @ text never dispatches work; built-in workflows keep their accepted plans.
- Shared skills have one source under internal/skills/library and use runtime roles/capabilities. Never bind skill text to concrete members, handles, CLI brands or model names. Track enabled skill versions per Run.
