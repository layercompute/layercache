# LayerCache launch plan, 2026-10-16

[TODOs.md](../TODOs.md) owns current priorities and outstanding acceptance.
Reuse successful local tasks in required CI checks, preserve reliable required
handoffs, and measure Phone whole-job savings. Keep task commands present,
restore compatible deterministic results, and execute on a miss. Protect
useful outputs and bounded storage before expanding machine management or
hosted runners. The existing operator-managed
Team Cache is available again. The self-serve cloud implementation is present,
but its public service has not been deployed.

The product has three visibility tiers: Personal Cache, Team Cache and Public
Cache. Local Cache is each machine's bounded storage under those tiers. Private
projects reuse full native build context; Public Cache accepts only the trusted
build and collector publication path. See the [tier contract](cache-tiers.md).
Native OSS clients and permissively licensed cache engines should supply build
hashing and execution as we add integrations.

## Current state and ownership

| Area | State | Next acceptance target |
| --- | --- | --- |
| Existing Team Cache | [Beta.5 gateway and host installation](evidence/gateway-beta5-rollout-20261016.json) passed on October 16, after full cold recovery and 170 isolated restore/upgrade/restart/rollback checks. All five configured projects pass authenticated health and exact legacy/Gradle canaries. | Preserve monitoring and storage limits; measure useful fresh-runner restores for each adopted integration. |
| Local results available to CI | [#23](https://github.com/ziahamza/layercache/pull/23) added Turbo publication confirmation, durable repair and explicit CI cache modes. [Phone #241](https://github.com/ziahamza/phone-app/pull/241) merged main-writer and same-repository PR-reader lanes. Installed-release QA corrections merged in [#29](https://github.com/ziahamza/layercache/pull/29). | Prove a directly observed main-to-PR remote restore with an empty native cache, and measure whole-job time including setup, transfer and publication. Native snapshot hits alone do not attribute savings to LayerCache. The Phone CI chat owns application adoption. |
| Phone test groups | [Phone #298](https://github.com/ziahamza/phone-app/pull/298) merged as `3bae98fe`, preserving thirty isolated workflow invocations under ten Turbo groups and required typechecking. [Qualification](phone-test-reuse.md) includes private publication/fresh consumption and current hosted Check plus Android Contacts/iOS startup-CallKit acceptance. The simulator entitlement repair is qualified with unchanged strict verification. The installed Local service restores all 28 tasks in an 807ms native stage and confirms Team availability. | Measure representative follow-up whole jobs and direct Team versus snapshot attribution. Current unit observations are 8m28s and 7m13s, not monthly or controlled savings. Production RunCloud-to-TestFlight qualified in [#305](https://github.com/ziahamza/phone-app/pull/305#issuecomment-6178375570); exact-source release-validation reuse is now measured in #310 below. |
| Phone release-validation reuse | [Phone #310](https://github.com/ziahamza/phone-app/pull/310) merged as `9f38e36d`. Exact-source main Check and [signed canary](https://github.com/ziahamza/phone-app/actions/runs/38730000568) pass. Preparation is 3m16s versus 28m18s previously; matching native-QA reuse is 2m12s versus 19m46s fresh. | Build-only canary skips TestFlight/deployed smoke. Preserve fresh fallback and signing/inspection gates; other stage changes and workflow totals are not controlled savings. |
| RunCloud dependency reuse | Prior signed worker spent 2m13s in pnpm and 6m25s in CocoaPods; provider policy disabled dependency storage. [Phone #310](https://github.com/ziahamza/phone-app/pull/310) records the evidence. | Establish supported cache policy or restore/save hooks, then qualify complete context, bounded storage and current paired phase timings. The prior 8m38s is not promised savings. Phone Gradle adoption remains independently testable. |
| Actions acknowledgement diagnosis | [#36](https://github.com/ziahamza/layercache/pull/36) merged at `fc98937922f69daad788a89cc132845ff4c0c79e`. Real PostgreSQL/S3 SDK qualification restores the reported payload size and distinguishes controlled misses; the historical incident's cause remains unknown. | Obtain exact incident identity and authoritative removal/commit evidence. The [existing gateway rollout](evidence/gateway-beta5-rollout-20261016.json) is complete; this does not establish the historical miss cause. |
| Gradle native HTTP | [Source qualification](gradle-cache.md) at `3b9ded9` passes 22 real Gradle commands plus managed Personal owner/isolation controls and real PostgreSQL/S3 SDK malformed-upload regressions. It reuses bounded artifact storage and native hashes/execution. [Phone #310](https://github.com/ziahamza/phone-app/pull/310) merged release-validation reuse separately; its owner retains signed measurement. | An [independent Android pair](evidence/gradle-android-private-20261016.json) restored 318 tasks and reduced one local command from 880.213s to 524.971s. [Beta.5 installation](evidence/beta5-installed-20261016.json) passes all supported architectures, with installed Gradle acceptance on Linux x64. Existing gateway and five host Local daemons now run the reviewed beta.5 source. The Phone CI owner is preparing workflow adoption and whole-job measurement. Local-to-private forwarding and Public support remain separate. |
| AI SDK LLM replay | [#37](https://github.com/ziahamza/layercache/pull/37) merged at `453c09487a2268700821417e830d2fb1e3296526`, with actual SDK fixtures, 72 local tests, independent reviews, [hosted source qualification](https://github.com/ziahamza/layercache/actions/runs/38580949339) and a [direct private gateway proof](evidence/llm-private-gateway-20261014.json). | Qualify real planner adoption with a raw judge. Streaming, a shared LLM gateway, durable corpora and Local-to-Team winner reconciliation remain outstanding. CLI release and service deployment have separate qualification. |
| Personal Cache | Owner-only private projects merged in [#28](https://github.com/ziahamza/layercache/pull/28), with stable numeric ownership, separate project identities and owner-authorized CI. | Qualify cross-machine use through the configured private endpoint. The implementation does not establish a deployed self-serve service. |
| Nx native HTTP | [#31](https://github.com/ziahamza/layercache/pull/31) merged at `7404090ac915c015dad37ef92ca662f3dec90a5f`. The released Nx client passed Local and direct private replay, owner/Team isolation, changed-input misses and failed-test reruns. | Add Local-to-private read-through, durable publication, exact-hash confirmation and reporting before claiming local Nx results are available to CI. |
| Phone Xcode integration | [#20](https://github.com/ziahamza/layercache/pull/20) added the adapter. [#30](https://github.com/ziahamza/layercache/pull/30) merged native graph retention after real multiple-root Xcode replay under quota pressure. | Qualify Phone-scale lookup/restore timing and its pinned toolchain before expanding shared native use. Swift cache hits and signed-release timing remain separate measurements. The Phone CI chat owns application workflows. |
| Cloud input integrity | Source `3d5e2df` rejects failed input streams at shared S3 staging, including failures after an observed multipart upload. [Contract and evidence](retention.md#upload-completion) preserve exact complete bytes, quota behavior and the global error sanitizer. | Beta.5 installation and the [existing private gateway rollout](evidence/gateway-beta5-rollout-20261016.json) are complete. Endpoint-outage cleanup and the historical Actions HTTP 204 cause remain unqualified. |
| Storage correctness | [#27](https://github.com/ziahamza/layercache/pull/27) merged permanent Turbo conflict handling and pin retirement for [#24](https://github.com/ziahamza/layercache/issues/24). Existing byte quotas, LRU, pins and durable uploads remain the foundation. | Preserve conflict retirement and bounded graph leases in future native publication work. Retain LRU until representative workload traces justify another policy. |
| Cloud authentication and acceptance | [#16](https://github.com/ziahamza/layercache/pull/16), [#17](https://github.com/ziahamza/layercache/pull/17), and [#18](https://github.com/ziahamza/layercache/pull/18) are merged. Recovery [#21](https://github.com/ziahamza/layercache/pull/21) merged at `447095385671323a186299e0718d10c6459abb6f`, including the unchanged device-flow fix from closed, superseded [#19](https://github.com/ziahamza/layercache/pull/19). Beta.2 passed fresh native installation; beta.3 retains those fixes. | Use the verified release for the limited cloud deployment once its real inputs are configured. |
| Public self-serve cloud | Draft [config #27](https://github.com/ziahamza/config/pull/27) prepares deployment. No service, dedicated pool, protected cloud configuration, or new public route has been activated. | Real GitHub OAuth, an approved creator cohort, isolated bounded storage, and live two-user acceptance before declaring a hosted beta. |
| Machine enrollment and hosted runners | Deferred. | Useful cross-machine cache reuse and operating costs measured first. |

The LayerCache recovery effort owns integration, PR disposition, deployment
evidence, and this plan. LayerCache owns the separate Phone package-task and
cache qualification and bounded simulator repair explicitly delegated in
[the Phone handoff](https://github.com/ziahamza/phone-app/pull/298#issuecomment-6173637809).
The Phone CI chat owns workflow and production release routing. Coordinate
separate branches and acceptance evidence for follow-ups.

## Recovery evidence

The public Caddy proxy stopped cleanly on September 26 at 23:00:01 UTC. Its
`unless-stopped` restart policy kept it down. Cache processes and their loopback
health remained healthy, while public HTTPS connections were refused. The logs
do not establish who stopped the proxy.

On October 11, the existing Caddy configuration passed validation in an isolated
container. Restarting only `platform-ingress` restored the pre-existing routes
at 15:46:56 UTC. No cache image, app data, firewall, DNS, credential, or cloud
route changed. The host monitor reports `healthy:true`. The independent
[GitHub HTTPS and certificate check](https://github.com/ziahamza/layercache/actions/runs/38152753823)
passed at 15:48:34 UTC.

At recovery, the gateway ran image `layercache:qa-16868d9`, digest
`sha256:d69ec986b4d2e76a0acc560add087a6bc4cd60879446e71aa1c4827492cff727`.
It predates the Xcode adapter. The physical cache pool is 48 GiB with a 10 GiB
reserve; roughly 161 MiB was used during recovery. Each of the five projects has
a 5 GiB Team Cache quota and reported zero committed artifacts during recovery.
Availability is established. These checks do not establish current reuse or
build-cost savings.

The former GitHub account billing lock no longer prevents jobs from executing.
All four cloud PRs were revalidated together at integration commit
`3d80c178eac3d1423a8ed8b016b2e8a88cf8e613`. Full Go tests, race tests, vet,
JavaScript checks and builds, workflow/shell lint, three PostgreSQL 16 store
race runs, and 78 browser checks across Chromium, Firefox, and WebKit passed.
Further review found that delayed OAuth processing could extend a session past
the intended provider deadline. #17 now fixes and tests that case, including
PostgreSQL session expiry and retention. The Turbo acceptance harness in #18
now handles interruption asynchronously and stops its owned command process
groups, including Turbo descendants. Actual SIGINT and SIGTERM regressions
passed. An unrelated CI fixture gave promotion leases only 80 milliseconds;
the recovery change retains delayed responses, proves three accepted renewals,
and tests cancellation and invalid-renewal rejection. Fifteen race-test runs
passed.

The real CLI/Turbo acceptance test produced a cold miss and a second user's
remote hit from a fresh clone and empty Local Cache after the producer daemon
stopped. It executed the task once, transferred 261 bytes each way, restored
matching output, and denied the removed reader's previously issued capability.
GitHub and the cloud origin were emulated. This is development acceptance, not
evidence of a live public cloud or customer-machine reuse.

The immutable [v0.1.0-beta.2 release](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.2)
pins protected main commit `447095385671323a186299e0718d10c6459abb6f` after
all required recovery checks passed. Its
[release workflow](https://github.com/ziahamza/layercache/actions/runs/38155658632)
passed native candidate qualification, persistence, performance, and provenance
checks, then published the immutable release at 16:33:44 UTC. The
[published-binary installer workflow](https://github.com/ziahamza/layercache/actions/runs/38156102533)
passed on fresh Linux x64, Linux arm64, and macOS arm64 runners. Each job checked
the locked tag and source commit, verified attestations and checksums, installed
into an empty prefix, and exercised `help` and setup preview. The workflow pins
both beta.2 and the same source checkout. Beta.2 excludes the Xcode adapter,
which remains under its separate qualification.

Beta.3 pins protected main commit `d99e6f685b5db878b924181926399c57be9f16d7`,
which includes the merged Xcode adapter and
[local-to-CI Turbo reuse](local-ci-reuse.md). Its
[release workflow](https://github.com/ziahamza/layercache/actions/runs/38478627110)
and [published installer qualification](../.github/workflows/beta-release-install.yml)
retain exact-source, provenance and immutable-publication checks. The installer
qualification adds the 17-scenario real Turbo acceptance against the installed
release binary on Linux x64, Linux arm64, and macOS arm64. The evidence covers
successful local and Team restores, failed-test reruns, changed-input misses,
fresh execution and offline publication recovery. It does not qualify arbitrary
task definitions or provide a commit-level attestation that a developer ran tests.

Beta.4 pins protected main commit `ca3d5889a52815bce9565d3825b081839ac7077b`,
including the storage fix, Personal projects, native Nx endpoints and complete
native Xcode graph retention. Its
[release workflow](https://github.com/ziahamza/layercache/actions/runs/38552629557)
qualifies all three native binaries, persistence, performance and provenance.
The [published installer qualification](../.github/workflows/beta-release-install.yml)
uses the same exact source and runs both the 17-scenario Turbo contract and
compiled Nx client against downloaded binaries. Beta.4 requires conflict pins
to retire; it has no historical-beta exception. These are release/fixture proofs,
not measurements of Phone's complete job or a deployed self-serve cloud.

Beta.5 pins protected main commit `53ec27443a406a508b7e8faeab3f46292e0ba07b`,
adding native Gradle HTTP, a direct scoped OIDC action and complete-stream
publication guards. Its [immutable release](https://github.com/ziahamza/layercache/actions/runs/38893572601)
passed native candidate and persistence gates. The
[published installer run](https://github.com/ziahamza/layercache/actions/runs/38895127715)
passed Linux x64, Linux arm64 and macOS arm64, including 17 Turbo scenarios and 12 Nx stages on
each platform and 22 Gradle stages on Linux x64. The
[qualification summary](evidence/beta5-installed-20261016.json) binds the Gradle
receipt to the published binary, release source and init script. Gradle on other
architectures and whole application CI savings remain unqualified.

The [existing operator rollout](evidence/gateway-beta5-rollout-20261016.json)
changes only the gateway image pin to the reviewed beta.5 source. It preserves
all project configuration, authority, backend images, the 48 GiB physical pool,
10 GiB reserve, per-project 5 GiB committed quotas and retention settings. A full
cold copy of pool/configuration/old CLI was restored in isolated services before
upgrade, including exact credential reuse through rollback. Live HTTPS checks
restore old Turbo and signed Actions canaries, qualify Gradle access and duplicate
behavior, and repeat after restart. All five Local daemons now run the attested
published beta.5 binary, with healthy authenticated status and no queued uploads.
Protected recovery data stays outside the public repository. These are tiny
protocol canaries; application workflow and current runtime acceptance remain
separate. Public self-serve and Public Builds have not been activated.

## Outstanding work and priorities

[TODOs.md](../TODOs.md) owns the active backlog and acceptance targets. Its
current order puts reliable Phone handoffs and reusable test groups first,
then exact LLM result replay and shared native outputs. Protocol expansion and
cloud/runners follow measured personal value. Follow
[the roadmap review](roadmap-review.md) when new evidence changes that order.

This document retains qualification and deployment status. A published CLI,
installed client, accepted application workflow and deployed service are
separate outcomes. The limited cloud still needs real OAuth, an approved
numeric cohort, isolated storage and live two-user acceptance. Public Builds
retain their separate trusted-publisher and native KVM qualification.

The beta stays limited to an explicit cohort and operator budget. Admission is
bounded in code, and managed projects bind to signed numeric repository IDs.
Before unrestricted signup, add billing/quotas, account and project lifecycle,
and support escalation. The
[v0.1.0-beta.5 CLI beta](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.5)
does not make the hosted service live. Public Builds remain unqualified.
