# LayerCache launch plan, 2026-10-11

Phone's Xcode compilation cost is the immediate priority. Restore useful compiler
outputs across fresh Macs before expanding the dashboard, machine management, or
hosted runners. The existing operator-managed Team Cache is available again.
The self-serve cloud implementation is present, but its public service has not
been deployed.

## Current state and ownership

| Area | State | Next acceptance target |
| --- | --- | --- |
| Existing Team Cache | Public HTTPS restored on October 11. All five configured projects pass authenticated status checks; anonymous Phone access is denied. | Keep external health and capacity monitoring green, then prove useful fresh-runner restores for each integration actually in use. |
| Phone Xcode integration | Draft [LayerCache #20](https://github.com/ziahamza/layercache/pull/20), linked to [Phone #218](https://github.com/ziahamza/phone-app/pull/218). Real Xcode replay after deleting native CAS passed. The Phone CI chat owns both PRs. | A closed producer store restored into a separate consumer store on a fresh Mac, empty consumer native CAS and DerivedData, real Clang/module action hits and CAS loads, correct Phone output, and cold/warm timing. |
| Cloud authentication and acceptance | [#16](https://github.com/ziahamza/layercache/pull/16), [#17](https://github.com/ziahamza/layercache/pull/17), [#18](https://github.com/ziahamza/layercache/pull/18), and [#19](https://github.com/ziahamza/layercache/pull/19) have passed combined local validation and independent review. | Land each on current main with the required hosted checks. Publish and verify a later immutable CLI beta containing the fixes. |
| Public self-serve cloud | Draft [config #27](https://github.com/ziahamza/config/pull/27) prepares deployment. No service, dedicated pool, protected cloud configuration, or new public route has been activated. | Real GitHub OAuth, an approved creator cohort, isolated bounded storage, and live two-user acceptance before declaring a hosted beta. |
| Machine enrollment and hosted runners | Deferred. | Useful cross-machine cache reuse and operating costs measured first. |

This chat owns LayerCache integration, PR disposition, deployment evidence, and
this plan. The Phone CI chat owns the active Xcode adapter and Phone workflow
changes. Keep those branches separate while implementation is underway.

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
a 5 GiB Team Cache quota and currently reports zero committed artifacts.
Availability is established. These checks do not establish current reuse or
build-cost savings.

The former GitHub account billing lock no longer prevents jobs from executing.
All four cloud PRs were revalidated together at integration commit
`3d80c178eac3d1423a8ed8b016b2e8a88cf8e613`. Full Go tests, race tests, vet,
JavaScript checks and builds, workflow/shell lint, three PostgreSQL 16 store
race runs, and 78 browser checks across Chromium, Firefox, and WebKit passed.
Independent review found one interruption-cleanup issue in the new Turbo
acceptance harness, which must be fixed before #18 lands.

The real CLI/Turbo acceptance test produced a cold miss and a second user's
remote hit from a fresh clone and empty Local Cache after the producer daemon
stopped. It executed the task once, transferred 261 bytes each way, restored
matching output, and denied the removed reader's previously issued capability.
GitHub and the cloud origin were emulated. This is development acceptance, not
evidence of a live public cloud or customer-machine reuse.

## Priorities and landing gates

1. **Finish Phone compiler reuse.** The corrected
   [Xcode proof at `0edcbaa`](https://github.com/ziahamza/layercache/actions/runs/38152661755)
   deletes native CAS before both builds and uses fresh DerivedData. It recorded
   69 warm action hits and 70 CAS-object hits. This proves protocol replay on
   one Mac after local compiler-cache deletion; a restored snapshot on another
   Mac and actual Phone compile timings remain acceptance gates. The newer
   `f5ebe18` also waits for gRPC handlers before closing storage and tests an
   uncached retry for backend failures. Reject simultaneous ownership of one
   adapter data directory before opening its artifact store. Measure Phone
   compile reuse before enabling the adapter in signed release workflows or
   claiming savings. Local snapshots alone are not a remotely hosted compiler
   service.
2. **Land the reviewed cloud fixes and distribute them.** Keep OAuth fixtures
   confined to QA. Require current-main hosted checks for #16-19 and native
   installer verification for the next immutable beta. Do not bypass branch or
   release protections. None of these four PRs is obsolete.
3. **Activate one limited cloud beta after its live inputs exist.** Configure
   the real OAuth app with device flow and callback
   `https://cloud.services.ziahamza.com/auth/callback`, protected secret files,
   approved numeric creator IDs, a separate storage pool, and restart/backup
   recovery. Keep config #27 draft until its reviewed release and real inputs
   are ready. From two real identities, verify invitation acceptance, a cold
   upload, a fresh-machine Team Cache restore, role downgrade/removal, and
   recovery after restart through the public HTTPS origin.
4. **Make successful onboarding repeatable.** Provide copyable CI setup pinned
   to a verified release. Show measured source, bytes, hit status, transfer
   overhead, and timing in the dashboard. Keep misses non-blocking. Resolve
   repository renames against the stored immutable numeric GitHub repository
   identity. Qualify Parle's native Safari reuse separately when Phone's cache
   path is working.
5. **Expand after useful reuse is measured.** Add revocable machine identities,
   enrollment, last-seen state, and usage reporting. Then consider runner
   admission, leases, cancellation, isolation, cleanup, and usage accounting.
   Public Builds retain their separate publisher trust model and native KVM
   qualification. Windows, Actions cache v2, and a wider adapter SDK remain
   later work.

The beta stays limited to an explicit cohort and operator budget. Admission is
bounded in code, and managed projects bind to signed numeric repository IDs.
Before unrestricted signup, add billing/quotas, account and project lifecycle,
and support escalation. The published
[v0.1.0-beta.1](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.1)
remains an immutable, attested CLI release with verified native installation;
it does not make the hosted service live. Public Builds remain unqualified.
