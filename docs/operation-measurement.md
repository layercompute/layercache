# Measure a complete cached operation

Observe the operation once, starting before cache authentication and preparation
and ending after cache closure and its update acknowledgement. This captures
the cost of fetching and updating the cache as well as the intervening work.
Native task and compiler reports remain separate observations. No additional
native run is started automatically to obtain a baseline.

Client source, hosted deployment, application adoption and a qualified
cold/warm pair are separate gates. The qualification section records which
gates have passed.

## Record an observation

The lifecycle is **start → prepare → work → update → finish**. Start and finish
never launch a build. Each phase marker starts the named interval; run that
phase's existing command after its marker.

Use the offline native CLI with a protected context file and an absolute private
state path. Its ancestors must also be trusted directories: a group-writable
home/cache parent is rejected even when the final directory is private. For a
local experiment, a newly created private directory under the system's sticky
temporary directory is suitable; resolve its actual path before using it.
The context declares the project, exact compatibility profile,
immutable source, integration, session and opaque workload/coverage digests.
It also declares fresh versus incremental outputs, the cache policy, compatible
execution context and other-cache policy. Raw paths, commands, environment maps
and credentials do not belong in that context or the uploaded report.

```sh
layercache-native measure-start --context-file /absolute/private/context.json \
  --state-file /absolute/private/operation-state.json

# Authenticate/configure the existing integration during setup.
layercache-native measure-phase --state-file /absolute/private/operation-state.json \
  --phase restore
# Restore with the existing integration; inspect its actual hit/miss result.
layercache-native measure-phase --state-file /absolute/private/operation-state.json \
  --phase work --cache-preparation miss

# Run the intended native operation once, preserving its actual result.
layercache-native measure-phase --state-file /absolute/private/operation-state.json \
  --phase close
# Stop/close the cache adapter and verify its closed-store boundary.
layercache-native measure-phase --state-file /absolute/private/operation-state.json \
  --phase publish
# Update the cache and wait for its existing acknowledgement.
layercache-native measure-finish --state-file /absolute/private/operation-state.json \
  --status success --output /absolute/private/operation-report.json

layercache report operation --integration gradle \
  --config /absolute/private/project.json --source-commit IMMUTABLE_COMMIT \
  --input /absolute/private/operation-report.json --json
```

Use a canonical absolute path: on macOS `/tmp` and `/var` can resolve through
`/private`. The context, state and output must be owned regular files with mode
`0600`, in an owned `0700` directory. Context exists before start; state and report
are created by the recorder. For example, this illustrative context uses opaque
digests of versioned workload, assertion-coverage, execution and other-cache
recipes. Replace its values with the actual declared context; matching arbitrary
constants does not establish equivalent work.

```json
{
  "version": 1,
  "project": "github.com/example/mobile-app",
  "integration": "gradle",
  "compatibility": "android-debug-x86_64-qualified-profile",
  "sourceCommit": "0123456789abcdef0123456789abcdef01234567",
  "sessionId": "run-example_session_20261021",
  "workloadDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "coverageDigest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "environmentDigest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "otherCachesDigest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
  "outputState": "fresh",
  "cachePolicy": "enabled"
}
```

The recorder hashes neither your source nor these recipes automatically. An
integration owns their completeness. `sourceCommit`, project and profile in the
local context support local comparison; hosted imports use the configuration
and authenticated execution authority instead. Supply the producing source and
profile when importing, rather than the machine's current checkout context.

The phase commands mark boundaries; they do not run the commented work. An
adapter must finish with the native operation's real status, preserve a compiler
or test failure and avoid rerunning it when optional reporting fails. A missing
finish is incomplete, not a successful observation. Setup starts at begin;
repeated phase markers accumulate disjoint intervals. Unmeasured phases remain
unknown. The measurement upload occurs after the observed operation.

The state is private, local and bound to its monotonic clock. Moving it to a
different machine or boot cannot produce a valid elapsed interval. Finishing
seals it; retrying the same final report reconciles an unknown acknowledgement.
Changing a previously accepted report conflicts. New admissions must begin
within the previous 24 hours. Exact accepted retries reconcile during their
original 30-day retention without extending it; expired reports cannot be
resurrected. A cache receipt still promises no artifact retention.

## Select a comparison

Use the baseline's accepted receipt ID in the candidate context. The server
compares that exact pair, without selecting the fastest or slowest sample.
Local sealed-state comparison is also available through `measure-compare`;
its local content hashes are distinct from hosted receipt identities:

```sh
layercache-native measure-compare \
  --baseline-state /absolute/private/baseline-state.json \
  --candidate-state /absolute/private/candidate-state.json --json
```

Keep each operation's context immutable. Prepare a separate candidate context
with the baseline's accepted `baselineReceiptId` for a hosted pair. Change only
the intended cache policy/state; the integration must independently verify
cache outcomes and output/coverage equivalence. A local comparison selects
its two sealed states directly and needs no hosted account.

Both operations must succeed with the same source, workload, assertion coverage,
complete profile, declared session, execution context and other-cache policy.
Both need fresh native outputs. A bypass baseline compares with enabled caching;
an enabled cold miss compares with a hit or mixed restore. A cache miss is a
different experiment from disabling the cache. Up-to-date native outputs can
skip work independently of LayerCache, so they cannot establish this comparison.

The dashboard shows baseline elapsed time, candidate elapsed time and their
observed difference. Negative differences remain visible. Phase details show
where measured time went; incomplete phase coverage leaves its overhead estimate
unknown. Comparisons never contribute to existing task-derived savings totals.
A reused baseline does not become additional avoided work each time it appears.

Same-session observations reduce machine variation, but OS page cache, JVM
warmth and dependency state can still differ. The result is an observed elapsed
difference with declared assumptions, not proof that caching caused the entire
change. Allocated compute needs matched allocation and job overhead; CPU needs
usage counters; billed savings need provider pricing and billing evidence.

## Qualification

The current repair work separately covers a canonical-path containment failure
after a successful restore and safe reporting of an earlier archive failure.
Retained logs alone do not establish why that historical archive transfer failed.
A real dependency-free Gradle fixture exercised the packaged recorder, local
HTTP cache, Go import and SQL reporting in one declared session. The cold
operation took 6,562 ms; after removing native outputs, the warm operation took
4,772 ms. Both cold tasks executed and published; both warm tasks restored from
HTTP with zero executions or writes, and class/assertion-output digests matched.
Local and hosted-format comparisons agreed on the observed 1,790 ms difference.
An exact import retry returned the same receipt. This is one fixture pair with
shared OS/bootstrap warmth, not an application or billing benchmark.

Gradle owned its HTTP transfers inside the measured work interval, so separate
restore/publication phases stayed unknown. Their overhead was not invented or
subtracted. Independent reviews covered the protected recorder, read-scoped
report endpoint, explicit pairing, independent quotas and dashboard. Full Go and
JavaScript checks passed. Source/packaged recorder controls also exercise
split-process monotonic timing; the normal macOS CI job qualifies the Darwin
clock and private filesystem behavior.

Application/provider acceptance is coordinated with its CI owner. The reader
repair and the operation recorder are separate changes: the repaired reader
still needs a compatible hosted restore/readiness/replay receipt, and adoption
must place the timer around the actual application lifecycle. No cache-dependent
native run is started automatically to obtain measurement evidence.
