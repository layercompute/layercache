# LayerCache launch plan, 2026-10-14

Reusing successful local tasks in required CI checks is the immediate priority.
Measure whole-job savings in Phone and complete Nx read-through and durable
publication between Local Cache and private endpoints before expanding Nx use. Keep the task commands
present, restore compatible deterministic results, and execute on a miss.
Protect useful compiler outputs and bounded storage before expanding the
dashboard, machine management, or hosted runners. The existing operator-managed
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
| Existing Team Cache | Public HTTPS restored on October 11. Recovery checks passed authenticated status for all five configured projects and denied anonymous Phone access. | Keep external health and capacity monitoring green, then prove useful fresh-runner restores for each integration actually in use. |
| Local results available to CI | [#23](https://github.com/ziahamza/layercache/pull/23) added Turbo publication confirmation, durable repair and explicit CI cache modes. [Phone #241](https://github.com/ziahamza/phone-app/pull/241) merged main-writer and same-repository PR-reader lanes. Installed-release QA corrections merged in [#29](https://github.com/ziahamza/layercache/pull/29). | Prove a directly observed main-to-PR remote restore with an empty native cache, and measure whole-job time including setup, transfer and publication. Native snapshot hits alone do not attribute savings to LayerCache. The Phone CI chat owns application adoption. |
| Personal Cache | Owner-only private projects merged in [#28](https://github.com/ziahamza/layercache/pull/28), with stable numeric ownership, separate project identities and owner-authorized CI. | Qualify cross-machine use through the configured private endpoint. The implementation does not establish a deployed self-serve service. |
| Nx native HTTP | [#31](https://github.com/ziahamza/layercache/pull/31) merged at `7404090ac915c015dad37ef92ca662f3dec90a5f`. The released Nx client passed Local and direct private replay, owner/Team isolation, changed-input misses and failed-test reruns. | Add Local-to-private read-through, durable publication, exact-hash confirmation and reporting before claiming local Nx results are available to CI. |
| Phone Xcode integration | [#20](https://github.com/ziahamza/layercache/pull/20) added the adapter. [#30](https://github.com/ziahamza/layercache/pull/30) merged native graph retention after real multiple-root Xcode replay under quota pressure. | Qualify Phone-scale lookup/restore timing and its pinned toolchain before expanding shared native use. Swift cache hits and signed-release timing remain separate measurements. The Phone CI chat owns application workflows. |
| Storage correctness | [#27](https://github.com/ziahamza/layercache/pull/27) merged permanent Turbo conflict handling and pin retirement for [#24](https://github.com/ziahamza/layercache/issues/24). Existing byte quotas, LRU, pins and durable uploads remain the foundation. | Preserve conflict retirement and bounded graph leases in future native publication work. Retain LRU until representative workload traces justify another policy. |
| Cloud authentication and acceptance | [#16](https://github.com/ziahamza/layercache/pull/16), [#17](https://github.com/ziahamza/layercache/pull/17), and [#18](https://github.com/ziahamza/layercache/pull/18) are merged. Recovery [#21](https://github.com/ziahamza/layercache/pull/21) merged at `447095385671323a186299e0718d10c6459abb6f`, including the unchanged device-flow fix from closed, superseded [#19](https://github.com/ziahamza/layercache/pull/19). Beta.2 passed fresh native installation; beta.3 retains those fixes. | Use the verified release for the limited cloud deployment once its real inputs are configured. |
| Public self-serve cloud | Draft [config #27](https://github.com/ziahamza/config/pull/27) prepares deployment. No service, dedicated pool, protected cloud configuration, or new public route has been activated. | Real GitHub OAuth, an approved creator cohort, isolated bounded storage, and live two-user acceptance before declaring a hosted beta. |
| Machine enrollment and hosted runners | Deferred. | Useful cross-machine cache reuse and operating costs measured first. |

The LayerCache recovery effort owns integration, PR disposition, deployment
evidence, and this plan. The Phone CI chat owns active Phone task-input, PR reuse
and native workflow changes. Keep those branches separate while implementation
is underway.

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

## Priorities and landing gates

1. **Measure Phone jobs and complete private task reuse.** Measure the merged
   Phone lanes without attributing native snapshot hits to the remote service.
   Finish read-through and durable Nx publication between Local Cache and
   Personal/Team endpoints before expanding Nx adoption. Add exact-hash confirmation and
   reporting before claiming that a local Nx result is available to CI.
   Keep explicit compatible execution
   profiles and complete deterministic inputs. For existing Turbo lanes,
   confirm publication with `run --sync-team`; use `cache-mode: read-only` in
   required PR task lanes and `cache-mode: fresh` for deliberate fresh checks.
   Preserve failed task exit codes and execute on misses. The active Phone CI chat owns
   application input fixes, rollout and post-merge measurements. Network/live suites and mutable
   Git-base checks retain fresh execution policies. Measure whole-job savings
   separately from task cache hits.
2. **Preserve compiler and storage correctness.** Keep the merged permanent
   conflict, pin-retirement and graph-pressure regressions green. The native
   multiple-root gate in [#30](https://github.com/ziahamza/layercache/pull/30)
   passed; qualify Phone's larger workload before adopting a new adapter pin. Retain bounded LRU until native traces justify a different
   admission policy. Keep useful shared snapshot baselines within provider
   budgets. Local snapshots alone are not a remotely
   hosted compiler service. Expand shared native use after these checks pass.
3. **Add native OSS integrations in order.** Next qualify Gradle HTTP caching
   for audited Phone Android tasks. Follow with an isolated, pinned
   `bazel-remote` sidecar after native protocol qualification, then sccache.
   Reuse native task hashes, clients and existing engines. Require fresh-consumer
   output checks and private-scope isolation before application adoption. See
   the [integration plan](oss-integration-plan.md).
4. **Keep verified distribution pinned.** Use beta.4's exact source and
   qualified downloaded binaries on all three native architectures. Keep
   OAuth fixtures confined to QA. Beta.2's historical publication and installer
   evidence remains valid; current examples use beta.4's exact source. Do not
   bypass branch or release protections.
5. **Activate one limited cloud beta after its live inputs exist.** Configure
   the real OAuth app with device flow and callback
   `https://cloud.services.ziahamza.com/auth/callback`, protected secret files,
   approved numeric creator IDs, a separate storage pool, and restart/backup
   recovery. Keep config #27 draft until its reviewed release and real inputs
   are ready. From two real identities, verify invitation acceptance, a cold
   upload, a fresh-machine Team Cache restore, role downgrade/removal, and
   recovery after restart through the public HTTPS origin.
6. **Make successful onboarding repeatable.** Provide copyable CI setup pinned
   to a verified release. Show measured source, bytes, hit status, transfer
   overhead, and timing in the dashboard. Keep misses non-blocking. Resolve
   repository renames against the stored immutable numeric GitHub repository
   identity. Qualify Parle's native Safari reuse separately when Phone's cache
   path is working.
7. **Expand after useful reuse is measured.** Add revocable machine identities,
   enrollment, last-seen state, and usage reporting. Then consider runner
   admission, leases, cancellation, isolation, cleanup, and usage accounting.
   Public Builds retain their separate publisher trust model and native KVM
   qualification. Windows, Actions cache v2, and a wider adapter SDK remain
   later work.

The beta stays limited to an explicit cohort and operator budget. Admission is
bounded in code, and managed projects bind to signed numeric repository IDs.
Before unrestricted signup, add billing/quotas, account and project lifecycle,
and support escalation. The
[v0.1.0-beta.4 CLI beta](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.4)
does not make the hosted service live. Public Builds remain unqualified.
