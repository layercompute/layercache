# LayerCache TODOs

Last strategic review: 2026-10-15. This file owns outstanding work. Checkboxes
mean that every acceptance target is satisfied, not that a prototype exists.
[Launch status](docs/launch-plan.md) records delivered/deployed qualification;
[roadmap review](docs/roadmap-review.md) explains how to reprioritize.

The next milestone is reliable Phone handoffs plus less repeated test and
inference work across compatible local machines and CI. Reuse established
build/test/model clients. Prefer fewer expensive executions over higher cache
hit counts alone.

## P0: reliable Phone reuse

- [ ] **Qualify required file handoffs independently of evictable caches.**
  Owner: Phone workflow/held-stack owners, coordinated with LayerCache.
  [Phone #299](https://github.com/ziahamza/phone-app/pull/299) repairs exact
  retained web/APK handoffs and qualifies hosted producer/consumer use;
  [Phone #305](https://github.com/ziahamza/phone-app/pull/305#issuecomment-6178375570)
  qualifies the production signed-IPA handoff through Linux inspection and ASC;
  [Phone #273](https://github.com/ziahamza/phone-app/pull/273) remains the
  historical held-stack reference. Prove an exact producer-to-consumer
  transfer and consumer-only rerun with source/run/attempt/job/digest checks.
  Required publication fails visibly. The separate fourteen-day release-package
  policy does not establish this handoff. Preserve the held stack's unrelated
  acceptance requirements.

- [ ] **Roll out shared cloud rejection of failed input streams.**
  Owner: LayerCache storage and gateway rollout. The October 15 native Gradle
  qualification exposed a shared S3 SDK edge case. A failed `io.Reader` ending
  in `io.ErrUnexpectedEOF` could publish a partial artifact under an Nx or Turbo
  key. Source `3d5e2df8aa9530dc1b80aaa1f87ea77233f3f2bd` guards the shared staging
  boundary with wrapped, retained input failures and a final publication fence.
  [Evidence](docs/evidence/cloud-stream-integrity-20261015.json) covers actual
  PostgreSQL and the production SDK, including failure after an observed part
  upload, zero remaining metadata/objects/parts with a working endpoint, valid
  complete multipart bytes/digests and unchanged quota refusal. Preserve the
  global error sanitizer. This does not explain the separate historical Actions
  lookup miss or qualify cleanup during an object-endpoint outage. Next: qualify
  the existing private gateway upgrade and installed release independently;
  no operator configuration or deployed service changes in this source fix.

- [ ] **Explain acknowledged Actions uploads that miss before consumption.**
  Owner: LayerCache; investigation in progress. Migrated from
  [issue #34](https://github.com/ziahamza/layercache/issues/34).
  The reported 124,108,800-byte acknowledgement followed by GET lookup HTTP 204
  is unexplained; 204 denotes a miss. Establish an isolated PostgreSQL/S3
  upload/finalize/lookup/restore loop at deployed `16868d9` and current source.
  Verify exact lookup/profile/ref identity and full bytes, including concurrent
  maintenance. Distinguish eviction, expiry, integrity rejection, scope mismatch
  and interrupted commit with bounded evidence. Add meaningful regression
  coverage for any proven bug and document acknowledgement/read-back guarantees.
  Qualify a separate gateway rollout if a service fix is needed.

  Evidence on 2026-10-14: [#36](https://github.com/ziahamza/layercache/pull/36)
  merged reviewed source `276400c70b1fe4d0a65f069f73b877ba674200eb` at
  `fc98937922f69daad788a89cc132845ff4c0c79e`. It adds the
  [acknowledgement/miss contract](docs/actions-cache-contract.md) and a real
  PostgreSQL 16/production S3 SDK loop with a disposable HTTP S3 fixture.
  Deployed `16868d9` and current source both restore a varied 124,108,800-byte
  payload with complete size/SHA-256 verification after commit acknowledgement.
  Controlled quota, expiry, deletion, integrity, scope, failed-copy and lost
  response cases distinguish expected behavior; pin, healthy-fallback and
  concurrent-maintenance/read-lease controls pass. The bounded miss header
  reports only this lookup's observations, not a durable removal history.
  The historical cause remains unproven; no backend bug was reproduced.
  Next: obtain exact request identity and authoritative removal/commit evidence
  for the reported incident. Gateway rollout remains separate and pending.

- [ ] **Measure representative Phone reuse and whole-job allocation after landing.**
  Owner: LayerCache with the Phone CI owner. Phone #298 is merged and its
  source, private publication, fresh consumers, failure/input controls and native
  gates are qualified. Collect representative subsequent PR/main jobs with
  source/profile, exact task hits/misses, direct Team versus runner snapshot
  attribution, setup/transfer/tooling time and allocated runner spans. Current
  8m28s/7m13s unit observations are not a controlled baseline or monthly saving.
  Separate vendor queue/execution from Actions allocation and use actual billing
  evidence before publishing a monthly-dollar comparison.

  The first post-merge main observation at `716c8442`,
  [Check 38718878987](https://github.com/ziahamza/phone-app/actions/runs/38718878987),
  passes all selected gates. Its unit job takes 6m15s, including a 2m02.667s
  Turbo stage with 20 of 27 tasks cached. A prior runner snapshot restores;
  the direct Team report has zero hits among seven eligible tasks. Keep those
  native hits separate from the earlier directly attributed Team qualification.

- [x] **Reuse trusted exact-commit Check results in manual release preparation.**
  Owner: Phone CI/release owner. [Phone #310](https://github.com/ziahamza/phone-app/pull/310)
  merged as `9f38e36db94d1161e5cfd561293cc0a1ba214ca8`.
  [Main Check 38727776394](https://github.com/ziahamza/phone-app/actions/runs/38727776394)
  and the exact-source [signed canary 38730000568](https://github.com/ziahamza/phone-app/actions/runs/38730000568)
  pass. Preparation takes 3m16s versus 28m18s in prior signed run 38629591949;
  a two-second trusted proof lookup removes the 23m18s repeated-validation block.
  RunCloud's signed controller takes 10m17s and Linux IPA inspection 37s.
  All allocated Actions jobs use Ubicloud two-core runners, with zero GitHub Mac
  jobs. The separate matching native-QA reuse run takes 2m12s versus 19m46s fresh.
  Preserve latest-run/current-attempt checks, exact source/context binding,
  fresh fallback and explicit fresh validation. Signing, package inspection,
  publication and live-provider gates remain required. The canary is build-only:
  it skips TestFlight upload and deployed smoke, whose qualification remains
  separate. Provider/cache variation prevents attributing other stage changes
  or comparing whole workflow totals to the earlier full publishing run.

- [ ] **Reduce repeated Phone setup and deterministic tooling checks.**
  Owner: Phone CI owner. The same sample has 4m07s median outside Turbo.
  Break down dependencies, tooling contracts and resources before caching them.
  Reuse complete deterministic contracts and qualified runner/toolchain images;
  validate misses when their inputs change. Compare whole-job time, including
  transfer and setup, rather than presenting task time as total CI savings.

  The 2026-10-15 installed Phone profile confirms 28/28 results in Team Cache.
  The first warm replay restores 25 results from Local in 13.714s. Its three
  analysis misses come from generated native evidence entering broad root
  lint/format/Fallow inputs. On the same `b7279389` source, after generated
  outputs stabilize, the second replay restores all 28 from Local with zero
  executions in an 807ms native stage and confirms 28/28 Team results.
  [Phone #309](https://github.com/ziahamza/phone-app/pull/309) then fixes those
  three inputs at reviewed source `913e03fe`, merged as `716c8442`. It excludes
  only the declared generated output directory. Real Turbo controls preserve
  hashes across generated evidence changes and invalidate for genuine source
  and configuration edits; all 28 local tasks and
  [hosted Check](https://github.com/ziahamza/phone-app/actions/runs/38717692698) pass.
  The package-input fix changes no native or workflow source. Qualify Git-base
  changes separately for cached local Git-dependent analysis; required CI Fallow
  still executes fresh. Setup, tooling contracts and image work remain open.


## P1: LLM and native outputs across machines

- [ ] **Add Gradle's native HTTP task cache for Phone Android.**
  Owner: LayerCache protocol/client integration, coordinated with Phone CI.
  Next implementable adoption after the qualified protocol slice below; the
  Phone owner has completed release-validation reuse. Remote Mac dependency
  integration remains gated on supported provider hooks.
  The October 15 Android acceptance spent 16m11s in the build step inside a
  21m02s compile job. Current Phone source already enables Gradle build caching
  and ccache. The avoidable fraction and transfer overhead remain unmeasured.
  Keep Gradle's native keys, executor and archives. Add scoped GET/PUT over the
  existing bounded artifact engine and an opt-in credential-free init script.
  Qualify Local standalone and direct Personal/Team endpoints with the actual
  pinned Gradle client: trusted producer, empty compatible consumer, exact
  outputs, input/profile misses, failed-task reruns, read-only PRs, revocation,
  outages, cancellation, duplicates and quota pressure. Preserve source/profile
  identity and report native outcomes, transfer bytes and complete command time.
  A Local acknowledgement does not establish private publication. Automatic
  Local-to-private forwarding, Phone workflow adoption, whole Android build
  savings, installation and gateway rollout remain separate acceptance targets.
  Public Gradle publication remains disabled.

  First protocol slice is source-qualified at `3b9ded989140abadedb476cfc29fc100163c9597`.
  The [native receipt](docs/evidence/gradle-native-private-20261015.json) records
  22 actual Gradle 9.3.1/JDK 17 commands in 134.472s, with empty independent
  Local/private consumers restoring exact compiled and verification outputs
  as `FROM-CACHE` with zero actions. It covers native duplicate winners,
  input/compiler/profile misses, failed-task reruns, read-only/revoked clients,
  explicit fresh bypass, init conflicts, quota and service outage behavior.
  [Personal policy controls](docs/evidence/gradle-personal-policy-20261015.json)
  reproduce and fix the missing managed-portal route, then pass actual isolation,
  restart, owner-only CI, PR-reader and former-owner revocation checks, including
  race detection. [Real PostgreSQL/S3 SDK controls](docs/evidence/gradle-cloud-upload-integrity-20261015.json)
  also reproduce and repair incomplete-upload publication, with zero bytes or
  metadata committed in the small malformed-upload cases. The [contract](docs/gradle-cache.md) separates this Java
  protocol proof from Phone Android adoption and measured build savings.
  Next: qualify an isolated Phone-native Gradle producer/fresh consumer, then
  coordinate an installed release and gateway upgrade before workflow adoption.

- [ ] **Qualify dependency reuse on RunCloud through supported provider hooks.**
  Owner: LayerCache cache integration, coordinated with the Phone CI owner.
  Prior signed worker 38629591949 spent 2m13s in pnpm and 6m25s in CocoaPods,
  totaling 8m38s. Its provider log reports dependency storage disabled by operator
  policy. [Phone #310](https://github.com/ziahamza/phone-app/pull/310) records the
  evidence and [published API boundaries](https://docs.run.cloud/ios/build-with-xcode#intentional-differences).
  The published [Xcode SDK](https://docs.run.cloud/sdk/latest#xcodebuildsclient)
  and [CLI](https://docs.run.cloud/cli/latest#xcode-builds) expose no dependency
  cache toggle or restore/save hook. Native-shell rebundling is a separate
  simulator Release path, not a signed-device dependency cache. The prior
  timings are an opportunity, not current or avoidable work. First establish a
  supported dependency-cache setting or restore/save
  hooks on the remote Mac. Do not recreate the removed worker bypass.
  Capture current per-phase timings and provider cache-hit/miss evidence before
  comparing a paired cold/warm build. A LayerCache integration must preserve
  private project authority, complete toolchain/lockfile context, bounded bytes,
  exact restored data, failure/input controls and fresh native/runtime checks.
  Include restore/save transfer and allocated controller time in the result.
  Phone-native Gradle qualification can proceed independently of this gate.

- [ ] **Deliver exact LLM response replay through existing model interfaces.**
  Owner: LayerCache; first slice merged, application adoption pending.
  First target: the AI SDK model
  seam used by Callstack/TesterArmy e2e. Reuse its model interface and LayerCache's
  bounded private artifact storage. Identity includes provider configuration,
  model revision, complete messages/tools/tool results, sampling/options,
  multimodal bytes, execution context, recording generation and sample slot.
  Absolute expiry never renews on a hit. Prove cold capture, zero-call replay,
  a fresh private consumer, changed-input misses, failures/cancellation,
  per-result/storage bounds, and explicit fresh bypass using actual OSS client
  calls and a deterministic provider fixture. Preserve historical usage without
  crediting it as newly billed inference. Never infer model determinism from a
  seed or temperature setting. Streaming and external mutable inputs require
  separately qualified support.

  Evidence on 2026-10-14: [#37](https://github.com/ziahamza/layercache/pull/37)
  merged reviewed head `7df375d9a1b40394496b51038a55a4ea0b77cbac` at
  `453c09487a2268700821417e830d2fb1e3296526`.
  [Hosted source qualification](https://github.com/ziahamza/layercache/actions/runs/38580949339)
  passed at that reviewed head. Runtime source
  `c2fe2f9d2c990bb9918bb6a69ec7b813f9bfc4be` provides
  [`@layercache/native/llm`](native/LLM.md) through pinned AI SDK 7.0.107.
  Actual `generateText`/`ToolLoopAgent` fixtures prove zero-call replay, live tool
  execution and a separately fresh judge. An authenticated disposable Go
  artifact service proves a concurrent committed winner and an empty consumer.
  Local validation passes 72 tests with the installed service and a packed,
  separately typechecked SDK consumer. Independent Standards and Spec reviews
  completed without remaining findings at that runtime source. Strict pinned
  Ajv 8.17.1 and secure-json-parse 4.1.0 validate visible tool names/JSON and
  synchronous normalized structured schemas; later custom validators,
  tool execution and app assertions remain outside this middleware's success
  contract. First-writer consistency lasts only while retained; strict replay
  fails on eviction/expiry, while read-through may record again after eviction.
  A [direct private gateway checkpoint](docs/evidence/llm-private-gateway-20261014.json)
  at that source also proves plain/structured recording, empty-consumer replay
  and changed-input/slot strict misses with two fixture calls and no paid
  inference, configuration change or deployment. Immutable admission rejects
  degraded/pending/full acknowledgements and preserves possibly accepted or
  queued server bytes. First-writer qualification covers the chosen endpoint;
  a Local daemon may retain its unconfirmed/conflicting copy without establishing
  the canonical Team winner. Frozen samples across machines require the direct
  shared private endpoint until Local-to-Team reconciliation is qualified.
  Streaming replay, provider-hidden observations, durable frozen corpora, paid
  provider/live Phone adoption and Local-to-Team winner reconciliation remain
  pending. Next: coordinate the smallest real planner integration with an
  explicit raw judge. Gateway and CLI release qualification remain separate.

- [ ] **Qualify Phone E2E action replay with fresh runtime assertions.**
  Owner: Phone E2E owner with LayerCache integration. Use upstream e2e and
  agent-device rather than another device driver. Record successful actions,
  execute them on the current app, and validate current assertions. Configure
  the judge explicitly when wrapping a planner; upstream defaults can share the
  same model. A saved plan or LLM result alone is not a current passing test.
  Validate changed UI/context, divergent replay, exact assertions and retained
  failure evidence on an actual device target before adoption.

- [ ] **Add a shared HTTP LLM gateway through an OSS gateway cache seam.**
  Owner: LayerCache. Evaluate pinned LiteLLM core and its custom backend/key
  hooks after the first real-client adapter. Preserve provider-native request
  semantics, full identity, private project isolation, cancellation, bounded
  streams and truthful hit/current-cost reporting. Default gateway key
  allowlists do not establish complete context. Require real HTTP client
  cold/restore/miss/failure qualification. Document supported protocols and
  providers rather than claiming universal compatibility.

- [ ] **Separate frozen eval corpora from fresh model benchmarks.**
  Owner: LayerCache/eval integration. Record independent sample slots and model
  revisions; replay exact recorded outputs for regression comparisons. Fresh
  benchmarks bypass reads and writes. Changed evaluator/judge/data/tool context
  invalidates the applicable result. Qualify promptfoo or other existing eval
  clients through their native provider/cache interfaces. Keep current billing
  distinct from historical token usage and estimated avoided calls. Current
  response storage is evictable; qualify retained corpus storage/manifest policy
  before promising durable frozen replay.

- [ ] **Qualify shared Debug development clients for Phone.**
  Owner: Phone native owner with LayerCache client support. The existing Expo
  provider is configured, but trusted CI production and fresh local consumption
  need end-to-end proof. Use the app's pinned Expo fingerprint and actual
  Xcode/SDK/architecture or Android toolchain identity. Verify install/launch
  and current Metro JS. Debug clients and bundled Release QA apps have distinct
  identities; physical iOS devices and EAS Build retain their own contracts.

- [ ] **Measure residual iOS work before extending shared LLVM transport.**
  Owner: Phone native owner. Two recent RunCloud samples already hit all 1,725
  cacheable compiler tasks but occupied Macs for 16m44s/18m28s. Time setup,
  packaging, runtime acceptance and receipt misses separately. Qualify beta.4
  graph retention on Phone's exact toolchain/scale before expanded use. Swift
  replay and signed-build cost remain separate evidence targets.

## P1: complete supported private protocols

- [ ] **Complete Nx Local-to-private reuse and confirmed durable publication.**
  Owner: LayerCache. Migrated from
  [issue #33](https://github.com/ziahamza/layercache/issues/33).
  Beta.4 provides Local and direct private endpoints; the bridge remains absent.
  Reuse native Nx hashing/packing and existing bounded storage, capabilities,
  durable queues and conflict/pin lifecycle. Read through and publish only to an
  explicitly configured Personal/Team destination, preserving project/profile
  identity. Recover after outages/restarts, release verified permanent-conflict
  pins, and distinguish uploads from already-present winners. Confirm exact
  requested native hashes at the destination before reporting CI availability.
  Extend the compiled-client fixture through an empty consumer with zero task
  executions and exact outputs/logs, changed-input/profile misses, failed-test
  reruns, interruption, quota pressure, owner/Team revocation and authorized CI.
  Report transfer overhead and direct attribution. Nx's separate filesystem
  cache remains outside LayerCache's byte budget until its lifecycle is bounded.
  Public Nx stays disabled until separately qualified.

## P2: broaden after measured value

- [ ] **Qualify an isolated pinned bazel-remote sidecar, then sccache.**
  Owner: LayerCache. Next: select a real personal workload and pin the engine.
  Preserve per-project/profile isolation;
  an instance-name label alone is not an access boundary. Reuse permissively
  licensed engines and native clients. Follow [the OSS plan](docs/oss-integration-plan.md).
- [ ] **Qualify the existing operator gateway upgrade.**
  Owner: LayerCache/operator. Next: build an exact-source candidate and test a
  disposable recovery copy before changing the gateway. Preserve current
  project configs/backends/routes and unrelated deployment edits. Build an
  exact-source image, test an isolated recovery copy and authenticated restores,
  then update only the gateway. Record rollback compatibility for issued tokens.
- [ ] **Deploy limited self-serve only after live inputs are configured.**
  Owner: LayerCache/operator. Next: configure the approved cohort and real OAuth.
  Real OAuth, numeric cohort, isolated bounded storage and two-user upload,
  fresh restore, removal and restart acceptance remain launch gates.
- [ ] **Make private CI onboarding repeatable.**
  Owner: LayerCache, with application workflow owners. Next: qualify one pinned
  example through a trusted producer and an empty compatible consumer.
  Provide copyable setup pinned to a verified release and execution profile.
  Prove publication and an empty
  consumer restore, preserve task execution on misses, and report source,
  bytes, hit status, transfer overhead and whole-job time in the dashboard.
  Resolve repository renames against the stored immutable numeric GitHub
  repository identity. Qualify Parle's native Safari reuse separately after
  Phone's cache path is working.
- [ ] **Improve measured cache admission and reporting.**
  Owner: LayerCache. Next: collect native traces with physical storage and
  whole-job timings. Preserve hard budgets and correct graph/read leases.
  Collect representative native traces
  before changing LRU. Count physical staging/deletion/metadata and report
  avoided work after lookup/transfer/verification overhead.
- [ ] **Revisit machine management, hosted runners, Windows, Actions v2,
  a wider adapter SDK and Public Builds.**
  Owner: LayerCache. Next: reassess scope after useful private reuse is measured.
  Promote only after useful cross-machine reuse and costs are established.
  Public population stays confined to LayerCache-controlled builds.

## Delivered baseline

- [x] Source-qualified rejection of failed streams at the shared S3 boundary,
  source `3d5e2df`. Real SDK and PostgreSQL controls cover multipart cleanup and
  publication fences. [Contract](docs/retention.md#upload-completion) and
  [evidence](docs/evidence/cloud-stream-integrity-20261015.json). Gateway rollout
  remains open above.
- [x] Source-qualified native Gradle HTTP over existing bounded artifact storage,
  expiring process credentials and an opt-in init script, source `3b9ded9`.
  The [22-command real-client qualification](docs/evidence/gradle-native-private-20261015.json)
  and [managed Personal policy proof](docs/evidence/gradle-personal-policy-20261015.json)
  pass. The Linux native-client CI gate uses the pinned Gradle distribution and
  candidate binary. Installation, gateway rollout, Local forwarding and Phone
  whole-build savings remain open above.
- [x] Phone's ten independent mobile test groups, all thirty isolated workflow
  processes and mandatory typechecking, reviewed source `b7279389` merged in
  [Phone #298](https://github.com/ziahamza/phone-app/pull/298) as
  `3bae98fefadda18a21a5353a726a4567f00084fb`. The existing Check command and
  two-task limit remain. [Qualification](docs/phone-test-reuse.md) includes real
  local publication/fresh consumption, exact input/failure controls, current
  hosted Check and passing actual Android Contacts/iOS startup-CallKit acceptance.
  The simulator has one entitlement producer and unchanged strict verification.
  The installed persistent Local profile confirms all 28 results in Team Cache;
  its stable full native stage restores all 28 from Local in 807ms with zero
  execution. This does not establish full signed-in iOS Contacts coverage,
  physical calling, monthly savings or a self-serve cloud deployment.
- [x] Reviewed beta.4 CLI, immutable publication and installed Turbo/Nx proof
  on Linux x64/arm64 and macOS arm64, source
  `ca3d5889a52815bce9565d3825b081839ac7077b`.
- [x] Owner-only Personal projects, native Nx Local/direct private endpoints,
  permanent Turbo conflict pin retirement and complete native Xcode graph
  retention under quota pressure, merged in #27–#31.
- [x] Five Local services upgraded with preserved data/profiles/budgets and
  authenticated health. This does not establish a gateway/cloud deployment.

New evidence belongs beside its acceptance target. Move completed items here
with exact sources and tests; keep unresolved qualifications unchecked.
