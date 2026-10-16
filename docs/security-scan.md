# Go 1.25 security scan — 2026-10-16

Go remains at the user-requested 1.25 baseline, pinned to 1.25.14. The current
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

Reproduce with `make security`. Re-run against the current vulnerability
database before release. A toolchain upgrade is a separate project decision;
the implementation does not override the user's Go 1.25 requirement.

Frontend production dependency audit: zero findings in the local 2026-10-16
scan. This is point-in-time evidence and must be refreshed in CI.
