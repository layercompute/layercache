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

The recorder is a separate Node.js 24+ package; installing the Go CLI does not
install `layercache-native`. Install the pinned package before the measured
operation, rather than adding a dependency installation to cache setup:

```sh
pnpm add -D '@layercache/native@github:ziahamza/layercache#cb254e9af694f6fe25f5a0ad416456174bda3e44'
```

Invoke it with `pnpm exec layercache-native` from that project, or use its
resolved executable on `PATH` for the commands below. This is a source-pinned
JavaScript package, not an npm release or an attested Go binary. Keep that same
recorder pin throughout the lifecycle.

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
Declare and protect the context before begin, and pin the same recorder version
for start, every phase and finish. If discovering the native profile or preparing
that context requires work before start, record that bootstrap time separately.
An interval that excludes it does not establish complete setup time.

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

This example assumes the native operation succeeded. Read preparation and reuse
outcomes from the native client; an existing archive or directory does not prove
a hit. Keep preparation unknown until its result is available, then record the
observed outcome at a phase marker or finish. Finish with the actual operation
status and preserve its exit code. Optional measurement or upload failures must
neither hide a build/test failure nor rerun the work.

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

The recorder supports Linux and macOS; Windows clock/filesystem support is not
qualified. It runs offline and needs no cloud account.

The recorder hashes neither your source nor these recipes automatically. An
integration owns their completeness. `sourceCommit`, project and profile in the
local context support local comparison; hosted imports use the configuration
and authenticated execution authority instead. Supply the producing source and
profile when importing, rather than the machine's current checkout context.
The Go import command requires a declared `sessionId`; operations from the same
installation and session share a 32-observation quota per integration. Changing
operation IDs, source or profile does not create another session bucket. This is
member/local session grouping, not proof of a physical machine or GitHub job. Direct reports
under verified Actions authority retain their authenticated job grouping; the
independent 4,096-observation/project and 8 MiB document limits apply as well.

The phase commands mark boundaries; they do not run the commented work. An
adapter must finish with the native operation's real status, preserve a compiler
or test failure and avoid rerunning it when optional reporting fails. A missing
finish is incomplete, not a successful observation. Setup starts at begin;
repeated phase markers accumulate disjoint intervals. Unmeasured phases remain
unknown. The measurement upload occurs after the observed operation.

The state is private, local and bound to its monotonic clock. Moving it to a
different machine or boot cannot produce a valid elapsed interval. Finishing
seals it; retrying the same final report reconciles an unknown acknowledgement.
Recorder leases recover only after proving the recorded owner process is gone
in the same boot and process namespace. A live owner, unknown boot, legacy or
malformed lease is refused. Recovery neither reruns work nor guesses its status.
A sealed report with a missing output file can be exported again byte-for-byte.
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

Reviewed [PR #87](https://github.com/ziahamza/layercache/pull/87) merged as
[`cb254e9`](https://github.com/ziahamza/layercache/commit/cb254e9af694f6fe25f5a0ad416456174bda3e44).
All thirteen PR gates and the normal workflows on that exact main source passed.
The final macOS job passed sixteen source and sixteen packaged recorder controls,
including split-process monotonic timing, a killed child, immutable output repair
and overlapping owners. The Linux/PostgreSQL jobs passed the real persistence
contract and race suite, including late exact retry, concurrent admission and
restart recovery. Independent reviews covered the protected recorder, read-scoped
report endpoint, explicit pairing, strict decoding, independent quotas and
dashboard. Full Go and JavaScript checks passed.

A real dependency-free Gradle fixture exercised the packaged recorder, local
HTTP cache, Go import and SQL reporting in one declared session. The cold
operation took 6,303 ms; after removing native outputs, the warm operation took
4,485 ms. Both cold tasks executed and published; both warm tasks restored the
same native keys over HTTP with zero executions or PUTs. Class and assertion-output
digests matched. Local and stored comparisons agreed on the observed 1,818 ms
difference. An exact import retry returned the same receipt; an independent SQL
and session audit confirmed the stored pair. This is one fixture pair with shared
OS/bootstrap warmth. It establishes no application, causal, CPU or cost saving.

Gradle owned its HTTP transfers inside the measured work interval, so separate
restore/publication phases stayed unknown. Their overhead was not invented or
subtracted.

The exact merged source is deployed. Fresh authenticated exchange and status
checks advertise the operation-report feature. Thirteen actual-account dashboard
checks passed, preserving access to all five projects and their native and cost
projections. No synthetic operation reports were uploaded. Generic rolling-window
counts can change as their time window moves; this comparison is not proof of
immutable generic history. The source-pinned native CLI is installed locally,
and its offline start/phase/finish, exact sealed retry and unmatched-comparison
smoke checks passed. The immutable
[beta.9 release](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.9)
publishes this source as native Linux amd64/arm64 and macOS arm64 Go binaries.
All three packages passed independent checksum, build-metadata and source-bound
attestation verification. The verified Linux amd64 release is installed locally;
the separate native recorder remains a source-pinned JavaScript package. Fresh
three-platform installed-release acceptance is tracked by the
[published installer workflow](../.github/workflows/beta-release-install.yml).

The application reader repair is merged and its normal main Check passed, with
seventy-two local hook controls. It fixes canonical-path containment after a
successful restore and preserves bounded diagnostics for earlier archive
failures. Retained logs alone do not establish why the historical archive
transfer failed. A compatible hosted restore/readiness/replay receipt is still
coordinated behind the application owner's reserved provider slot. Operation
timer adoption must also surround the actual application lifecycle; the deployed
application currently has no operation reports. No optional native run is started
to fill the dashboard or a measurement gap.
