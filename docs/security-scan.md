# Security scan — 2026-10-28

The pre-merge scan found reachable
[GO-2026-6860](https://pkg.go.dev/vuln/GO-2026-6860) in the `golang.org/x/net/html`
parser used by `internal/tools/web.go`. Upgraded `golang.org/x/net` from
v0.58.0 to the fixed v0.62.0 and synchronized its required indirect dependencies
and third-party notices. The project remains on Go 1.26.10.

Local `GOFLAGS=-p=1 make security` passed: the tracked-file credential scan
passed, `govulncheck` reported no vulnerabilities, and the frontend production
dependency audit reported zero findings. `go mod verify` also passed. These
results describe the local build and vulnerability database on this date;
real-provider checks and remote CI remain separate verification.

## Historical scan — 2026-10-24

Current minimum toolchain is Go 1.26.10. The 2026-10-24 scan found reachable
GO-2026-6809 in Go 1.26.9 crypto/x509; the fixed 1.26.10 patch stays within the
project's approved Go 1.26 line. Current verification is recorded in
`development-progress.md`. Historical scan results below describe their dates.

## Historical scan — 2026-10-18

At that scan the project required Go 1.26.9, the current 1.26 patch listed by
[the official downloads API](https://go.dev/dl/?mode=json). The user approved
the move from Go 1.25 on 2026-10-18. Both Go modules and CI use the new baseline.

`make security` passed locally with Go 1.26.9:

- Common-secret scan: passed.
- `govulncheck`: 0 reachable vulnerabilities and 0 affected imported packages.
  It also reports 5 findings in required modules whose vulnerable symbols are
  not called by this build. This is not a claim that every dependency is
  vulnerability-free.
- Frontend production dependency audit: 0 findings.

Re-run `make security` against the current vulnerability database before each
release. CI retains the scanner without allowlisting IDs or ignoring failures.
Real-provider checks and GitHub-hosted CI remain separate release gates.

## Historical Go 1.25 scan — 2026-10-16

At this scan the project used Go 1.25.14. The then-current
Go vulnerability database reports reachable standard-library findings whose
listed fixes are Go 1.26.9 / 1.27.2, with no Go 1.25 fix listed at this scan.
`golang.org/x/net` also reports affected HTTP/2 code; only its HTML parser is
used by this application. Transitive tooling currently selects x/net v0.58.0.

Recorded IDs: GO-2026-6603, GO-2026-6604, GO-2026-6605, GO-2026-6607,
GO-2026-6608, GO-2026-6609, GO-2026-6610, GO-2026-6611, GO-2026-6612,
GO-2026-6613 and GO-2026-6617.

The service accepts local HTTP only; it does not implement CONNECT, parse
multipart uploads or run on Windows. These boundaries reduce some exposure,
but this note does not waive all findings or certify the Go 1.25 build safe.
The scanner's reachability report needs to be assessed against each actual
transport path. CI keeps `govulncheck` as a failing release gate on affected
toolchains. Do not publish a clean-security claim or silently allowlist IDs.

This historical result explains the upgrade; current results are recorded
above. The Go 1.25 baseline is no longer supported by this project.

Frontend production dependency audit: zero findings in the local 2026-10-16
scan. This is point-in-time evidence and must be refreshed in CI.
