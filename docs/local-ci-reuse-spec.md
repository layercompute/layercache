# Local results available to CI

This slice implements the user's October 13 request to reuse successful local
tests and builds in matching commit checks. Turbo owns task fingerprints,
dependency inputs, environment and successful-task artifacts. LayerCache owns
bounded Local Cache, authenticated Team Cache and publication confirmation.

## Interface

`layercache run --sync-team --sync-timeout 30s -- pnpm exec turbo run test build`
uses the loopback daemon for Turbo reads and writes. It sets
`TURBO_CACHE=local:,remote:rw`, disabling Turbo's separate Workspace cache so
successful results pass through the bounded Local Cache. CLI cache arguments
can override this environment setting, so readiness still requires actual
artifact verification.

After the child finishes, collect only that invocation's reported summaries.
Select successful, cacheable task hashes using the existing summary validation.
Confirm those hashes in the configured Team Cache, republishing matching Local
Cache entries where needed. Confirmation is scoped to those task hashes and
compatibility, never the global upload queue. Team entries may have different
opaque tar/log bytes for the same task hash; validate their metadata and use the
existing trusted-writer/deterministic-task contract rather than treating benign
log differences as missing test results.

The local daemon's narrow authenticated synchronization endpoint accepts a
bounded task-hash set and a bounded wait. It is available only for Local Cache,
uses the existing configured Team credential and namespace, and does not expose
credentials or arbitrary endpoints. Reuse existing pins and durable upload
queue for failed publication. Use a metadata check before and after publication.
Retries obey the overall timeout and do not wait on unrelated queue conflicts.

Print `Team Cache ready: N/N successful tasks` only after confirming all selected
tasks. Empty or invalid summaries, missing Local/Team bytes, unavailable daemon,
authorization failure and timeout cannot produce that message. Publication
warnings preserve the child result; failed tasks never produce an all-checks
passed receipt. Independently successful tasks may still be shared when another
task fails.

## CI policy

Required PR jobs remain present and run the same task definitions with read-only
Team Cache. A matching successful task restores rather than executes. A miss
executes normally. Main's fresh-test profile explicitly uses
`--cache=local:,remote:`. Turbo 2.10.12 rejects combining `--force` with
`--cache`. Disabling LayerCache alone does not disable
Turbo's separate cache. Build/compiler caches and deterministic verdict reuse
remain separate policies.

Local and CI must use an explicit compatible execution profile, including actual
Node/pnpm/platform/toolchain. A Mac result does not implicitly cover Linux, and
Phone's local Node24.21 cannot label itself as CI's Node24.20. Only audited
deterministic tasks are eligible initially. Network/live suites, mutable Git
base checks and incomplete task inputs need their own policy.

Authenticated project writers are trusted to publish Local Build outputs. Digest
validation proves transfer integrity; it does not remotely attest that arbitrary
developer-supplied archives represent real test execution. This slice reuses
the existing trust contract and does not invent a commit-level attestation.

## Acceptance

- Real Turbo producer runs build and test; Local Cache repetition and a fresh
  Team consumer restore matching outputs without executing either task.
- Failed tests execute again and retain their failure exit code.
- Source, dependency, declared environment and compatibility changes miss.
- Main's read-disabled mode executes despite existing cache entries.
- Offline local success remains usable locally and reports unconfirmed Team
  publication; queued publication survives a daemon restart.
- Same-hash archives with changed log bytes and unrelated queued jobs do not
  falsely block confirmation or claim that different bytes were uploaded.
- Scope, malformed request, invalid remote metadata and publication timeout
  cases cannot produce false readiness.

Phone implementation remains owned by its active CI chat. Coordinate a small
Core typecheck/unit lane and PR read policy after its task-input fixes. This
LayerCache PR provides the reusable capability and real-client acceptance.
