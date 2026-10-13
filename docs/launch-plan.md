# LayerCache launch plan, 2026-10-14

Reusing successful local tasks in required CI checks is the immediate priority.
Keep the task commands present, restore compatible deterministic results, and
execute on a miss. Protect useful compiler outputs and bounded storage before
expanding the dashboard, machine management, or hosted runners. The existing
operator-managed Team Cache is available again. The self-serve cloud
implementation is present, but its public service has not been deployed.

## Current state and ownership

| Area | State | Next acceptance target |
| --- | --- | --- |
| Existing Team Cache | Public HTTPS restored on October 11. All five configured projects pass authenticated status checks; anonymous Phone access is denied. | Keep external health and capacity monitoring green, then prove useful fresh-runner restores for each integration actually in use. |
| Local results available to CI | [#23](https://github.com/ziahamza/layercache/pull/23) merged at `d99e6f685b5db878b924181926399c57be9f16d7`. Beta.3 includes successful Turbo-task publication confirmation, durable publication repair and explicit CI cache modes. | Verify the installed beta.3 artifacts on all three native architectures, then adopt a small audited task lane with read-only PR reuse. The active Phone CI chat owns Phone adoption. |
| Phone Xcode integration | [LayerCache #20](https://github.com/ziahamza/layercache/pull/20) is merged and the adapter is included in beta.3. Real Xcode replay after deleting native CAS passed. The Phone CI chat owns workflow integration. | Protect complete graphs during streamed restores in [#25](https://github.com/ziahamza/layercache/issues/25), then qualify behavior under quota pressure before expanding shared native-cache use. |
| Storage correctness | Existing byte quotas, LRU, pins and durable uploads remain the foundation. [#24](https://github.com/ziahamza/layercache/issues/24) tracks permanent Turbo archive conflicts retaining pins; [#25](https://github.com/ziahamza/layercache/issues/25) tracks Xcode graph consistency during reads. | Classify permanent publication conflicts, release their pins, and protect complete native graphs within the storage budget. |
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

The existing gateway still runs image `layercache:qa-16868d9`, digest
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

## Priorities and landing gates

1. **Reuse successful local tasks in CI.** Keep explicit compatible execution
   profiles and complete deterministic inputs. Confirm publication with
   `run --sync-team`; use `cache-mode: read-only` in required PR task lanes and
   `cache-mode: fresh` for deliberate fresh checks. Preserve failed task exit
   codes and execute on misses. The active Phone CI chat owns the initial
   audited task lane, input fixes and rollout. Network/live suites and mutable
   Git-base checks retain fresh execution policies. Measure whole-job savings
   separately from task cache hits.
2. **Finish storage and compiler correctness.** Resolve permanent archive
   conflict retries and retained pins in
   [#24](https://github.com/ziahamza/layercache/issues/24). Protect complete Xcode
   graphs through streamed reads in [#25](https://github.com/ziahamza/layercache/issues/25),
   with quota-pressure and eviction acceptance. Retain bounded LRU until native
   traces justify a different admission policy. Keep useful shared snapshot
   baselines within provider budgets. Local snapshots alone are not a remotely
   hosted compiler service. Expand shared native use after these checks pass.
3. **Keep verified distribution pinned.** Qualify the installed beta.3 binary
   with the real Turbo client on all three native architectures. Keep OAuth
   fixtures confined to QA. Beta.2's historical publication and installer
   evidence remains valid; current examples use beta.3's exact source. Do not
   bypass branch or release protections.
4. **Activate one limited cloud beta after its live inputs exist.** Configure
   the real OAuth app with device flow and callback
   `https://cloud.services.ziahamza.com/auth/callback`, protected secret files,
   approved numeric creator IDs, a separate storage pool, and restart/backup
   recovery. Keep config #27 draft until its reviewed release and real inputs
   are ready. From two real identities, verify invitation acceptance, a cold
   upload, a fresh-machine Team Cache restore, role downgrade/removal, and
   recovery after restart through the public HTTPS origin.
5. **Make successful onboarding repeatable.** Provide copyable CI setup pinned
   to a verified release. Show measured source, bytes, hit status, transfer
   overhead, and timing in the dashboard. Keep misses non-blocking. Resolve
   repository renames against the stored immutable numeric GitHub repository
   identity. Qualify Parle's native Safari reuse separately when Phone's cache
   path is working.
6. **Expand after useful reuse is measured.** Add revocable machine identities,
   enrollment, last-seen state, and usage reporting. Then consider runner
   admission, leases, cancellation, isolation, cleanup, and usage accounting.
   Public Builds retain their separate publisher trust model and native KVM
   qualification. Windows, Actions cache v2, and a wider adapter SDK remain
   later work.

The beta stays limited to an explicit cohort and operator budget. Admission is
bounded in code, and managed projects bind to signed numeric repository IDs.
Before unrestricted signup, add billing/quotas, account and project lifecycle,
and support escalation. The
[v0.1.0-beta.3 CLI beta](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.3)
does not make the hosted service live. Public Builds remain unqualified.
