# Native cache measurement

LayerCache reports completed native work separately from cache transport.
Downloading an archive, finding an action record and accepting a restored task
are different events. The dashboard identifies each integration's units,
reporting support and missing evidence instead of treating missing reports as
zero usage.

The reporting implementation adds Gradle task reports and an offline Xcode
evidence exporter. Source qualification, published client versions, deployed
servers and application adoption are separate gates. The Go import command is
in immutable [beta.8](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.8).
Use the offline Node exporter from its exact source
[`48e4208`](https://github.com/ziahamza/layercache/tree/48e420868b4250c9176ae5aa48127f1456e97208/native);
that bundle is a separate source artifact. Earlier beta.7 clients do not contain
these commands. See [TODOs](../TODOs.md) for application rollout status.

## Gradle

The reporting-enabled [Gradle action](../action/gradle/README.md) discovers
native reporting support during its scoped exchange. Its init script uses
Gradle's public completed-task listener. It records an independent invocation
identity and final `executed`, `fromCache`, `upToDate`, `noSource`, `skipped`,
`failed` or `unknown` outcomes. Configuration-cache reuse must still produce a
fresh invocation report. Reporting does not turn an unsuccessful task into a
hit, alter task keys or change cache publication policy.

`FROM-CACHE` establishes an eligible native hit. A successful executed task
with an exact native key establishes an eligible miss. A qualified native
"Caching disabled for task" diagnostic marks an executed task ineligible.
Otherwise eligibility remains unknown. Up-to-date, no-source and skipped tasks
are shown separately. A zero eligible denominator has no hit percentage.
If executed-task eligibility is incomplete, the whole reported-task hit rate
remains unknown; known counts remain visible.

The qualified native "Skipping task ... as it has no actions" diagnostic also
marks an executed lifecycle task ineligible. Gradle can report that task as
executed because a dependency ran; it has no own cacheable work. The reporter
preserves the native outcome and excludes only its eligibility. A conflicting
key or malformed marker keeps the mapping unknown. This correction applies to
future reports; accepted immutable reports are not rewritten.

Supply one Gradle 9.3.1 or 9.4.1 `--info` log per action instance through
`native-info-log` to correlate task paths with native keys. The parser keeps
multiple task paths sharing one key, but rejects conflicting keys for one path.
Raw logs, task paths, commands and free-form failure reasons stay local. Work
references are opaque hashes scoped to project and compatibility profile.
Without the log, native outcomes remain useful but key, source and producer
timing coverage may be incomplete. Reports are client observations, not an
attestation that task code could not emit misleading diagnostics.
Only those exact versions are qualified. Other versions retain native outcomes
with unknown key, source and producer-timing coverage. The
[9.4.1 qualification](evidence/gradle-941-metering-20261020.md) covers the public
listener and exact log correlation, separately from application adoption.

The action's post step uploads bounded final reports under its own renewed job
authority. It does not use a later step's exported credential. Reporting failure
warns without rerunning or changing the native build result. PR read authority
can report its own job; it cannot publish cache entries.

For offline/local use, enable collection for a private owned directory using
`LAYER_CACHE_GRADLE_REPORT_DIR`, `LAYER_CACHE_GRADLE_REPORT_PROJECT` and
`LAYER_CACHE_GRADLE_REPORT_COMPATIBILITY` when running the init script. The
native CLI exports the same strict projection from one completed invocation
and its log. The Go CLI imports that protected JSON:

```sh
layercache-native gradle-report --project github.com/acme/app \
  --compatibility EXACT_PRODUCING_PROFILE \
  --reports-dir /absolute/private-report-directory \
  --info-log-file /absolute/gradle-info.log --output /absolute/native-report.json

layercache report native --integration gradle \
  --config /absolute/project.json --input /absolute/native-report.json --json
```

The producing project's complete compatibility profile is required. An optional
`--source-commit` accepts an immutable commit, explicitly caller-declared. The
importer does not substitute its checkout HEAD or report its own hardware as
the producer's hardware.

## Original producer timing and cache source

Gradle's native cache accepts equivalent duplicate archives for one key. A
successful PUT therefore does not prove the caller published the retained
bytes. LayerCache records the original committed publication only when its
store confirms creation, binding the retained digest and signed job authority.
Later duplicate PUTs cannot replace that origin or its resource sample.

The dashboard attributes a native accepted restore to a cache source only when
the exact key, profile, signed job and task time window uniquely match a
completed HTTP delivery. Otherwise it remains unattributed. Parallel tasks and
composite builds are not joined using request order. A successful HTTP GET
without native acceptance is not a task hit.

A duration estimate needs a unique successful executed task matching the
earliest observed committed publication of those retained bytes. Missing
reports, ambiguous matches or missing durations
leave that hit's timing unknown. Gross avoided task elapsed time sums those
matched producer durations. Net avoided task elapsed subtracts the corresponding
reader task duration, including native restoration. Negative observations stay
negative. These sums can overlap across parallel tasks: they are neither
critical-path savings nor measured CPU consumption. Native graph coverage is
currently unavailable, so native build-time savings remain unknown.

This reporter cannot reconstruct missing original task-event timing for
archives first published before native instrumentation. A later duplicate
upload cannot replace that missing observed origin. Manual report imports have their own observation authority;
they do not impersonate a different producing run to recover its transport
provenance. Native outcome counts can still be useful when those joins are
unavailable.

Gradle 9.3.1 and 9.4.1 also write an archive-native estimate in
`METADATA.executionTime`: original work execution plus output snapshotting,
in milliseconds. It is a different interval from a public task-finish event.
[Gradle's origin writer](https://github.com/gradle/gradle/blob/v9.4.1/platforms/core-execution/build-cache-base/src/main/java/org/gradle/caching/internal/origin/OriginMetadataFactory.java#L44-L75)
and [duration composition](https://github.com/gradle/gradle/blob/v9.4.1/platforms/core-execution/execution/src/main/java/org/gradle/internal/execution/steps/CaptureOutputsAfterExecutionStep.java#L94-L106)
document that estimate. LayerCache does not extract it yet. A bounded decoder
could recover an explicitly archive-declared estimate from older retained bytes;
it would not recover signed producer, hardware or billing provenance. The
[upstream tar reader](https://github.com/gradle/gradle/blob/v9.4.1/platforms/core-execution/build-cache-packaging/src/main/java/org/gradle/caching/internal/packaging/impl/TarBuildCacheEntryPacker.java#L168-L215)
can consume later metadata members, so a prefix alone cannot establish the
final consumed value. A decoder must verify exact bytes and bounded stream
structure, handle Java Properties semantics and reject ambiguous metadata.
Keep that estimate separate from observed task elapsed time and CPU savings.

When available, the first publication freezes the original execution's recent
resource sample. The lookup requires exact primary execution authority, not an
alias, a physical-host guess or the latest inventory row. A later reader cannot
overwrite the producer's sample. Resource samples remain client-reported. Provider identity
is signed for GitHub-hosted execution and otherwise declared or unknown. If an
entry is deleted and later recreated with identical bytes, this version cannot
prove which storage generation a historical restore used. It does not present
the earliest observed origin as a current-generation receipt.

## Xcode

The offline `layercache-native xcode-report` command accepts the supported
compiler-log format, final cache-service stats and optional measured lifecycle
phases. It emits private bounded JSON and performs no authentication, cache
traffic or native compilation. Import it under an authenticated project member:

```sh
layercache report native --integration xcode \
  --config /absolute/project.json --input /absolute/native-report.json \
  --source-commit IMMUTABLE_COMMIT --json
```

This is member-reported external-build evidence, not Apple workload
attestation. The reporting credential grants neither cache publication nor
administration. Scoped snapshot grants remain archive-only and cannot submit
reports. Do not copy a member login or general project token into Xcode Cloud
to make reporting work; import the exported evidence through an authorized
controller instead. For example, after retaining the complete log, final stats
and actual invocation timestamps:

```sh
layercache-native xcode-report --format xcode27-replay-v1 \
  --log-file /absolute/xcodebuild.log --stats-file /absolute/final-stats.json \
  --invocation-id run-UNIQUE_BUILD_ATTEMPT \
  --started-at RFC3339 --finished-at RFC3339 --status success \
  --output /absolute/native-report.json
```

The [pinned source notes](xcode-metering-source-notes.md) describe the accepted
terminal diagnostics. Intermediate local misses and duplicate generic cache
notes are not counted twice. Swift batch headers are not a compiler denominator.
Malformed, truncated, ambiguous or unsupported input retains explicit coverage
limits. A scoped compiler-verdict percentage is available only with complete
eligible verdicts in that scope; it is not a whole-build Xcode hit rate.

The rendered-log qualification also accepts exact `TEST BUILD` completion,
bounded target/project diagnostic suffixes, and the observed SGR formatting
codes `0`, `1`, `33` and `36`. Other terminal controls remain unsupported, and
input bounds apply before formatting removal. Native module and bridging-header
tasks use their existing replay actions. The `swift` scope means Swift-driver
replay observations, which can include Clang module jobs; it does not establish
Swift-language external replay. Apple artifact logs may add a leading UTC
timestamp and tab to each record. Preserve the original and remove only that
verified record prefix before offline export; do not rewrite diagnostics or
synthesize completion. See the [native log qualification](evidence/native-log-coverage-20261020.md).

Action-index reads/hits, CAS reads/hits and writes are protocol counters.
Compiler verdicts are a separate unit. An action lookup hit does not prove a
compiler replay, and snapshot restoration does not prove either. Supplied
operation, compile, load, restore, close and snapshot phases measure elapsed
time, not savings. Xcode currently has no original action-duration join, complete
task graph, measured CPU saving or billing saving.

## Reporting contract and limits

The eligible exchange advertises `nativeReports: {version: 1, path:
"/v1/reports/native"}`. The POST body contains version, invocation identity,
bounded timestamps, final status, task outcomes and optional counters/phases.
Project, profile, run, job and source authority come from the signed capability;
the body cannot choose them. Local OSS reporting requires the matching Local
installation. Hosted reporting rechecks current project membership.

Final reports are immutable per signed project, run, workspace/job, integration
and invocation. An identical retry returns the same acknowledgement; a changed
report or source for that identity conflicts. Reuse the exact input after an
unknown acknowledgement. Do not invent a new invocation to repair an uncertain
response.

Limits are 4 MiB per input, 4,096 task rows per invocation, 32 invocations per
exact signed job and integration, and 30 days of retained history. A project
admits at most 4,096 receipts, 65,536 task rows and 32 MiB of canonical report
documents. Optional transport provenance separately admits 65,536 rows and
32 MiB of documents. Capacity refusals affect reporting coverage, not cache
publication or native execution. Reports must describe a completed
invocation within the preceding 24 hours. History outside retention and known
quota-related admission gaps are marked incomplete. Report-loss markers follow
the refused invocation's start time. Transport-loss markers follow each selected
invocation's full report/task window, including the five-second matching grace,
even when that window ends after the dashboard period or the loss arrives later.
Daily ranges retain at most 124 rows per project across two integrations and
two loss kinds over 31 UTC days. Gaps within one marked day's range remain
conservative. A later refusal does not mark an earlier unrelated period
incomplete. Cache transport provenance uses an optional 100 ms recording budget
and indexed quota counters; missing evidence leaves source or timing unknown.
Optional reporting never promises artifact retention or future
availability. Required transfers continue to use the retained-artifact contract.

If native reporting cannot be read, the endpoint preserves available generic
outcomes and graph estimates and sets `nativeReportsUnavailable: true`.
Gradle/Xcode coverage then says reporting is unavailable rather than implying
zero work. Generic Turbo, Actions and BuildKit estimates retain their own scope;
native evidence neither erases them nor qualifies whole-project savings.

## Coverage audit

These are independent units. Combining their counts would produce a misleading
overall percentage. Existing generic task/vertex/archive reports retain their
own denominator; native evidence is shown alongside them.

| Integration | Working evidence | Why a dashboard value may be unknown | Next adoption or implementation step |
| --- | --- | --- | --- |
| Turborepo | Completed Run Summary task outcomes through the Turbo action or CLI | Raw artifact traffic is insufficient; summaries may be absent in the selected period | [Enable build-time reports](../action/turbo/README.md#build-time-reports) |
| GitHub Actions cache | Archive restore/save outcomes | Archive scope is not native compilation; miss-to-save intervals may include unrelated work | [Use the Actions cache endpoint](actions-cache-contract.md) and interpret archive units |
| Docker / BuildKit | Native vertex outcomes through local `layercache buildx` | Hosted registry configuration does not upload completed native vertex reports; hosted RemoteOnly upload is unsupported | Preserve local results; implement the authenticated hosted native reporter before claiming hosted rates |
| Gradle | Completed native task reports; qualified eligibility and optional exact key/source/producer timing | Old action pins, absent final report, incomplete eligibility, missing log or ambiguous source/timing join | Adopt the reporting-enabled action and one completed `--info` log; keep filesystem hits separate from HTTP attribution |
| Xcode | Supported terminal compiler verdicts, final RPC counters and explicit lifecycle phases | Unsupported/partial compiler scope, no original action timing or native task graph | Export evidence with the supported parser, import through a member/controller, and retain coverage labels |
| Nx | Native HTTP cache; native run-result parser in qualification | No production completed-task outcome uploader | Reuse the native run-result seam in a scoped reporter; GET counts are insufficient |
| Bazel | Native AC/CAS cache; Build Event Protocol parser in qualification | No production action-outcome uploader; separate native engine bytes are outside shared artifact totals | Add a bounded BEP outcome reporter without changing Bazel action identity |
| Expo | Provider validates an app artifact and emits source/bytes/restore duration | No hosted app-restore outcome uploader; shared archive metadata does not identify all snapshot purposes | Report validated app restores as app artifacts, with explicit purpose metadata independent of keys |
| LLM replay | Local replay/fresh/provider-call events and provider usage metadata | No hosted event ingestion or dashboard replay projection; current calls and billing differ from recorded historical usage | Add a separate inference reporter and denominator; preserve fresh-evaluation bypass and actual current provider calls |
| Retained artifacts | Local publication/provenance and verified transfer receipts, plus server timing | No dashboard transfer-event ingestion or projection; storage totals do not establish transfer performance or avoided build work | Add transfer phase and retention coverage separately; retain required fallback until application acceptance |

Compiler snapshots, dependency archives and other opaque shared transport cannot
be classified as Gradle tasks, Xcode actions or Expo apps by filename or hash.
Explicit purpose metadata is still required where the archive protocol is
shared. Storage coverage also excludes independently managed registry/native
engine stores until their own accounting is integrated.

An LLM replay verdict can still follow a current provider call during publication
contention. Preserve `currentProviderCalls` rather than assuming every replay
means zero new inference. Historical token usage is not current paid usage.

## Compute and cost

The dashboard does not multiply task elapsed sums by a runner's vCPU count.
Concurrent tasks share a runner; doing so can count the same allocation many
times. Allocation saving requires a qualified change in complete worker/job
allocation, including setup, cache overhead, remote workers and failed attempts.
Measured CPU saving additionally needs actual CPU-use evidence. A fresh host
inventory sample cannot supply either number for historical work.

Money saving needs those compatible allocation observations plus the applicable
provider SKU, date, rate, minimums, rounding, included allowance and invoice
reconciliation. Public-runner compute can be reduced while runner charges remain
zero. No current metering report establishes monthly or invoice savings. See
[measurement policy](ci-optimization/measurement.md) for the three clocks and
the experiment receipt. The remaining work is tracked in the single
[TODO list](../TODOs.md), including producer resources, hosted native collectors,
snapshot purpose and pricing evidence.
