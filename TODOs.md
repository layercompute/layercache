# LayerCache TODOs

Last strategic review: 2026-10-20. This file owns outstanding work. Checkboxes
mean that every acceptance target is satisfied, not that a prototype exists.
[Launch status](docs/launch-plan.md) records delivered/deployed qualification;
[roadmap review](docs/roadmap-review.md) explains how to reprioritize.

The next milestone is hosted private cache sharing with R2-backed persistent
storage, alongside reliable Phone handoffs and fewer repeated executions or
copies across local machines and CI. Build a general compute and storage product
through established build/test/model clients. Prioritize expensive immutable
outputs, bounded native stores and run-owned generated data. Measure net benefit
before adding another integration. Transparent sharing of arbitrary mutable
workspaces and filesystem migration are outside the current product scope.

## P0: reliable compute and storage reuse

- [ ] **Show where LayerCache runs and the resources available there.**
  Owner: LayerCache observability. October 20 user outcome: recognize the same
  persistent client installation across projects and worktrees, distinguish
  GitHub Actions jobs and declared providers such as Ubicloud, and inspect
  effective CPU, memory and filesystem capacity, used, available and reserved
  space. Keep verified execution authority separate from client-reported
  hardware facts. A client installation is not proof of a unique physical host;
  CI jobs must not be collapsed using runner names or network addresses.
  Add optional bounded observations through existing authenticated clients,
  with backward-compatible feature discovery, live project authorization,
  restart-safe bounded inventory and a shared local/hosted dashboard view.
  Never upload hostnames, hardware identifiers, raw paths or environment maps.
  Validate cross-project deduplication, independent CI jobs, unknown limits,
  provider evidence, stale samples, membership removal and collection failure
  without changing artifact keys or failing the underlying build. Record source
  qualification separately from installed clients and deployed inventory.
  The first [source qualification](docs/evidence/execution-observability-20261020.md)
  now covers authenticated inventory, shared presentation, bounded collection,
  persistence and standalone operation. [PR #73](https://github.com/ziahamza/layercache/pull/73)
  and the runtime corrections in
  [PR #74](https://github.com/ziahamza/layercache/pull/74) are merged. The final
  reviewed source is deployed and installed; five authenticated no-op clients
  produce one shared installation in the hosted dashboard. All 132 live browser
  checks pass. The
  [rollout receipt](docs/evidence/execution-rollout-20261020.md) separates that
  location/resource evidence from cache reuse. Normal application CI observation
  adoption remains pending for older immutable action pins; no extra native jobs
  were dispatched to populate inventory.

- [ ] **Share one bounded local artifact cache across worktrees and projects.**
  Owner: LayerCache storage/native clients. The latest user outcome adds local
  hot-byte reuse to hosted R2 storage: fetch a compatible immutable artifact once,
  reuse it across workspaces, and avoid multiplying independent disk budgets.
  Reuse the existing content-addressed store, verified read-through, LRU and
  reader protection. Start with the complete native Turbo path, then connect
  Gradle through the same owner and budget. Preserve origin, project, profile,
  integration and visibility authority before resolving a reference. Cloud-only
  operation remains selectable. Qualify concurrent single-fetch reuse, equal
  payload deduplication, corruption, cancellation, restart and quota pressure.
  Account for committed blobs, metadata, staging and bytes held by readers;
  the current committed-blob cap is not a ceiling on total disk allocation.
  Track caller-owned extracted outputs separately. Prefer a leased immutable
  path for read-only consumers; use isolated copies or verified native CoW
  for writable consumers. Never hardlink arbitrary writable outputs. Measure
  complete command time and total allocated bytes before rollout, preserving
  existing package-manager deduplication and the machine's free-space reserve.

- [ ] **Qualify portable reuse without weakening native compatibility.**
  Owner: LayerCache native integrations. Architecture, target ABI, OS and SDK
  belong in the logical identity when they can affect an output. Apply this to
  every proposed key dimension: qualify invalidation for relevant changes and
  unchanged identity for irrelevant changes. Machine identity, runner size and
  observed free space must not fragment compatible task caches. Execution-host
  architecture and compilation target are different facts. Keep native systems'
  complete declared inputs; never strip architecture from an opaque key or
  infer portability from a file extension. Current measured runtime profiles
  deliberately separate platforms. Introduce narrower sharing only for an
  explicitly declared and qualified task/output contract, including its
  dependency closure. Prove equivalent outputs across supported architectures
  and misses for changed target, ABI, SDK, toolchain and relevant inputs.
  Keep logical compatibility independent of content-addressed byte identity:
  different authorized references with identical payloads should share one blob
  within the supported storage scope. Record project isolation and current
  cross-project deduplication limits rather than claiming a global blob pool.

- [ ] **Explain conflicting immutable Turbo publications before claiming full adoption.**
  Owner: LayerCache native integrations with the producing application's owner.
  Normal hosted browser work recorded two HTTP 409 publications under one
  source/profile. The server correctly preserves the existing immutable bytes.
  Task identities and semantic output equivalence are not yet established.
  Native Turbo already normalizes archive ownership and timestamps; do not
  attribute these conflicts to timestamps without source evidence. Identify
  the producing tasks and differing output/log bytes from existing evidence,
  then qualify deterministic output or a justified native-client contract.
  Do not replace the winner, weaken integrity checks or add duplicate builds
  merely to hide warnings. Cache failures remain distinct from task failures.

- [x] **Qualify the live customer dashboard and fix verified reporting gaps.**
  Owner: LayerCache dashboard/integrations. The actual operator account completed
  its normal initial password change, with the current credential kept outside
  Git. [PR #70](https://github.com/ziahamza/layercache/pull/70) merged as
  `7b221c181b89100fa26b731e8eb32eeead289d21` after all eighteen PR checks passed.
  Its reviewed source `4ddb1387e16897767645fd2458133323d4bc7494` is deployed and
  installed as the CLI; the merged tree is identical. All main checks pass.
  The fixes separate raw/projected Turbo summary bounds, accept omitted transit
  tasks without invented critical-path savings, preserve actual reporting
  periods, correct future Team Actions source attribution and remove unsupported
  per-source upload zeros. Post-deployment Chromium, Firefox and WebKit each
  passed 44 live scenarios, including 45 UI/API metric projections in total.
  Forty-five existing native artifact reads and three signed Actions archive
  reads passed across current and compatibility origins. All five installed
  clients passed doctor and scoped authenticated-read controls without creating
  Local artifact stores. Password/session/project files and other containers
  were preserved; no live pruning or membership change occurred. The
  [bounded audit receipt](docs/evidence/dashboard-integration-audit-20261020.md)
  keeps source, live qualification and application adoption distinct.

- [ ] **Finish local and CI client adoption across the operator cohort.**
  Owner: LayerCache integrations with application CI owners. An explicit host
  launcher now validates installed pinned tools, derives runtime profiles through
  the same algorithm as the prepared CI clients and uses temporary protected
  configuration overlays without changing saved configurations. Five plans and
  doctors pass; that does not establish task reuse. Four application adoption
  PRs are merged with immutable action pins, canonical endpoints and eligible
  read-only PR consumers. Three clients recorded normal Team restores. The
  fourth passed its cold read-only PR with zero eligible Team hits, then its
  normal trusted main workflow recorded 4 Team hits among 47 eligible task
  outcomes. Those hits can come from other jobs in that same workflow; a later
  independent reader remains unqualified. An existing consumer following main
  already submitted a
  correct normal OIDC export report after the source update, without dispatch.
  The hosted CLI report projection from
  [PR #72](https://github.com/ziahamza/layercache/pull/72) is now deployed and
  installed in the final PR #74 rollout. Unused input details, environment
  metadata and commands stay on the machine. Raw/wire bounds, malformed-input
  refusal before HTTP and unchanged original summaries for native publication
  passed source controls.
  The Expo provider needs a native credential/cloud-only storage bridge; Go
  `remoteOnly` does not control that client's local archives or extractions.
  Existing compiler/provider snapshots remain separate. Native metering and
  invoice reconciliation remain open below. A configured endpoint alone
  establishes neither cache reuse nor cost savings.

- [x] **Move the existing operator cohort to hosted R2 and the cloud dashboard.**
  Owner: LayerCache cloud/operator. The October 20 user outcome is one hosted
  customer dashboard for every currently configured project, remote artifact
  storage, and ordinary local commands that work without a Local daemon.
  Preserve native keys, project/signing identities, current membership and CI
  repository/ref authority. Add a bounded operator adoption interface rather
  than inserting portal records manually. Support the user's selected
  operator-provisioned email/password sign-in for the private rollout, bound
  to a verified existing project owner. Reuse portal sessions and standard
  password hashing; keep native repository/CI authentication on GitHub.
  OAuth and self-serve enrollment remain separate options and gates.
  Provide opt-in cloud-only CLI operation that obtains scoped credentials from
  the configured backend and never constructs a Local artifact store. Preserve
  standalone defaults. Qualify actual remote restores and changed-input misses,
  authentication/revocation, expiry and exact repository scope, reporting limits
  and failure fallback. Migrate SQL metadata with verified complete R2 objects;
  freeze writes and recheck retained obligations before cutover. Keep compatible
  existing endpoints while clients move.
  [PR #68](https://github.com/ziahamza/layercache/pull/68) merged with all
  seventeen checks green. The exact
  [deployed backend](https://github.com/ziahamza/layercache/commit/95c418b92e2f41076d97c975972b71bc706cc206)
  and [installed CLI](https://github.com/ziahamza/layercache/commit/3da910fc6d9486517230b63bd96e63db81dec2dc)
  are recorded with the [sanitized rollout results](docs/evidence/operator-cloud-migration-20261020.md).
  The October 20 operator rollout now provides
  authenticated hosted project views, R2-backed artifacts and cloud-only
  clients while preserving existing project identities and native authority.
  All five cohort projects authenticated through real GitHub CLI credentials;
  ten legacy artifact reads passed in total, followed by restart checks.
  A synthetic publication and an operator-signed read-only fixture in a new
  HTTP session passed reader and scope-denial controls. These live checks do
  not establish a second user identity, application OIDC adoption or a live
  membership-revocation mutation. Separate PostgreSQL source tests cover
  revocation across adoption and restart. The physical recovery copy uses
  the matching PostgreSQL major version. TCP password authentication passed
  and the server rejected incorrect credentials. The
  [migration guide](docs/cloud-migration.md) records the reusable checks.
  This completes the bounded operator cohort, not public signup or paid plans.

- [x] **Retire verified legacy cache storage after recovery and dependency checks.**
  Owner: LayerCache cloud/operator. October 20 retirement followed a full R2
  recovery download, authenticated decryption and complete archive/hash
  verification. The operator removed obsolete containers, normally unmounted
  the exact old filesystem and detached its backing device. Every active
  mount namespace was clear before the exact sparse backing file was removed.
  Source, credentials, shared stores and evidence remain protected. Physical
  allocation was recorded separately from logical/sparse capacity. The
  [migration guide](docs/cloud-migration.md) keeps these retirement gates
  explicit for future moves. The
  [deployed source](https://github.com/ziahamza/layercache/commit/95c418b92e2f41076d97c975972b71bc706cc206)
  and [retirement results](docs/evidence/operator-cloud-migration-20261020.md#recovery-and-retirement)
  record the completed scope and its recovery limits.

- [ ] **Move API and SQL hosting off this machine and qualify ongoing recovery.**
  Owner: LayerCache cloud/operator. R2 stores artifact bytes; API, PostgreSQL
  and bounded verification staging still run on the current operator host.
  Provisioning remote compute, independently recoverable encryption keys and
  current off-host SQL backups remain separate work. Qualify restoration and
  authorization on the replacement host before retiring its local counterpart.
  The verified legacy archive does not back up later writes to the new service.

- [x] **Deliver the first customer dashboard locally and on the web.**
  Owner: LayerCache dashboard. User outcome: inspect effective cache reuse,
  source attribution, transfers, storage, maintenance and account costs through
  the CLI without a cloud account, or through the authenticated hosted portal.
  Reuse the existing dashboard, measurement repositories and administrative GC.
  Separate Local placement from Personal/Team/Public access, and preserve
  trusted Public publication. Share rendering between the two authentication
  modes. Support an optional CLI-owned backend and an explicitly configured
  remote backend. Require confirmed administrative pruning, current membership,
  retained/read-lease protection and refreshed state after uncertain mutations.
  Costs must distinguish recorded operator input, estimates, incomplete coverage
  and unavailable invoices. Do not infer total spend from current stored bytes.
  Acceptance and outstanding native metering/billing limits are recorded in the
  [dashboard specification](docs/customer-dashboard-spec.md).
  [Reviewed implementation](https://github.com/ziahamza/layercache/commit/3e13358f72845d4b9d9cce3038ed71371b3c8a59)
  passes affected Go suites and race checks, type checking, builds, vet, lint
  and 222 isolated browser checks across Chromium, Firefox and WebKit.
  [Qualification](docs/evidence/customer-dashboard-20261020.md) records actual
  pruning, read-only denial, lifecycle and exact-currency controls. These are
  the original source checks. The October 20 operator cohort now also uses
  the hosted portal. An immutable CLI release, full native usage and provider
  billing remain open below.
  [PR #67](https://github.com/ziahamza/layercache/pull/67) tracks hosted source
  checks. Initial remote authentication now precedes owned-backend construction,
  with restored and published native bytes tested from an empty capability.

- [ ] **Launch hosted private caches with R2-backed storage pools.**
  Owner: LayerCache cloud/storage. The October 19 user priority is a usable
  hosted service so teams can accumulate compatible results and restore them
  quickly across local machines and CI. Reuse the existing PostgreSQL metadata,
  S3-compatible byte store, project gateway and access model. Default managed
  storage to Cloudflare R2; keep storage placement separate from Personal/Team
  visibility and trusted-publisher Public access. Bind projects explicitly to
  persistent storage pools and enforce plan capacity/admission independently
  of Local Cache size. Separate operator-provisioned beta access from public
  signup, paid entitlement/billing and launched self-serve claims.
  Named pools and operator-assigned capacity plans now persist backend identity
  and project allocations. SQLite/PostgreSQL controls cover atomic admission,
  restart, credential rotation and managed quota enforcement. Two private R2
  buckets and separate bucket-scoped credentials are provisioned outside Git;
  [Actual R2 transport](docs/evidence/r2-private-source-20261019.md) now passes
  scoped authority, exact bytes, restart, quota, retained publication and expiry.
  [Operator hosting](docs/evidence/r2-operator-hosting-20261019.md) is live with
  two private R2-backed projects, real GitHub human authentication, exact reads
  after API/SQL restarts and trusted public TLS. Existing services are preserved.
  [External native Turbo acceptance](docs/evidence/r2-native-oidc-20261019.md)
  now uses real GitHub OIDC and separate fresh hosted runners: default-ref
  publication, non-default-ref read-only restoration, matching native keys and
  full output hashes, zero executions or artifact uploads during baseline
  restores, and keyed invalidation/failed-test controls. It is a small synthetic fixture, not
  application or economic acceptance. The operator-run
  [fixture](qa/r2-native-oidc/README.md) preserves service permissions and reports
  denied optional reader telemetry separately from artifact failures.
  The October 20 operator rollout adds existing-workload namespace migration,
  cloud-only clients and an authenticated hosted dashboard. Next: supported
  ongoing recovery, remote compute hosting, OAuth signup and paid-plan
  activation. The API and SQL still run on the current operator host.
  Operator hosting does not activate
  managed billing or the portal's plan lifecycle.
  See [hosted storage](docs/hosted-storage.md). Prepare and review the complete
  runtime candidate before changing operator services.
  Acceptance: authenticated producer and independent read-only consumer, exact
  native bytes after gateway restart, project/owner isolation and revocation,
  enforced pool/project quotas, supported retention/eviction, upgrade/rollback,
  and real request/metadata/byte-transfer timings. Qualify direct transfers or
  nearby read-through only through existing integrity and authority contracts.
  Unknown publication must reconcile without duplicate work. GitHub artifacts
  remain required until application retained-handoff acceptance passes.

- [x] **Release the merged CLI capabilities as beta.7.**
  Owner: LayerCache release. The immutable beta.5 binary predates native Bazel,
  storage inspection and hosted storage plans. Qualify the current candidate
  and downloaded binary with the existing real Bazel client on Linux amd64;
  keep the ordinary acceptance fixture separate from the large pressure test.
  Exercise bounded installed `storage inspect` fixtures on Linux amd64/arm64
  and macOS arm64, including preserved source bytes, allocation accounting and
  a visibly incomplete scan with nonzero exit. Require the exact release
  source's hosted-pool PostgreSQL check, reusing its existing dedicated fixture.
  Qualify installed dashboard backend ownership, remote selection, read-only
  defaults and confirmed administrative maintenance with isolated fixtures.
  Publish through the immutable attested pipeline, then qualify fresh installers
  before changing default installation or Local daemon pins. Server deployment, Swift
  replay, self-serve signup and required retained handoffs have separate gates.
  The beta.6 tag remains at `86d2383d` without a published release after its
  macOS worker-test environment assertion failed. Node's
  [Darwin test](https://github.com/nodejs/node/blob/3e5c8afba6dcff91d46b8204276d62e3843b23b6/test/embedding/test-embedding-permission-env.js#L28-L30)
  accounts for `__CF_USER_TEXT_ENCODING` despite a minimal supplied environment,
  so [PR #77](https://github.com/ziahamza/layercache/pull/77) corrected that test
  while preserving production isolation. All 15 required checks passed,
  including all 14 native macOS worker tests and the full macOS Go suite.
  Immutable [beta.7](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.7)
  is published at source `104b3fb7483986dcad485ceb30e9c8b37973b286`;
  its [release qualification](https://github.com/ziahamza/layercache/actions/runs/39474876385)
  and all post-merge checks passed. Keep beta.6 unchanged. The published
  installer workflow binds provenance, platform metadata and 24 lifecycle/archive
  controls to the downloaded binary on three fresh platforms. These checks must
  pass before merging the default pin refresh or updating a Local daemon.

- [x] **Expose an honest local storage inventory.**
  Owner: LayerCache. User outcome: identify the owners of disk growth before
  collecting another cache or deleting useful work. Add account-free
  `storage inspect` for explicit paths, with allocated and apparent bytes,
  inode-aware hardlink accounting, sparse files and filesystem headroom.
  Never initialize or repair a cache, follow symlinks, traverse nested mounts,
  or label path categories as deletion eligibility. Bound error details and
  report incomplete live scans. Qualify overlap, links, cancellation and platform
  limits through the CLI. This first slice measures storage; it neither deduplicates
  existing files nor establishes reclaimable bytes.
  [Reviewed source](https://github.com/ziahamza/layercache/commit/b1045376e7469f10ab5811a753995fe746061e8b) passes
  complete CLI/module tests, focused race tests, vet and local lint; independent
  Standards/Spec reviews have zero findings. The
  [built CLI fixture](docs/evidence/storage-inspect-20261017.json) confirms
  cross-root hardlink and overlap accounting, sparse allocation, preserved bytes
  and metadata, unvisited symlinks and partial JSON with nonzero exit. A live
  explicit-root scan matches an independent native `du` allocation total. Darwin
  arm64 and Windows amd64 module compilation passed; target tests were not
  executed. Mounted-file boundaries and root-relative categories have scanner
  regression tests and independent follow-up review. Hosted checks and landing
  are tracked by [#58](https://github.com/ziahamza/layercache/pull/58); this
  records source qualification, not a released CLI or reclaimed storage.

- [ ] **Prevent repeated local outputs and abandoned build-slot growth.**
  Owner: LayerCache, coordinated with application execution owners. User outcome:
  materially less compute and allocated storage across short-lived workspaces.
  Rank immutable final outputs and native package/task caches before writable
  workspace sharing. Use complete native identities and one bounded managed
  copy where existing clients support it. New managed materializations require
  explicit ownership, active-reader leases, admission before publication, and
  restart-safe release/GC; count packed bytes, expanded bytes and concurrent
  deltas together. Preserve required handoffs and source/evidence retention.
  Accept one representative workload only after measuring total allocated bytes
  and complete command time, changed-input isolation, concurrent readers,
  cancellation, quota pressure and restart cleanup. The
  [storage decision](docs/research/local-workspace-storage-20261017.md) records
  why native clone support is optional and rootless overlays remain a separate
  experiment. Do not rewrite active worktrees, hardlink mutable outputs, or
  create a generic filesystem layer for this milestone.

- [x] **Qualify the first hosted native Gradle producer and full reader.**
  Owner: Phone CI, with LayerCache protocol/client support. [Phone #317](https://github.com/ziahamza/phone-app/pull/317)
  merged as `0fd189000d1166a91ca39a2a94f3de6e1e2c8cc9`; main Check is green.
  The [hosted qualification](docs/evidence/gradle-android-hosted-20261016.md)
  records the exact-source trusted main writer and independent read-only full
  reader, matching profile and 297 matching restored task paths/native keys.
  App filesystem task caching is disabled. APK inspection, Kotlin tests and
  all eight fresh Contacts emulator steps pass, with no measured OOM events.
  The narrow main writer includes no C++/JNI/signing/APK tasks, including its
  separate Kotlin test graph. PR/manual/`workflow_run` builds remain readers.
  This completes the first hosted correctness gate, not economic acceptance.

- [ ] **Measure net benefit of hosted Gradle adoption.**
  Owner: Phone CI/release owner. The final warm reader restores 297 matching
  native tasks in a 644s complete two-vCPU compile job, versus the earlier 864s
  warm baseline. Both have 94/94 compiler hits. Another warm consumer takes
  834s and a normal PR restores 296 tasks. Source, VM and network differences
  prevent causal or monthly savings claims. Warm publisher jobs cost 309s and
  316s separately; the first cold publisher costs 797s. Complete workflow latency
  includes queue time and has not shown the same stage improvement. Retain
  fresh APK inspection, Kotlin tests and device assertions. Signed/ARM and
  observed PUT/GET byte costs remain separate acceptance targets.

- [x] **Land retained private storage and the direct action.**
  Owner: LayerCache storage/protocol/qualification.
  Required retrieval plus verification of a reported 124,078,900-byte artifact
  takes 229s in the first reader and 87s in the warm reader. These are step
  durations, not pure transfer bandwidth. [#53](https://github.com/ziahamza/layercache/pull/53)
  merged at `b0d39011fa703944b4f7cd9c7936f067db1347ed` after all fourteen PR
  checks, independent Standards/Spec review and external review passed.
  Immutable action source `29489e8e9b132df4cf2f9625ea49cb7c3150e858` has the
  same tree. The [contract](docs/retained-artifacts.md) and
  [source evidence](docs/evidence/retained-source-20261017.json) cover reserved
  admission, fixed TTL, immutable receipts, deletion fencing, durable commit,
  lost-acknowledgement reconciliation and full-file integrity. PostgreSQL/S3
  race tests and signed OIDC eligibility fixtures remain distinct from the
  locally authorized large-file fixture. The direct action streams one opaque
  file and records consumed bytes and overlapping phase timings. Full main
  CI, PostgreSQL and local-to-CI workflows pass at the merged source. This
  implementation does not establish issue #34's historical miss cause.

- [x] **Qualify the retained service deployment.**
  Owner: LayerCache operations. The [gateway receipt](docs/evidence/retained-gateway-20261017.json)
  records a fresh full cold backup, 191 isolated checks over five phases and
  exact legacy bytes through upgrade/restart/rollback/forward. Retained 4 KiB
  and 124,078,900-byte files pass after candidate restart and forward. The
  old-binary rollback begins with zero reservations or staging uploads and
  preserves published receipts and claims. Forward upgrade restores full reads.
  Live HTTPS publication and full-file reads pass before and
  after a gateway-only restart. Only the gateway image pin and Phone feature
  flag change; quotas, physical reserve, signing authority, other projects and
  the beta.5 CLI installation remain unchanged. The [interface](docs/retained-artifacts.md)
  records the exact source/action/image pins. A live 25-second fixed-TTL canary
  rejects read-only publication, foreign-run/other-job access and over-quota
  admission, then expires and releases its claim, pin and reservation. These
  operator-minted capabilities qualify service behavior, not real GitHub OIDC
  or application adoption.

  **GitHub artifacts remain the required-transfer path.** Fresh actual Phone
  workflow OIDC and required-consumer acceptance remain outstanding. Phone owns
  those workflow changes and dispatches, including a consumer-only rerun within
  the retained deadline.
  Optional exact-input APK reuse is independently evictable and rebuilds on a
  miss.

- [ ] **Make local Gradle results available to authorized private CI.**
  Owner: LayerCache. The direct hosted native path is now qualified; automatic
  Local-to-Personal/Team forwarding remains outstanding. Reuse Gradle's native
  keys/archives and the existing bounded artifact store, durable upload queue
  and remote confirmation patterns. Preserve complete selected-model context,
  Personal ownership, Team membership and read-only consumers. A Local admission
  or successful duplicate PUT alone does not prove the exact requested remote
  archive or retained handoff. Qualify read-through, durable publication,
  canonical-winner confirmation, restart repair, revocation, incompatible
  profiles, failed tasks, outages, cancellation, duplicates and quota pressure.
  Report actual native outcomes, bytes and full command/job time. Public Gradle
  publication remains disabled; this task does not supply retained artifacts.

  Protocol source is qualified at `3b9ded989140abadedb476cfc29fc100163c9597`;
  [native](docs/evidence/gradle-native-private-20261015.json),
  [Personal](docs/evidence/gradle-personal-policy-20261015.json) and
  [PostgreSQL/S3 integrity](docs/evidence/gradle-cloud-upload-integrity-20261015.json)
  controls remain separate from the [local Android pair](docs/evidence/gradle-android-private-20261016.json)
  and hosted application proof. The [immutable beta.5 release](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.5),
  [published installation](docs/evidence/beta5-installed-20261016.json) and
  [existing gateway/five-daemon rollout](docs/evidence/gateway-beta5-rollout-20261016.json)
  are complete at reviewed source `53ec27443a406a508b7e8faeab3f46292e0ba07b`.
  Existing `workflow_run` jobs remain readers. The [conditional producer-policy
  alternative](docs/research/trusted-ci-producer-20261016.md) is not required for
  the landed actual main-push writer. Phone retains workflow/release routing;
  remote Mac dependency hooks and full signed-release publication remain separate.

- [x] **Publish reusable CI optimization lessons.**
  Owner: LayerCache documentation. [#46](https://github.com/ziahamza/layercache/pull/46)
  merged as `a1d944efc22f34fd9b291f9ab92cc0272f98d9f7` after all fourteen
  hosted PR jobs, the external review and independent Standards/Spec reviews
  passed. The eight [public recipes](docs/ci-optimization/README.md) cover
  workflow selection, task reuse, native builds, handoffs, trusted release
  validation, inference, storage and measurement. Examples omit application
  names, private repository/run links, source identities, hosts and credentials.
  Measured examples retain their coverage and timing limits. The upstream
  recipe includes native GitHub parallel steps with dependency joins, bounded
  workers and parser/runner compatibility.
  Full main [CI](https://github.com/ziahamza/layercache/actions/runs/38822434257),
  [PostgreSQL](https://github.com/ziahamza/layercache/actions/runs/38822434141)
  and [local-to-CI](https://github.com/ziahamza/layercache/actions/runs/38822434236)
  validation passed all fourteen jobs at that merged source.

- [x] **Make CI optimization tips discoverable and usable by agents.**
  Owner: LayerCache documentation. Saved the requested
  [superseded-run and draft tip](https://x.com/JinjingLiang/status/2111211135542362334)
  with primary-source checks, an agent discovery pointer and an ordered
  [playbook](docs/ci-optimization/agent-playbook.md), checked on 2026-10-17.
  Guidance covers stable concurrency groups, draft transitions and required-check
  semantics, plus focused edit loops, honest failure logs, compression and
  retained-input reruns. Local lint, 81 relative links/anchors and the combined
  workflow fragments pass; independent Standards/Spec review checks the guidance.
  [Reviewed recipe source](https://github.com/ziahamza/layercache/commit/07bd184ddd75153fb2a06a42b301fb0ac6207fd4)
  passes all fourteen hosted jobs across [CI](https://github.com/ziahamza/layercache/actions/runs/39037344552),
  [PostgreSQL](https://github.com/ziahamza/layercache/actions/runs/39037344535)
  and [local-to-CI](https://github.com/ziahamza/layercache/actions/runs/39037344532).
  Independent Standards/Spec reviews have zero findings at that source.
  The [documentation PR](https://github.com/ziahamza/layercache/pull/55) records
  the subsequent evidence-only update separately from qualified recipes.
  Application workflows and service adoption remain owned by their separate
  tasks.

- [x] **Publish the audited mobile CI case study.**
  Owner: LayerCache documentation. Added the reviewed
  [guide](docs/ci-optimization/case-study/ci-cd-optimization-guide.md) and its
  [six anonymized measurements](docs/ci-optimization/case-study/case-study-measurements.json)
  together, linked from the recipe index, agent playbook and measurement guide.
  Preserve private-source boundaries, distinct reuse identities, observed
  versus causal savings, publisher cost and the required GitHub artifact path.
  [Reviewed content source](https://github.com/ziahamza/layercache/commit/c976ee37d8a6f7c37b9c668abdb8ed4b9e8bf8bc)
  corrects producing-run UTC dates, distinguishes different compression payloads
  and reconciles the older accepted-QA reuse row. The
  [source-bound measurement record](https://github.com/ziahamza/layercache/blob/c976ee37d8a6f7c37b9c668abdb8ed4b9e8bf8bc/docs/ci-optimization/case-study/case-study-measurements.json)
  retains each case's limits. Primary-source and private-evidence review,
  public-pattern checks, all six arithmetic cases, 83 local links/anchors,
  example actionlint and local lint pass. Independent Standards/Spec reviews
  have zero findings at that source. Hosted checks and landing belong to the
  publication PR; these are documentation checks, not new application benchmarks.
  No application workflow, service or diagnostic dispatch is part of publication.

- [x] **Apply the confirmed case-study provenance correction.**
  Owner: LayerCache documentation. The
  [corrected source](https://github.com/ziahamza/layercache/commit/522984f87a866e733cd511f42414f518319fc6c4)
  and [schema 2 record](https://github.com/ziahamza/layercache/blob/522984f87a866e733cd511f42414f518319fc6c4/docs/ci-optimization/case-study/case-study-measurements.json)
  supersede the initial digest comparison and metric names. ZIP size differences
  are non-controlled; producing-run UTC intervals, changed browser coverage and
  zero fresh assertions in accepted iOS QA reuse are explicit. Percentage
  arithmetic, six UTC intervals, 41 local links/anchors, privacy scans and local
  lint pass; independent Standards/Spec reviews check the correction. Hosted
  checks and landing remain in the documentation PR. GitHub artifacts remain
  required; no application benchmark, workflow/service change or diagnostic
  dispatch is part of this correction.

- [ ] **Complete representative native step-overlap qualification.**
  Owner: application CI owners. The first hosted two-vCPU execution overlaps
  both complete tooling command families and joins them before workspace tasks.
  Its [anonymous observation](docs/ci-optimization/measurement.md#observed-examples-and-their-limits)
  records a 56-second group versus 85 seconds in an earlier sequential stage.
  Retain the actual producing source and run evidence in the consumer repository.
  Collect paired same-resource samples with equivalent toolchains and cache
  warmth, including failure/cancellation behavior and complete job allocation,
  before claiming a controlled whole-job or cost improvement. Broader setup,
  cache and provider work remains prioritized by whole-job savings.

- [ ] **Keep LayerCache's own CI fast and exercise its OSS cache path.**
  Owner: LayerCache. Use standard GitHub-hosted runners for this public OSS
  repository. Keep main, manual qualification and relevant code-change gates
  complete. Reduce unrelated work for conservatively classified prose-only PRs
  without losing required check names; comparison failures and unknown files
  run full checks. Reuse native Go compilation and dependency outputs while
  keeping tests and race checks fresh. Preserve isolated cold protocol and
  performance fixtures. Qualify bounded, account-free standalone dogfooding
  through existing native clients before claiming local-to-CI reuse or adopting
  an extra transport whose setup/transfer costs erase the saving.
  Compare actual allocated job spans and full command time. The prior docs-only
  #42 PR used 40m54s across its selected workflows and main used 36m52s, without
  queue time or billing multipliers. Record cold/warm controls and hosted checks
  before marking the delivered slice complete.
  Source: [#43](https://github.com/ziahamza/layercache/pull/43) landed the
  conservative prose lane and native Go compilation reuse, with all 18 checks
  green and independent Standards/Spec reviews. The prose-only
  [#44](https://github.com/ziahamza/layercache/pull/44) measurement preserved all
  13 required contexts and reduced selected workflow allocation from 40m54s to
  2m15s (94.5%); its active execution span was 27s versus 5m21s. Exact source
  and runs are recorded in [self-CI evidence](docs/self-ci.md). The subsequent
  full main run verified an arm64 compiler-cache hit with fresh tests (109s
  versus the preceding 225s miss); x64 and macOS correctly missed changed
  runner-image profiles. Broader hit-rate and whole-job economics remain
  measurement work; standalone LayerCache transport adoption stays unqualified.
  The [opt-in Go archive qualification](qa/go-dogfood/README.md) now uses the
  existing stock Actions v1 client and account-free standalone daemon. On
  Linux amd64 / Go 1.25.13 it reduced a stdlib-only package's cold compile plus
  fresh tests from 4.623s to 0.755s including a 13,076,823-byte loopback restore.
  Source, native compiler-profile, signed-profile, repeated fresh failure and
  forced-cleanup controls passed; [the receipts](docs/evidence/go-dogfood-20261015.json)
  pin exact source `32e3446` and driver/binary digests. Publication, startup and
  bootstrap make first use slower. This is qualification, not routine CI
  adoption, module download, Phone or live local-to-cloud savings. Measure real
  network/setup cost before enrolling another transport.

- [ ] **Keep standalone OSS independent from managed cloud enrollment.**
  Owner: LayerCache product and control plane. Standalone CLI/daemon use is
  account-free on local storage or one self-hosted remote. Signup and scoped
  tokens authorize Team collaboration, multiple managed cache hosts and hosted
  storage. Accounts can bring their own hosts, choose hosted storage, or both.
  Preserve Personal/Team/Public visibility and complete native input context.
  Document the boundary now; qualify account enrollment, host registration,
  project authorization, token revocation and storage routing before claiming a
  deployed managed service. Existing private-cache fixtures are not that rollout.

- [ ] **Qualify required file handoffs independently of evictable caches.**
  Owner: Phone workflow/held-stack owners, coordinated with LayerCache.
  LayerCache owns the separate [retained service contract](docs/retained-artifacts.md)
  and service evidence in P0 above. This application acceptance keeps GitHub
  artifacts as the required provider until fresh actual workflow OIDC and the
  required consumer qualify against the deployed immutable pins.
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

- [x] **Roll out shared cloud rejection of failed input streams.**
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
  lookup miss or qualify cleanup during an object-endpoint outage. The fix is
  included in the immutable beta.5 release, verified installed binaries and the
  [qualified existing gateway upgrade](docs/evidence/gateway-beta5-rollout-20261016.json).
  Only the gateway image pin changed; configuration, authority and storage bounds
  were preserved. Malformed-stream regressions ran against disposable backends.

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
  for the reported incident. The [existing gateway rollout](docs/evidence/gateway-beta5-rollout-20261016.json)
  is complete without establishing that historical cause.

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

- [ ] **Complete native usage and tenant cost metering for the dashboard.**
  Owner: LayerCache measurements and cloud accounting. Extend existing native
  reporting interfaces for Gradle, Nx, Bazel, Xcode and Expo instead of inferring
  task hits from transport requests. Keep native task/compiler/archive results
  distinct and report source, complete bytes and timing coverage. Reuse native
  engine metrics for separate stores, with scoped capacity and maintenance.
  Add persistent byte-hours, request/transfer meters, provider cost imports and
  an explicit shared-overhead policy before reconciling tenant invoices.
  Reuse established telemetry and provider exports rather than a second event
  pipeline. Cover restart, gaps, deduplication, period boundaries and currency
  precision; distinguish estimates, recorded charges and provider reconciliation.
  Historical charts need actual time-series evidence. Current operator cost
  ledgers and project allocations do not complete this work.

- [ ] **Make Android compiler reuse portable across Workspaces.**
  Owner: LayerCache native integration, coordinated with the application CI owner.
  The [real Android pair](docs/evidence/gradle-android-private-20261016.json)
  restored 318 Gradle tasks, but its separately copied warm ccache seed restored
  zero of 94 cacheable compiler calls. Each build also declined 206 PCH calls;
  five link calls account for the remaining uncacheable calls. The
  [primary-source investigation](docs/research/android-compiler-cache-20261016.md)
  identifies differing Debug CWD and external Gradle-home include paths as
  testable causes. First prove a same-root hit and cross-root input difference
  with one actual non-PCH translation unit. Qualify explicit debug path mapping
  or stable logical roots against uncached consumer outputs and dependency paths.
  Test PCH changes, transitive headers, macros and time separately before changing
  engines or transport. Preserve native hashing and complete declared context;
  do not suppress path/macro/header inputs to manufacture hits. Repeat the whole
  Android pair only after the small compiler qualification passes. Keep task,
  compiler, transfer, full-job and billing measures separate.

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

- [ ] **Productize Expo remote build caching through its native provider hook.**
  Owner: LayerCache native integration. The October 20 user request points to
  Expo's [build-cache provider guide](https://docs.expo.dev/guides/cache-builds-remotely/).
  Build on the existing `native/expo.ts` adapter and Expo's
  `resolveBuildCache`/`uploadBuildCache` interface rather than another build
  service. Qualify the supported, pinned Expo SDK/CLI and default fingerprint
  implementation with cold publication and independent fresh-workspace restore
  through Local, Personal and authorized Team caches. Keep Public population
  restricted to trusted publishers. Preserve complete build configuration,
  toolchain, platform and architecture identity, bounded storage, byte integrity,
  read-only consumers and ordinary compilation on misses or unavailable caches.
  Verify current Metro JavaScript after reuse and changed-native-input rebuilds;
  expose attributed outcomes, bytes and complete command timings in the shared
  dashboard before claiming savings. The documented hook serves local
  `npx expo run:android` and iOS Simulator `npx expo run:ios`, not `eas build`
  or physical iOS device builds. Keep embedded build-number behavior explicit.
  Acceptance: released provider setup, install/launch on actual supported targets,
  cross-machine reuse, invalidation, bypass, corruption, cancellation, expiry and
  tenant isolation. General product qualification remains separate from the
  application-owned adoption below; no new build dispatch is requested here.

- [ ] **Qualify shared Debug development clients for Phone.**
  Owner: Phone native owner with LayerCache client support. The existing Expo
  provider is configured, but trusted CI production and fresh local consumption
  need end-to-end proof. Use the app's pinned Expo fingerprint and actual
  Xcode/SDK/architecture or Android toolchain identity. Verify install/launch
  and current Metro JS. Debug clients and bundled Release QA apps have distinct
  identities; physical iOS devices and EAS Build retain their own contracts.

- [ ] **Measure residual iOS work before extending shared LLVM transport.**
  Owner: Phone native owner with LayerCache artifact support. Two retained
  RunCloud samples already hit all 1,725
  cacheable compiler tasks but occupied Macs for 16m44s/18m28s. Time setup,
  packaging, runtime acceptance and receipt misses separately. Qualify released
  graph retention on Phone's exact toolchain/scale before expanded use. Swift
  replay and signed-build cost remain separate evidence targets. The
  [October 19 Linux/Mac research](docs/research/bazel-and-layercache-20261019.md#reduce-mac-work-before-migrating-an-expo-app)
  prioritizes native-shell reuse with current JS/assets over a full build-system
  migration. Qualify a compatible JS-only change and a changed-native control,
  fresh simulator assertions, exact native/toolchain/Hermes/environment identity,
  and source/provenance checks. Separate native-shell, bundled-app and QA-verdict
  identities. Measure native seed amortization, actual Apple worker allocation,
  Linux preparation/repack, transfers and whole-workflow time; a Linux controller
  does not establish reduced Mac use. Keep store release, experimental signing
  and simulator QA qualification separate. Turbo can remain the JS orchestrator;
  consider a bounded Bazel pilot only if residual native action work justifies it.

- [ ] **Qualify compiler-cache lifecycle with Apple-managed Xcode builds.**
  Owner: LayerCache compiler client with the application build owner. On October
  20 the user authorized standalone lifecycle, scoped authentication and joint
  Xcode Cloud acceptance after cache-enabled managed builds still missed their
  compiler actions. Preserve Apple's managed build/test lifecycle. Reuse the
  existing native archive save/restore transport for closed stores, with a
  bounded rootless service mode, explicit readiness and instance-safe shutdown
  acknowledgment only after RPC handlers and SQLite close. Provision separate
  revocable reader and trusted-publisher bootstrap grants bound to one project,
  exact toolchain profile and snapshot namespace. Exchange them for short-lived
  exact-artifact capabilities; an operator-provisioned shared credential does
  not attest to Apple identity or a caller-reported branch. Keep writer secrets
  out of untrusted source execution. Reject local/queued acceptance as hosted
  snapshot publication, and keep required retained handoffs separate. Qualify actual
  worker/toolchain/SDK/target identities, fresh-worker restores and relevant
  changed-input misses, complete graph integrity, readiness, cancellation,
  closure before snapshots and trusted publication. Do not reuse GitHub OIDC
  as an Apple workload identity or introduce a broad publication secret.
  Xcode 27 source replay, managed producer/consumer acceptance and external
  Swift replay have separate gates; the Xcode 26.6 Clang proof does not establish
  later toolchain or language coverage. LayerCache owns its implementation,
  deployment and pins; the application owner owns hook/settings changes and
  bounded Cloud producer/consumer acceptance. Record source, installed binary,
  deployed authorization, restored snapshots and language-specific replay
  separately before marking this complete.

  [PR #76](https://github.com/ziahamza/layercache/pull/76) merged standalone
  readiness/closure, revocable scoped grants and the existing native immutable
  archive client. The actual server/CLI fixture passes 24 synthetic-CAS controls
  locally and in the [hosted macOS job](https://github.com/ziahamza/layercache/actions/runs/39467518289/job/118782677413).
  The strict Xcode 27 gate passed at `fc8b449` in the
  [candidate job](https://github.com/ziahamza/layercache/actions/runs/39467518317/job/118783279071)
  and repeated after merge at `86d2383d` in the
  [post-merge job](https://github.com/ziahamza/layercache/actions/runs/39468895209/job/118788090537).
  Both recorded 10/10 action and 20/20 CAS hits, main-source/PCH replay and a
  changed-source compilation miss producing 43 instead of 42 with correct
  disassembly. This is Clang/Objective-C and PCH proof on the same VM and source
  path with fresh processes, native CAS and DerivedData, not fresh-worker or
  managed Cloud acceptance. Its phase timings establish no application savings.
  Merged source `86d2383d` is live and healthy with scoped reader and
  trusted-publisher grants active. All 13 live gateway controls passed. An
  independent read-only audit checked SQL/R2 metadata and verified SHA256 over
  the complete 4,443-byte archive. Six live revocation controls passed; the
  original grant-file identity and bytes were restored without restart or
  bootstrap rotation. All 132 dashboard checks pass. Beta.6 stays
  tagged at `86d2383d` and unpublished. Beta.7 is published and immutable at
  `104b3fb7`; its installer and application acceptance remain separate gates.
  No managed Xcode Cloud seed is available;
  fresh-worker producer/consumer acceptance, external Swift replay and
  application savings remain unqualified.
  The [native qualification contract](docs/xcode-cache.md#compiler-behavior-and-native-qualification)
  records the bounded inline-leaf grammar and native API proof. Its QA manifest
  fails closed above 64 KiB per child, 256 children per target or 1,024 overall;
  optional root-only counts are deferred and cannot qualify replay.

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

- [ ] **Support native Bazel caching through an isolated OSS engine.**
  Owner: LayerCache. User outcome: share native Bazel build/test results across
  local machines and authorized CI without duplicating Bazel's build graph or
  requiring non-Bazel applications to migrate. The October 19 user decision
  advances this integration into P1; required retained handoffs remain P0.
  First source slice: an opt-in native HTTP endpoint backed by a dedicated
  pinned `bazel-remote` executable. Bind its project and compatibility explicitly,
  preserve current membership and server-enforced read-only authority, and reject
  Public access. Use a separate bounded engine volume; LayerCache's artifact
  quota does not cap sidecar bytes. Instance labels alone do not isolate CAS.
  See the [Bazel comparison](docs/research/bazel-and-layercache-20261019.md).
  Acceptance: real native producer and independent empty read-only consumer,
  exact outputs, changed-input/profile misses, failed-test reruns, revoked and
  foreign-project denial, incomplete-CAS fallback, quota/eviction and restart
  behavior. Record whole-command time and allocated storage including setup and
  transfers. Distinguish source qualification from installation, deployment and
  application adoption. Local forwarding, gRPC, remote execution and trusted
  Public publication need separate qualification. Follow the
  [OSS plan](docs/oss-integration-plan.md).
  [Source qualification](docs/evidence/bazel-native-private-20261019.json) passes
  ten real Bazel 9.3.0 commands at `d3f9bda`, including empty/restart readers,
  source/tool/environment misses, repeated failing tests and incomplete CAS.
  A 1 GiB logical engine accepts 1,050 MiB of unique CAS writes under eviction;
  read-only rebuild remains correct. This does not reach the 2 GiB hard fence
  or establish an application speedup. The source passes the Go suite, focused
  race tests, vet and independent Standards/Spec reviews. [PR #60](https://github.com/ziahamza/layercache/pull/60)
  merged as `42411411ebe50102e9e462fd186be2305961b98e` after all fifteen
  hosted checks passed, including the real native Bazel client. Published
  installation, cloud deployment, actual Bazel application adoption
  and account/CLI credential-helper onboarding remain separate gates.

- [ ] **Qualify Swift compilation replay through Xcode 26's native cache seam.**
  Owner: LayerCache native integration. Existing Clang/C-family replay and graph
  retention are qualified; the mixed-language fixture does not establish Swift
  source compilation hits. Audit the actual Apple plugin/socket integration and
  native Swift action identity before choosing an implementation. Reuse the
  toolchain's provided plugin and upstream protocol where available. Acceptance:
  clean producer and independent consumer with fresh DerivedData/native CAS,
  attributable Swift compilation replay, exact executable, changed-source and
  incompatible-toolchain misses, bounded graph retention and truthful counters.
  Preserve application compiler compatibility; do not enable unsupported flags
  merely to obtain hits. SwiftPM support, native network transport and full
  application/signed-release economics remain separate targets.

## P2: broaden after measured value

- [ ] **Qualify sccache when a measured workload justifies it.**
  Owner: LayerCache. Reuse its native compiler identity and permissively licensed
  engine. Preserve bounded storage, private authority and honest whole-command
  evidence; keep unsupported compiler/toolchain cases explicit.

- [x] **Qualify the existing operator gateway upgrade.**
  Owner: LayerCache/operator. Exact source `53ec27443a406a508b7e8faeab3f46292e0ba07b`
  passed the 91-check hardened image rehearsal, then 170 checks against a full
  cold recovery copy through old, new, restart, old rollback and forward phases.
  The same scoped credentials and exact legacy bytes survived the transitions.
  [Live acceptance](docs/evidence/gateway-beta5-rollout-20261016.json) preserves
  all five project configurations, backend images, authority, routes and storage
  limits while changing only the gateway image pin. Existing user deployment
  edits remain untouched; rollback image and protected recovery copies are retained.
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
  [evidence](docs/evidence/cloud-stream-integrity-20261015.json). The beta.5
  gateway rollout is qualified above.
- [x] Source-qualified native Gradle HTTP over existing bounded artifact storage,
  expiring process credentials and an opt-in init script, source `3b9ded9`.
  The [22-command real-client qualification](docs/evidence/gradle-native-private-20261015.json)
  and [managed Personal policy proof](docs/evidence/gradle-personal-policy-20261015.json)
  pass. The Linux native-client CI gate uses the pinned Gradle distribution and
  candidate binary. Beta.5 installation and the existing gateway rollout are
  qualified above, as is the first hosted Phone producer/full-reader gate.
  Local forwarding, net-benefit measurement and signed/ARM qualification remain
  open.
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
