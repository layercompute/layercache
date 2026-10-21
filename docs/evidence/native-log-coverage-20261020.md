# Native log coverage qualification

Checked October 20, 2026 UTC. These client changes close observed reporting
gaps without changing cache keys, native tasks, permissions or build outcomes.
Source qualification and application adoption remain separate. Previously
accepted reports retain their original immutable contents.
The qualified client is pinned at
[`48e4208`](https://github.com/ziahamza/layercache/commit/48e420868b4250c9176ae5aa48127f1456e97208).

## Gradle lifecycle tasks

Gradle 9.3.1 and 9.4.1 can report a lifecycle task as executed when a dependency
ran, even though that task has no own actions or outputs. Their exact native
`Skipping task ... as it has no actions` diagnostic now establishes
ineligibility. Native outcomes, durations, identities and keys are preserved.
Conflicting keys, unsupported versions and malformed markers remain unknown.
The [upstream implementation](https://github.com/gradle/gradle/blob/v9.4.1/subprojects/core/src/main/java/org/gradle/api/internal/tasks/execution/SkipTaskWithNoActionsExecuter.java#L38-L50)
sets the task non-actionable and returns without outputs before native caching.
The pinned 9.3.1 and 9.4.1 implementations are byte-identical.

Twelve retained native-stage captures across both versions were replayed without
another build. Four lifecycle eligibility gaps changed to ineligible; all
outcomes, keys, work references and timing remained unchanged. The existing cold
fixture now explicitly checks that its buildSrc lifecycle task stays executed
with no native key and is excluded from cache eligibility. Parser, projection
and packaged offline controls cover malformed input, key conflicts, privacy
and unsupported versions.

## Rendered Xcode logs

The supported Swift Build replay notes also occur in rendered Xcode logs with
target/project suffixes and terminal colours. The parser accepts that bounded
suffix and only the observed SGR codes `0`, `1`, `33` and `36`. Other terminal
controls remain unsupported. Raw byte and line bounds apply before formatting
removal. Exact `TEST BUILD SUCCEEDED` and `TEST BUILD FAILED` terminals complete
capture without establishing test execution or success.

Native `PrecompileModule`, `SwiftGeneratePch` and explicit-dependency module
headers now associate their existing terminal replay observations. They do not
supply a Swift job denominator. The Swift-driver scope includes Clang module
jobs, and does not qualify external Swift-language replay. The
[pinned source notes](../xcode-metering-source-notes.md) document these dispatches.

Two retained completed builds on Xcode 27.0 build 27A266a were processed offline,
with their original ZIPs, logs, source/action provenance and UTC record intervals
preserved privately. Each has one command invocation and one successful
build-for-testing terminal. Only the exact Apple UTC-record prefix was removed
before export. Each produces zero observed replay hits and 2,935 observed
terminal misses, with mixed, partial coverage and an unknown complete
denominator. These are two observations, not a controlled performance comparison.
The publisher independently retained 2,935 action reads, zero action hits and
zero service errors. The reader's optional cache preparation failed before
service readiness; its native build and tests succeeded. Its absent RPC
statistics stay unknown. Protocol requests and compiler observations remain
separate, and neither establishes saved time, CPU or money.

## Hosted coverage audit

An accepted application report contains 862 native task outcomes and 331 cache
restores. Sixty-three live checks across Chromium, Firefox and WebKit verified
the actual API-to-UI projection, period changes, cost isolation and normal
sign-out. This was separate from the earlier 63-check pass without native
reports. The accepted report retains its 30 unknown executed-task eligibilities;
offline review found their exact no-actions markers. A new client observation
is required to change future reporting coverage.

Of the 331 restores, 184 have unique Team delivery matches. The remaining 147
have multiple same-authority, same-key deliveries within their task windows.
They are ambiguous correlations, rather than evidence of local hits or absent
transport. Matching by request order would invent attribution. All original
producer timing/resource joins are missing for this report. Historical task
timing, native graph coverage, actual job allocation, measured CPU and provider
billing each need their own evidence; a cache hit count does not fill them in.

Sixty focused source/action checks and twenty packaged offline CLI checks passed,
with no failures or skips. Native bundle generation, full TypeScript checks and
whitespace checks passed. Independent Standards and Spec reviews found no
actionable issues. [PR #85](https://github.com/ziahamza/layercache/pull/85)
passed all eighteen PR checks and merged as
[`2e00779`](https://github.com/ziahamza/layercache/commit/2e00779c3fa10ba57ccd2812a4f45d6e567891e1).
All four workflows on that main commit passed.

## Hosted acceptance, October 21 UTC

A normal application PR job using the qualified Gradle action submitted one
completed invocation with 862 tasks. An independent exact-job read verified
862 persisted rows, outcomes, opaque artifact identities and signed source.
It recorded 331 restores from 337 eligible tasks, with six eligible misses.
Its per-invocation eligible hit rate is 98.22%. All 30 no-actions lifecycle tasks
are now ineligible, leaving zero unknown executed-task eligibilities. The other
198 tasks with unknown eligibility are up-to-date, no-source or skipped, and
remain outside this denominator. Its signed source matches the actual checkout,
and the action post confirms one recorded invocation. Normal application Check,
APK/Kotlin and fresh-device gates passed before the adoption landed. Exact main
qualification remains distinct from accepting this report.

An authorized controller imported both retained Xcode observations with the
qualified exporter and beta.8 Go CLI. The builds produced their logs on
October 20 UTC; the imports occurred on October 21 UTC. Independent read-only
verification matched both accepted immutable receipts to their exact input,
producing profile, declared source and compiler counters. The controller did
not inject its hardware as the producer's hardware. These member observations
remain distinct from Apple workload attestation.

A further 63 authenticated live checks across Chromium, Firefox and WebKit
passed against the combined Gradle/Xcode projection. The two Xcode observations
show zero terminal replay hits and 5,870 terminal misses, with mixed, partial
coverage and an unknown full rate. Complete RPC totals stay unknown because
one observation lacks final service statistics. The dashboard currently has
no per-invocation RPC coverage count or subtotal for the known subset. All six
optional phase timings remain unknown. Neither absent input becomes zero.

Historical Gradle reports keep their original partial eligibility. A later
complete invocation cannot repair an older immutable report or make a selected
period complete while that older report is included. Known task and restore
counts stay visible. Producer timing/resource matches, native graph coverage,
measured CPU and provider-complete billing remain separate acceptance gaps.
The [coverage audit](../native-metering.md#coverage-audit) lists every currently
supported integration and the remaining reporter or adoption work.
