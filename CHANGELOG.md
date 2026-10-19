# Changelog

## Unreleased

- Reuse native CLI API provider, authentication and default-model settings
  without loading unrelated hooks, plugins or tools.
- Use local CLI sessions for web execution, routing and connection checks;
  separate executor installation from actual model verification.
- Preserve exact upstream reports and routing constraints; keep default
  discussion participants within the conversation.
- Fix repeated member edits, default workflows, unavailable role choices and
  historical member labels.

- Group frontend code into app, API, components, forms, styles and test directories.

- Require Go 1.26.9 or newer; CI covers the 1.26 line and current stable.
- Move the native CLI to `cmd/cocogem` and `internal/cli`; use
  `go run ./cmd/cocogem` instead of `go run .`.

- Preserve the original Claude, Codex and Gemini CLI prototype and session tests.
- Define the v0.1 multi-agent workspace product and implementation contracts.
