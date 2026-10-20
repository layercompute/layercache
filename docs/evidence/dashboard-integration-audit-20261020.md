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
  unknown. No execution duration or hit is invented for an omitted task.
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

## Adoption boundaries

The audit traced five configured projects from local scripts and hooks through
PR, main and native jobs. Cloud-only CLI configuration does not intercept bare
package-manager commands. Some CI jobs use another provider or GitHub snapshots;
a successful cached task does not identify LayerCache without matching native
and server evidence. A developer shell also used a different Node patch version
than its configured CI profile, requiring explicit pinned-toolchain launch.

The operator now has an explicit local launcher that validates project, tools
and compatibility before delegating to the existing CLI. Planning and health
checks passed for all five projects. No build or publication was run to qualify
that launcher, and application runtime-helper rollout remains separate.

Native Gradle, Expo, Xcode and provider caches need their own selected-toolchain
profiles and credentials. The Expo provider's local archive/extraction settings
do not inherit the Go CLI's cloud-only option. Native usage metering, tenant
costs, actual local-to-CI task reuse and normal application producer/independent
consumer acceptance remain in [TODOs](../../TODOs.md). Required transfers keep
their GitHub artifact fallback until retained-service acceptance qualifies.

Source tests, installed clients, live deployment and application adoption are
separate outcomes. No monthly saving or universal integration claim follows
from this audit.
