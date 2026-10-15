# Phone test results across machines

The October 14 reviewed checkpoint for [Phone #298](https://github.com/ziahamza/phone-app/pull/298) source
`1f97be8b753194fd58b13f25550c78721e92e96e` exposes ten independent mobile
test groups through native Turbo 2.10.12. All thirty existing isolated workflow
processes remain. The required aggregate includes both mobile and Core
typechecking; CI keeps its existing command and two-task concurrency limit.

Real Phone tasks now have a local-producer/fresh-consumer qualification through
the existing private Team gateway and installed immutable beta.4 CLI. The
[sanitized evidence](evidence/phone-test-groups-20261014.json) records native
hashes, execution and cache status, direct LayerCache attribution, restored
output digests and controls. It excludes protected configuration and raw logs.

| Stage | Native task execution | Native stage time |
| --- | ---: | ---: |
| Producer | Ten test groups execute; two prerequisite typechecks already cached | 4m10.355s |
| Independent empty consumer | Twelve remote hits; zero execution | 965ms |

The producer confirmed `Team Cache ready: 12/12 successful tasks`. The consumer
started with no native result cache, native evidence or Local entries, used
read-only remote access, and restored the exact twelve hashes. LayerCache
attributed all twelve hits to Team Cache, with 396,756 downloaded bytes and no
degraded result. Ten restored native evidence files match the producer's byte
counts and SHA-256 digests. The evidence records separate producer and consumer
values and the passed comparison for every file. The producer's two existing workspace typecheck hits
remain unattributed in its report and are not counted as remote hits.

Both machines were separate checkouts and Local daemons on this Ubuntu 24.04
x64 host. They matched Node 24.21.0, pnpm 10.7.1, Turbo 2.10.12, Vitest 5.0.4
and CI's exact compatibility identity. They authenticated normally through
GitHub CLI. The proof changed no persistent host cache profile, gateway
deployment or release routing. This is the compatible fresh local consumer checkpoint. Hosted Check now also
passes as described below; neither checkpoint is a monthly cost measurement.

Two intentionally failing full mobile aggregates each executed the failure and
returned exit 1. A counter outside the cached workspace advanced from one to
two; the same failed auth hash remained a miss. The other groups stayed reusable.
After restoring the source, explicit `--cache=local:,remote:` auth execution ran
both workflow processes and passed with no cached task. A cached result never
replaces the required checks or establishes a commit-level pass attestation.

Native hash controls verify independent unit/auth/numbers test edits, shared
Core/server/fixture/helper changes, public Expo environment and layout variants.
The first qualification caught a unit test's imported SQLite helper outside its
declared inputs and generated Turbo logs in a broad external package glob.
Final declarations keep helpers and actual runtime/configuration inputs, exclude
generated logs and have regression controls for both. Source closure remains
conservative; shared application changes can invalidate every mobile group.

Both independent Standards and Spec reviews passed. Full workspace checks passed
all 28 tasks and 36 focused cache/CI contracts passed. [Hosted Check 38595394031](https://github.com/ziahamza/phone-app/actions/runs/38595394031)
passes on PR merge tree `bd9a066ecfb710d51ee2362d027761288d73588c`, combining the
reviewed head with main `041f648f`. Its unit job restores the exact twelve
published hashes. The authenticated gateway report attributes twelve hits across
27 eligible tasks, with fifteen misses; all ten mobile groups reuse the local
producer's results. The existing PR lane uses read-only remote access. The
[hosted receipt](evidence/phone-hosted-test-groups-20261014.json) records the
actual source and timings: 3m20.413s in Turbo and 8m28s for the complete unit job.
Setup takes 2m22s, native tooling contracts 1m03s, resource checks 36s and post-job
work 54s. Those are observed job spans, not billing receipts or a controlled
before/after comparison. At that October 14 checkpoint, Android/iOS native
acceptance remained pending. Android compiled, but runtime QA stopped before
sign-in when the emulator
launcher repeatedly hung. iOS reached post-build preparation, where strict
entitlements parsing rejected trailing XML; its linker log shows two entitlements
inputs. Those native harness and staging files were unchanged by that October 14
candidate. Both failures were handed off without relaxing native guards. Keep local task, hosted job and monthly cost measures separate.

At that checkpoint the Phone CI owner kept signed-provider trials, setup and
required retained-artifact transfers in [#295](https://github.com/ziahamza/phone-app/pull/295).
[TODOs](../TODOs.md) owns the remaining acceptance and priorities. Repeated
setup and deterministic tooling checks remain the next measured cost target.

## October 15 installed Local service and updated Check

Reviewed Phone head `b7279389b24ca8f6d813451910dce8f2b50efd20` incorporates
main `d194ea69` and repairs the simulator entitlement producer. The native
staging bootstrap selects the complete generated plist through simulator-only
`CODE_SIGN_ENTITLEMENTS`; Xcode embeds it once. Device settings, ordinary linker
flags and strict postbuild verification remain. Both independent review axes
pass, along with all 28 local workspace tasks, 39 Python worker tests and 78
focused provider/cache/CI contracts. Real Ruby 3.4.5/Xcodeproj 1.27.0 controls
fail six of 43 baseline checks and pass all 43 for the candidate and fallback
team identity. Actual native runtime acceptance is recorded below, separately from those local controls.

The existing installed Phone Local service uses the same qualified CI profile,
a 1 GiB budget and LRU. Its [current-source receipt](evidence/phone-persistent-local-reuse-20261015.json)
records a real full check through that service, followed by two local replays.
The first pass confirms all 28 results in Team Cache and attributes sixteen hits
to Team Cache. The first local replay restores 25 results in 13.714s; three root
analysis keys change because restored generated native evidence enters their
broad input globs. The stable replay restores all 28 from Local Cache with zero
executions in an 807ms native stage, again confirming 28/28 Team results.
Configurations, service budgets and the installed immutable CLI remain unchanged.
These are native stage timings, not total CLI wall time or billing receipts.

Use the existing authenticated Phone profile to populate Local and confirm
availability to CI:

```bash
layercache run --config "$HOME/.config/layercache/phone-app.json" --sync-team -- \
  mise exec -- pnpm check --concurrency=2 --summarize
```

[Updated hosted Check](https://github.com/ziahamza/phone-app/actions/runs/38712658472)
also passes on merge tree `9407e02c5590a2b3e9264bbbef5389a8a7ed1790`.
Its [allowlisted receipt](evidence/phone-hosted-test-groups-20261015.json) records
all twelve historical task hashes as native hits, sixteen cached tasks out of
27, 3m38.324s in Turbo and 7m13s for the whole unit job. The gateway attributes
eleven hits across 22 eligible tasks; five native hits come from the runner
snapshot. Per-task remote attribution is unavailable in this log, so the receipt
does not assign a Team source to each of the twelve matching hashes. Setup takes
1m18s, native tooling 1m19s, resource checks 37s and post-job work 9s. These two
hosted observations do not establish a monthly saving or controlled speedup.

## Landing and native acceptance

[Phone #298](https://github.com/ziahamza/phone-app/pull/298) merged on October 15
as `3bae98fefadda18a21a5353a726a4567f00084fb` after both independent reviews and
all selected gates passed on head `b7279389`. [Native acceptance evidence](evidence/phone-native-acceptance-20261015.json)
records successful Android compilation and actual Contacts review/cancel/save/
readback on a fresh VM, with the repeated-launcher-ANR refusal preserved.
[RunCloud iOS acceptance](https://github.com/ziahamza/phone-app/actions/runs/38713331278)
also passes on the exact checked merge. Its retained linker command has one
Xcode-owned XML entitlement section input; packaging selects the complete generated
plist. Three embedded checks and two post-sign checks pass through the unchanged
strict verifier. Coverage is standard startup and CallKit; full signed-in iOS
Contacts, physical calling and optional audio coverage remain separate.

The original iOS failure and Android launcher incident above are historical
attempts. Phone #298's native acceptance is complete on its reviewed merge tree. Keep production
release routing with the Phone CI owner and use the canonical TODOs for remaining
setup, validation-reuse and application integrations.

The three generated-evidence analysis misses are corrected in
[Phone #309](https://github.com/ziahamza/phone-app/pull/309), reviewed source
`913e03fe582263192b2ab762e74f2b9d768309a9`, merged as
`716c8442d1a3ae872791d29b088074f29dd96d87`. The input lists exclude only the
declared generated native evidence directory. A real Turbo fixture rejects the
old behavior, preserves hashes when generated evidence appears or changes, and
still invalidates for real source and each analyzer's configuration. Both reviews,
all 28 installed-Local workspace tasks and hosted Check pass. Native and workflow
source are unchanged by this follow-up.
