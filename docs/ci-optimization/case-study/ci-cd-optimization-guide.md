# Faster CI without losing the evidence that matters

Prepared on October 17, 2026, from a mobile application's CI work and earlier build-cache experiments. This case study accompanies the [CI optimization recipes](../README.md). The case measurements are anonymized, bounded observations, with their limitations in [the measurement record](case-study-measurements.json). They are not a forecast of another project's savings.

The largest gains came from avoiding unnecessary execution, reusing compatible finished products and moving orchestration to Linux. Compiler caching helped where it actually received compatible work. Several expensive failures happened before compilation started.

## Decide what a passing change must prove

Write the acceptance contract before changing triggers. Separate compilation, current-source checks, runtime behavior and release publication. A cached APK proves that compatible native code was built. It does not prove that today's JavaScript, contacts fixture or call flow works.

Moving a second browser from every PR to main reduces pre-merge coverage and delays detection. That may be an acceptable policy, but document it. To preserve an existing acceptance contract, skip a check only when its relevant inputs and previously successful evidence meet an explicit reuse rule. A cheaper pipeline that silently checks less has changed the contract.

Start by recording why each job exists, the exact command it executes, its inputs, outputs and downstream consumers. A Turbo cache cannot accelerate a direct command that never invokes Turbo. A task with `dist/**` declared as its output cannot restore a deployed `.output/**` product. Inspect the actual restored product, not only the cache status line.

## Measure the whole pipeline

For each attempt record queue delay, provisioning, dependency setup, profile discovery, compilation, transfer, runtime checks, post actions and complete job duration. Record the run's critical path separately from the sum of job durations. Parallel work can reduce waiting while leaving total resource use unchanged.

Measure allocated core-minutes, actual CPU, peak memory, available memory, swap, major page faults and OOM events. Keep failed, cancelled and seed runs in the accounting. Provider invoices, allocated compute estimates and GitHub's displayed usage are different measurements. Moving Linux execution to another provider does not move GitHub artifact storage or remaining GitHub Mac charges.

An instructive runner trial completed signed Android jobs in 20m40s on eight cores, 21m44s on four and 27m55s on two. Four cores used 47.4% fewer allocated core-minutes than eight for 64 seconds more waiting. The two-core job passed, but used 2.43 GiB of swap and had 95,589 major faults. Its lower resident memory did not mean it needed less memory. SDK cache warmth and recipe differences limit this comparison; it is not a CPU-only benchmark or an invoice calculation. See measurement `signed-runner-sizing`.

Count seed cost as well as reader savings. In one APK experiment the main publisher took 912 seconds, versus 417 seconds for its previous narrower job. A matching reader saved 318 seconds. Under comparable conditions, roughly two matching readers would repay the additional 495 seconds. Fingerprint churn, misses and provider billing can change that result.

## Stop superseded work early

Use one PR identity across its commits, with a workflow-specific concurrency group. A group containing only the commit SHA cannot cancel an older SHA. Restrict branch push triggers so the same PR commit does not also start an unnecessary push pipeline.

```yaml
# Example for validation workflows, not store publication.
concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}
```

GitHub cancellation controls its workflow jobs. External build and simulator controllers must propagate cancellation too. [GitHub concurrency documentation](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency).

Have the controller save its owned remote job or session ID, forward termination, and use a short independent cleanup budget. A reader of a shared producer must not cancel that producer. Distinguish a cancellation request from a confirmed terminal state. Runner death can bypass cleanup, so provider deadlines or leases and an orphan reconciler are useful backstops. Those backstops are recommendations, not capabilities established by the case measurements.

Serialize externally visible release transactions and reconcile unknown outcomes before retrying. A timed-out upload may have succeeded. Cancelling or rerunning it blindly can create duplicate releases. Default concurrency serialization also does not promise that every pending release will run; choose an explicit queue and reconciliation policy when every version must publish.

## Select work conservatively, then cache successful tasks

A change planner should include source, imported fixtures, build configuration, dependency locks, executable test data and workflow recipes. Handle renames, deletions, merge parents and the PR's actual tested merge source. Unknown paths, an unavailable comparison or failed classification should select the ordinary checks.

Qualify trigger filters using the platform's actual matching rules, including dotfiles, ordered negative patterns and unsupported syntax. A local oracle using another glob language can report a reassuring result while omitting files that the real workflow treats differently.

Preserve existing required check names and coverage when introducing a required aggregate result. It may accept a skipped job only when the planner explicitly excluded that job. A selected job that is cancelled, absent or failed must fail the aggregate. This prevents conditional jobs from making branch protection appear satisfied while required work disappeared. Follow the [required-check recipe](../workflow-structure.md#keep-required-checks-present) when applying this policy.

For task caching, include the actual selected runtime, package manager, OS, architecture, locks, command and relevant inputs. Do not label a cache with a fixed Node version while installing a floating major. Avoid unrelated workflows racing to save different snapshots under one immutable key. A source-equivalent result still needs the right task, toolchain, success status and age policy.

On a two-core Linux runner, give the test executor useful file-level parallelism, then cap nested pools. Two browser workers, two package tasks and two inner test workers can create far more runnable work than two cores can sustain. Measure combined CPU and memory before adding more jobs. Parallelizing a 50-second task and a 35-second task can save at most 35 seconds of their combined critical path, before contention and setup.

## Give each reusable object its own identity

| Object                   | What its identity must describe                                                                       | What a hit permits                                    |
| ------------------------ | ----------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| Downloaded dependencies  | Package ecosystem, lock inputs, platform where needed, install recipe                                 | Avoid downloading bytes; still validate installation  |
| Build task outputs       | Engine task implementation, declared inputs and output contract                                       | Restore compatible task outputs                       |
| Compiler results         | Compiler, flags, headers, paths and supported preprocessing semantics                                 | Restore compatible compiler outputs                   |
| Native development shell | Native modules, plugins, native configuration, toolchain, ABI and build mode                          | Install native runtime and load current JS separately |
| Bundled release app      | Native inputs plus bundled JS, assets, configuration and build environment                            | Reuse that exact complete package                     |
| Successful QA result     | App identity, current source where relevant, fixtures, driver, coverage, environment and producer age | Reuse only the covered successful verdict             |
| Required job handoff     | Exact producer and consumer context, immutable content and retention obligation                       | Transfer required bytes during the promised interval  |

These are separate contracts. A failed runtime check may leave a valid compiled app worth caching. It must never create a successful QA receipt. Reading a successful QA receipt must preserve its original production time rather than renew its age.

Use separate schema versions for these identities. A native fingerprint can omit JS only when the runtime deliberately loads current JS and fresh checks exercise it. A bundled release fingerprint must include that JS. Before excluding a test file, check whether app code imports it. Before excluding a package manifest, check whether it changes native dependency selection or executable scripts.

The source commit is useful provenance. It is usually too broad as the sole compatibility identity for reusable native tasks. Conversely, a toolchain compatibility profile alone is too narrow to identify a complete app. Preserve both compatibility and original source provenance in receipts.

## Use the build engine's protocol

Let Gradle determine task keys from its actual inputs and implementations. Check that tasks declare the outputs consumers really need, then diagnose the first unexpected miss by comparing input hashes. [Gradle build cache](https://docs.gradle.org/current/userguide/build_cache.html), [Gradle cache debugging](https://docs.gradle.org/current/userguide/build_cache_debugging.html).

Qualify duplicate handling for the selected native protocol and backend. Gradle's archive includes per-invocation origin metadata, so equivalent task outputs can have different container bytes. This follows from the [Gradle 9.3.1 origin writer](https://github.com/gradle/gradle/blob/v9.3.1/platforms/core-execution/build-cache-base/src/main/java/org/gradle/caching/internal/origin/OriginMetadataFactory.java) and [archive packer](https://github.com/gradle/gradle/blob/v9.3.1/platforms/core-execution/build-cache-packaging/src/main/java/org/gradle/caching/internal/packaging/impl/TarBuildCacheEntryPacker.java). The [HTTP cache contract](https://docs.gradle.org/current/userguide/build_cache.html#sec:build_cache_configure_remote) accepts a 2xx PUT response; it does not mandate first-writer-wins. A backend can acknowledge a legitimate duplicate while retaining its valid existing entry, subject to qualification for that integration. Required artifacts instead bind one declared full byte hash. Applying that immutable-byte conflict rule to every native task upload can break legitimate clients.

Discover the compatibility profile from the prepared project and selected task graph. One app selected two NDK revisions across projects and a separate CMake version; its root defaults alone missed that. Variant names also hid a native model folded into another variant. Include the actual selected JDK, Gradle, Android plugin, SDK, NDKs, CMake, ABI and build mode as required by the integration. Do not put credentials, elapsed time or the cache client's binary digest into native toolchain compatibility.

Prepare once. Record the relevant authored files, generated files, non-ignored untracked source and environment, discover the profile, authenticate, then build that same prepared tree. Verify the preparation receipt before compiling. Re-running prebuild after profiling can invalidate the profile or make the cache describe a different project.

Keep bootstrap and application cache attribution separate. Disabling every local Gradle cache during profile discovery can force included plugin builds cold. Qualify that discovery cannot contact the remote before its profile is known, while retaining safe local bootstrap reuse. Scope init scripts to the intended project; included builds execute them too.

A narrow trusted publisher can seed useful JVM and resource tasks without assembling every native product. Guard the resolved task graph before task execution so a dependency change cannot quietly add signing, packaging, device testing or a full C++ build. Include profile discovery and publication overhead in the economics.

In one independent local producer/reader pair, whole Gradle command time fell from 880.213 to 524.971 seconds. The reader restored 318 tasks, downloaded 148,112,872 bytes and issued zero PUTs. Selected output trees matched. A separately copied C++ cache gave zero of 94 hits. That was Gradle task reuse, not demonstrated compiler reuse, hosted-job savings or device acceptance. See measurement `local-gradle-task-cache`.

## Diagnose compiler misses without weakening correctness

Count total calls, cacheable calls, hits, misses and reasons for refusal. In that local pair, 206 calls were rejected for precompiled headers and five were link calls. Another hosted configuration later achieved 94 of 94 cacheable hits. These results describe different path and environment conditions; combine neither their denominators nor their conclusions.

Investigate one ordinary translation unit first. Compare debug hash inputs across a same-root control and independent roots. Check working directory, external dependency paths, debug information and generated headers. Qualify explicit prefix maps or stable logical roots against uncached output before broadening them.

Do not turn on `NOHASHDIR`, `time_macros` or `pch_defines` just to raise a hit counter. ccache documents that PCH support requires relaxed checks with correctness tradeoffs. Treat PCH as a separate qualification task. [ccache manual](https://ccache.dev/manual/latest.html#_precompiled_headers).

For Xcode, distinguish local native CAS replays, a remote adapter's requests and actual service downloads. A local replay count with zero adapter RPCs does not prove remote cache reuse. Use a fresh DerivedData and local CAS control when attributing service hits. A C++ launcher that receives zero cacheable work should not remain enabled on the assumption that it helps Swift or Xcode.

## Reuse finished APKs across PRs

When native assembly dominates residual work, cache the compatible APK itself. Keep package name, ABI, build type, native fingerprint, toolchain profile, package SHA-256 and original producer source in a manifest. Verify them before installation. Current-source Kotlin checks and current JS runtime checks may still be required even when native assembly is skipped.

One safe policy is trusted-main publication, PR read-only access to that shared namespace, then a PR-local fallback. Keep namespaces explicit. Otherwise a recently saved PR-local entry may shadow the compatible main product forever. A key prefix is a lookup rule, not an authorization boundary; verify the provider's actual read/write policy separately.

Do not assume GitHub's REST cache inventory controls a third-party cache provider. Qualify the actual backend, branch scope, inventory and immutable-save behavior. A normal cache hit log does not identify its original producer. Record the full restored key and compare the package's manifest and hash.

Repair a broken immutable entry with a new generation under the same full compatibility fingerprint. Readers may search that fingerprint's generations. They must not fall back to a weaker fingerprint. Do not publish duplicate snapshots when a valid compatible product already exists.

In the cross-PR case, the reader's complete compile job fell from 670 to 352 seconds and assembly was skipped. It still ran current-source Kotlin validation and eight fresh device assertions. The runtime job itself took 349 seconds. This is one hosted pair, with separate main-seed cost, not a claim that total CI or monthly spending fell by 47.46%. See measurement `cross-pr-apk-reuse`.

## Bound setup and preserve an uncached path

An hour-long build can be a memory failure loop or an SDK download that never reached Gradle. Check the last completed phase before buying a larger runner. Fail promptly on a real JVM OOM. Allocate Gradle, Kotlin and native workers from the same measured memory budget.

Use preinstalled SDK components only after validating their exact revisions and executable files. Install missing selected components explicitly. Avoid repeated license-index and command-line-tool downloads when the image already supplies a qualified SDK. If automatic SDK downloads are disabled, missing components fail clearly instead of triggering an unbounded network phase.

Partial restoration is not a valid installation. After a failed extraction, clean only the owned selected package directories and restore or reinstall them. Use fresh temporary package-manager stores after interrupted setup; overlaying a failed installation can produce unrelated directory errors. Keep emulator images on runtime runners even when compile runners can reclaim them.

Bound total optional restore time as well as per-request or segment timeouts. A progress loop can survive a failed transfer. After a bounded optional-cache failure, perform the pinned frozen install or ordinary build. Do not hide compiler errors or corrupt accepted outputs behind a broad `continue-on-error`.

For optional app and QA snapshots, reject corrupt, expired or incompatible entries before accepting them, then perform normal work. A required handoff that was accepted as published must fail on corrupt or missing bytes. Keep recovery policy specific to the object; never turn a real compilation or runtime failure into an optional miss.

Set JVM network policy before JVM startup, and qualify repository routing across plugin management, buildscript dependencies, project dependencies and included builds. A flag passed to SDK Manager alone does not configure every child JVM. Route artifacts to the repository that actually hosts them instead of repeatedly probing an unavailable mirror. Keep enough disk for both restoration and expanded outputs.

## Keep Apple orchestration on Linux where supported

Evaluate operations separately. Source preparation, package inspection, store API calls, processing polls, groups and review reconciliation can run on Linux when the selected tools support them. Reserve Mac execution for the remaining qualified archive and simulator work. Allocate that Mac only after Linux has determined that a compatible accepted result cannot be reused.

Check current tools before claiming that an Apple operation requires macOS. App Store Connect CLI now documents a Linux iOS packaging path for one thin arm64 app plus resources, rejecting extensions and embedded frameworks. Its documentation does not establish toolchain licensing or App Store acceptance. Qualify signing, packaging and distribution requirements for the actual app before choosing it. That documentation is not evidence that an arbitrary existing Xcode product can move unchanged. [ASC CLI](https://github.com/rorkai/App-Store-Connect-CLI), [Linux iOS workflow](https://github.com/rorkai/App-Store-Connect-CLI/blob/main/docs/linux-ios.md).

Use the provider's supported simulator-session product for interactive or automated QA. Arbitrary access to a build Mac does not establish a supported simulator lifecycle, XCTest runner, accessibility API or audio contract. Request explicit support for the workload and qualify it with the real app. [RunCloud simulator sessions](https://docs.run.cloud/ios/run-simulator), [RunCloud Xcode builds](https://docs.run.cloud/ios/build-with-xcode).

Recheck upstream capabilities instead of freezing an old workaround. On the guide's checked date, RunCloud documents native-shell rebundling for compatible Expo Release simulator builds, immutable Asset IDs reusable across sessions, and early installation before durable publication finishes. These are candidates for application qualification, not results measured here. Device signing is outside the documented rebundle path. Early installability also does not establish the required downloadable-artifact retention guarantee. [Current RunCloud build lifecycle](https://docs.run.cloud/ios/build-with-xcode).

For app and QA identities, capture actual CPU architecture, Node patch version, macOS build, Xcode build, SDK build, simulator runtime and device where relevant. Choose an archive format that preserves long paths, symlinks and executable modes. Verify a restored app's inventory and integrity before execution.

Keep native preparation compatible with the repository's package manager. A provider's generic npm Expo preparation failed on workspace dependencies in the case history. Preparing the native project on Linux and using frozen workspace dependencies on the remote builder avoided repeating that incompatibility. Ensure codegen, autolinking and bundling use the same selected Node executable, including paths containing spaces.

Cache complete successful QA with an explicit maximum producer age and coverage identity. A standard-font, small-fixture manual pass does not qualify a dense 5,000-contact XCTest run, another appearance, device contact writes or carrier audio. A failed full UI run must not publish a success receipt because startup or CallKit passed.

Exact-source general-check reuse can remove repeated release preparation. Verify the tested source, trusted workflow, latest successful attempt and required aggregate status. Re-read metadata to detect a concurrent rerun. Missing or stale proof should run normal validation.

Delete unused optional validation jobs and their configuration once their removal is justified. Keep checks that the delivery channel actually requires. Checking restored executable integrity is a separate decision from running an optional store signature-audit job.

Provider pricing changes. A free beta is not a permanent architecture or a complete cost model. Count Linux controllers, session activity, storage, queue time and failed remote attempts as well as archive charges. Recheck [provider pricing](https://run.cloud/pricing/) when budgeting.

## Separate optional caches from required transfers

A cache can miss and regenerate its output. A required handoff must keep the exact accepted bytes available until its downstream obligation ends. An acknowledged optional cache upload is insufficient evidence of that retention guarantee. GitHub makes the same conceptual distinction between dependency caches and workflow artifacts. [GitHub caching concepts](https://docs.github.com/en/actions/concepts/workflows-and-actions/dependency-caching).

Choose a transport with immutable declarations, content verification, admission control and a retention deadline that includes queue delays and allowed reruns. A reserved upload is not publication. A lost upload response is an unknown outcome; inspect the authoritative receipt before uploading the entire file again. The reader still verifies full bytes, the payload manifest and safe extraction.

Stream whole bodies instead of imposing serial per-chunk round trips. Separate hashing, authentication, metadata, headers, body transfer, commit and local verification in phase receipts. Record byte counters and overlap. A 229-second download-plus-verification phase cannot be treated as 229 seconds of network throughput.

Two comparable-size handoff generations produced a stored ZIP of 124,109,619 bytes and a level-1 ZIP of 65,433,617 bytes, a 47.28% container-byte reduction. Their source TARs had the same size but different digests, so this is not an identical-payload compression control. Both upload steps rounded to six seconds. The byte comparison establishes no time or bandwidth-cost saving. Avoid redundant producer download-and-hash cycles when publication can be authoritatively reconciled; preserve independent consumer verification. See measurement `artifact-compression`.

Bind handoffs to the repository and numeric ID, authorized source, workflow, run, producer attempt and signed producer job identity, profile, logical name, full hash, size and fixed deadline. Verify checkout HEAD against the authorized source. OIDC provenance identifies the authorized producer context; it does not by itself attest embedded package contents or prove how the build process produced them.

An explicit receipt may allow a later consumer-only rerun to read an earlier producer attempt when the service contract permits it. Require exact context and receipt matching. Do not silently select the latest object or another profile. Keep native-cache writer authority separate from required-artifact publisher authority.

LayerCache's [retained-artifact contract](https://github.com/ziahamza/layercache/blob/ab3b63b2b189a557303202ddaec035d247379e0f/docs/retained-artifacts.md) is one concrete implementation. Its documented deployment limits are not universal retention recommendations. Service canaries and recovery tests must be followed by real CI identity, fresh application producer/consumer and consumer-only rerun acceptance. At this snapshot those application gates remain pending, so GitHub artifacts remain the required-transfer path.

## Make retention an operational policy

Set short retention for reproducible QA handoffs and longer retention only for diagnostics or release obligations that need it. Include queue and rerun windows. Retention is a capacity promise, so reserve space before accepting an obligation and account for active pins, reservations and free-space reserve. A project quota does not manufacture physical capacity.

Recovery, restart and rollback must preserve accepted objects and their deadlines. Publication, retention claims and deletion need an atomic fence so cleanup cannot remove required bytes in a race. Expiry must release both storage claims and quota. Qualify these properties using real backends, not only an in-memory fixture.

Changing future workflow retention does not shorten the expiry of existing artifacts. Inventory existing bytes, producer workflows, age, rerun obligations and release evidence before cleanup. Distinguish expired-artifact cleanup, cache eviction, object-store capacity and compute billing. Each has a different remedy.

## Qualify savings before widening rollout

Use the smallest experiment that can falsify the intended claim, then retain the evidence:

1. Establish an ordinary successful build and record its complete phases and output inventory.
2. Run a cold producer, then an independent reader with an empty task cache and explicit policy for compiler seeds. Record exact task restores, remote bytes, writer requests and output equality.
3. Change a relevant input and prove it invalidates the expected work. Run an invalid compile twice and prove failure is not published.
4. Exercise read-only, foreign-context, outage, partial transfer, corruption and quota controls. Optional outages should permit ordinary work; invalid accepted bytes should fail.
5. Run the real application's current-source and fresh runtime acceptance. A compile-only package inspection or operator-minted canary cannot substitute for these.
6. Exercise consumer-only reruns and remote cancellation. Keep producer provenance and original result age intact.
7. Compare complete jobs and a representative billing window, including publishers, misses, failed attempts and storage. State which variables differed.

Pin adopted actions and installers to reviewed immutable sources. Verify checksums or attestations through the supported installation path, then test the published installed binary or direct action. A local source fixture alone does not qualify the released distribution.

Keep raw logs and credentials private. Publish minimal receipts with the measured phases, versions, task outcomes, bytes and limits. Test redaction against actual failure syntax, including escaped delimiters. Summaries and progress messages are not proof of completed commands. Reconcile final task-graph records with actual output trees and service observations before counting hits.

## Follow-ups worth asking providers for

For simulator services, verify a supported native XCTest runner and result bundles, retained logs on timeout and cancellation, explicit session expiry, and audio input/output with timing and interruption controls. For build services, verify Xcode selection, cache import/export hooks, archive and dSYM retrieval, cancellation confirmation, and timestamps separating queueing from execution. Request missing capabilities. This is a capability checklist, not a claim that a named provider lacks every item. Verify the required behavior with a real workload before relying on it.

Evaluate faster upstream build paths after the workflow is measurable. Expo SDK 58 advertises prebuilt Expo Modules Core and a faster blank-app Android build, but that is not a measurement of this application. A framework upgrade still needs dependency, toolchain and runtime qualification. [Expo SDK 58 changelog](https://expo.dev/changelog/sdk-58#faster-android-builds-with-prebuilt-expo-modules-core).

Begin with the largest avoidable execution or setup phase in your own traces. Keep the same acceptance target, change one decision, and compare the complete outcome. A cache hit is useful only when it removes enough verified work to repay the cost of producing, retaining and restoring it.
