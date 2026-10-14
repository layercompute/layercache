# Phone test results across machines

The reviewed [Phone #298](https://github.com/ziahamza/phone-app/pull/298) source
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
counts and SHA-256 digests. The producer's two existing workspace typecheck hits
remain unattributed in its report and are not counted as remote hits.

Both machines were separate checkouts and Local daemons on this Ubuntu 24.04
x64 host. They matched Node 24.21.0, pnpm 10.7.1, Turbo 2.10.12, Vitest 5.0.4
and CI's exact compatibility identity. They authenticated normally through
GitHub CLI. The proof changed no persistent host cache profile, gateway
deployment or release routing. This is a compatible fresh local consumer, not
yet a completed hosted CI acceptance or a monthly cost measurement.

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
all 28 tasks and 36 focused cache/CI contracts passed. Phone's normal hosted
gates are running in [Check 38595394031](https://github.com/ziahamza/phone-app/actions/runs/38595394031)
plus its required native QA workflows. Keep hosted attribution, whole-job timing
and runner allocation separate from the local native-stage times above.

The active Phone CI owner keeps signed-provider trials, setup and required
retained-artifact transfers in [#295](https://github.com/ziahamza/phone-app/pull/295).
[TODOs](../TODOs.md) owns the remaining acceptance and priorities. Repeated
setup and deterministic tooling checks remain the next measured cost target.
