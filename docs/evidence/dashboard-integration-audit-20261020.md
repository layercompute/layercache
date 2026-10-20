# Live dashboard and command adoption audit

Checked October 20, 2026. Operational identities, credentials, database rows,
screenshots and raw workflow logs remain in protected operator records.

## Live account qualification

The operator completed the normal initial password-change flow once. The
protected login handoff was updated, the prior password was rejected and the
initial-change flag cleared. Browser sessions were signed out after testing.
No cache pruning, membership changes or application builds were needed.

Chromium and WebKit each passed 43 live scenarios. Firefox passed 42 of 43
initially; its final fresh login reached the expected account rate limit.
Separate login, logout, revoked-session and fresh-login controls passed after
natural expiry. This is not a claim that the initial Firefox run passed.

All 45 live browser/API projections across five projects in one Team matched
read-only SQL accounting for stored bytes, artifact and entry counts, logical
metadata and quota. Navigation, 24-hour/seven-day periods, refresh, responsive
layouts, canonical CLI connection instructions and sign-out passed. Maintenance
dialogs were opened and canceled without a mutation. Costs correctly remained
unknown because no ledger was configured. These controls establish neither
numeric provider costs nor independent cross-tier authorization.

## Reproduced reporting defects

- A real native Turbo summary contained 3,342,087 bytes, mostly repeated input
  maps. The action rejected it at its former 2 MiB raw-file limit, before
  authentication. Its allowlisted projection is only 7,910 bytes. The collector
  now bounds raw local input at 64 MiB per batch while retaining the server's
  exact 8 MiB JSON request limit, 32 summaries and 4,096 total tasks. Input maps,
  commands and environment metadata stay on the runner.
- Another real Turbo summary named transit dependencies that it omitted from
  its task list. The reconciler rejected the whole summary. Reachable omitted
  dependencies now become private unknown graph boundaries. Actual eligible
  task outcomes remain reportable; incomplete critical-path savings stay
  unknown. No execution duration or hit is invented for an omitted task. Their
  bookkeeping timestamps must not precede the first observed task and move its
  run outside the selected reporting window. Cross-window controls cover this
  external-review finding in SQLite and PostgreSQL.
- The Actions cache chain called its selected hosted Team backend Local Cache.
  The reporting callback now attributes new Team restores to Team Cache.
  Historical reports and the chain's Public trust and warming policy remain
  unchanged. Actual Team Turbo attribution was already correct and has a new
  request-to-report regression control.
- Per-source upload cells showed zero even when miss publications contributed
  nonzero total uploads. The shared renderer keeps total uploads and removes
  the unsupported source breakdown rather than inventing a destination.

Collector boundary/privacy tests, real summary reconciliation, SQLite and
PostgreSQL repeat/reopen checks, and complete native Actions HTTP publication
and restoration cover these fixes. Source attribution tests exercise embedded
Local, embedded Team and PostgreSQL/S3 Team backends. The existing browser
fixture passed 41 scenarios in each of Chromium, Firefox and WebKit, including
the nonzero miss-publication upload control.

## Deployed regression

[PR #70](https://github.com/ziahamza/layercache/pull/70) merged as
[`7b221c181b89100fa26b731e8eb32eeead289d21`](https://github.com/ziahamza/layercache/commit/7b221c181b89100fa26b731e8eb32eeead289d21)
after all eighteen PR checks passed. Its tree matches the reviewed and deployed
[`4ddb1387e16897767645fd2458133323d4bc7494`](https://github.com/ziahamza/layercache/commit/4ddb1387e16897767645fd2458133323d4bc7494)
source. All post-merge source checks passed too.

Fresh live Chromium, Firefox and WebKit runs each passed 44 scenarios. The
served JavaScript matched the reviewed bundle bytes. Forty-five live UI/API
metric projections passed, along with project navigation, both reporting
periods, desktop/mobile layouts, connection instructions, corrected upload
display, canceled maintenance and logout/revoked-session/fresh-login controls.
All test sessions ended signed out. The current password remained valid, the
old password remained invalid and the initial-change flag stayed cleared.

Forty-five reads of existing native artifacts and three signed Actions archive
reads passed across the canonical and two compatibility origins. Changed-key,
changed-profile and foreign-project controls passed without publication or
deletion. The same reviewed CLI was installed; all five clients passed doctor
and scoped authenticated-read controls, kept saved configuration unchanged and
created no Local artifact directories. The rollout preserved protected auth,
session and project files and replaced only the API container. API and SQL
hosting remain on the operator host; artifact bytes remain in R2.

An ordinary application CI export job resolved the new merged action and
submitted its final report through real OIDC. Turbo reported eleven successful
tasks, including three cached tasks; LayerCache correctly reported eight
eligible misses and zero attributed hits. The three workspace/snapshot hits
were excluded from Team credit. No reporter warning occurred. This qualifies
that reporting path without an extra dispatch or a savings claim.

## Adoption boundaries

The audit traced five configured projects from local scripts and hooks through
PR, main and native jobs. Cloud-only CLI configuration does not intercept bare
package-manager commands. Some CI jobs use another provider or GitHub snapshots;
a successful cached task does not identify LayerCache without matching native
and server evidence. A developer shell also used a different Node patch version
than its configured CI profile, requiring explicit pinned-toolchain launch.

The operator now has an explicit local launcher that validates project, tools
and compatibility before delegating to the existing CLI. Four projects derive
their profile from measured OS/libc/architecture, Node runtime, pnpm and Turbo
facts through the same algorithm as their CI integration. One retains its
existing qualified application profile. Protected temporary overlays avoid
flipping shared configuration across concurrent workspaces. Planning and health
checks passed for all five projects; cancellation and cleanup controls use only
owned test process trees. No build or publication was run to qualify that
launcher, and application runtime-helper rollout remains separate.

Native Gradle, Expo, Xcode and provider caches need their own selected-toolchain
profiles and credentials. The Expo provider's local archive/extraction settings
do not inherit the Go CLI's cloud-only option. Native usage metering, tenant
costs, actual local-to-CI task reuse and normal application producer/independent
consumer acceptance remain in [TODOs](../../TODOs.md). Required transfers keep
their GitHub artifact fallback until retained-service acceptance qualifies.

Source tests, installed clients, live deployment and application adoption are
separate outcomes. No monthly saving or universal integration claim follows
from this audit.
