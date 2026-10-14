# LayerCache TODOs

Last strategic review: 2026-10-14. This file owns outstanding work. Checkboxes
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
  [Phone #295](https://github.com/ziahamza/phone-app/pull/295) owns current signed
  build and retained web/APK/receipt qualification;
  [Phone #273](https://github.com/ziahamza/phone-app/pull/273) remains the
  historical held-stack reference. Prove an exact producer-to-consumer
  transfer and consumer-only rerun with source/run/attempt/job/digest checks.
  Required publication fails visibly. The separate fourteen-day release-package
  policy does not establish this handoff. Preserve the held stack's unrelated
  acceptance requirements.

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

  Evidence on 2026-10-14: candidate
  `c5a354badfab5d74bdcc2458141f277d382c61ec` adds the
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

- [ ] **Make expensive Phone test groups independently reusable.**
  Owner: Phone workflow owner for task definitions; LayerCache for publication
  and fresh-consumer proof. Five sampled PRs had median unit time 16m20s,
  including 11m40s in Turbo, at the October 14 research checkpoint.
  Separate ordinary mobile unit tests and the existing workflow suites/variants
  through Turbo/Vitest. Preserve SDK process isolation and complete transitive
  source, fixture, configuration and environment inputs. Keep full typechecking
  required while establishing accurate subgroup cache identities. Prove that
  an unrelated edit reuses unaffected groups, shared-input changes rerun affected
  groups, failed tests remain failures, and compatible local results restore on
  an empty CI consumer. Attribute remote hits directly; native snapshot hits
  alone are insufficient. Measure allocated runner time and whole jobs.

- [ ] **Reduce repeated Phone setup and deterministic tooling checks.**
  Owner: Phone CI owner. The same sample has 4m07s median outside Turbo.
  Break down dependencies, tooling contracts and resources before caching them.
  Reuse complete deterministic contracts and qualified runner/toolchain images;
  validate misses when their inputs change. Compare whole-job time, including
  transfer and setup, rather than presenting task time as total CI savings.

## P1: LLM and native outputs across machines

- [ ] **Deliver exact LLM response replay through existing model interfaces.**
  Owner: LayerCache; implementation in progress. First target: the AI SDK model
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

  Evidence on 2026-10-14: first-slice candidate
  `340ac52844499730f3fecec42003655af306d61a` provides
  [`@layercache/native/llm`](native/LLM.md) through pinned AI SDK 7.0.107.
  Actual `generateText`/`ToolLoopAgent` fixtures prove zero-call replay, live tool
  execution and a separately fresh judge. An authenticated disposable Go
  artifact service proves a concurrent committed winner and an empty consumer.
  Local validation passes 70 tests with the installed service and a packed,
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
  inference, configuration change or deployment. The Local-daemon immutable
  publication fix adds degraded/pending/full acknowledgement regressions and
  preserves possibly accepted or queued server bytes; its new exact-source
  review and live rerun are pending.
  Streaming replay, provider-hidden observations, durable frozen corpora, paid
  provider/live Phone adoption and hosted candidate acceptance remain pending.
  Next: finish hosted candidate qualification, then coordinate the smallest real
  planner integration with an explicit raw judge.

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

- [ ] **Add Gradle's native HTTP task cache for Phone Android.**
  Owner: LayerCache protocol/client integration, coordinated with Phone CI.
  Keep Gradle's keys and executor and retain ccache for C++. Implement bounded
  scoped GET/PUT and its native authentication/error semantics. Prove a trusted
  producer, empty compatible local/CI consumers, exact outputs, toolchain/input
  misses, read-only PRs, revocation, outages and quota pressure. Measure actual
  whole builds before broadening rollout.

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
