# Execution inventory source qualification

This records source checks for the [execution inventory](../execution-observability.md).
It does not establish deployment, installed-client adoption or measured savings.
The later [rollout receipt](execution-rollout-20261020.md) records installed
clients and actual hosted acceptance separately.

The complete Go suite passed with disposable PostgreSQL and isolated client
configuration. Focused race checks and vet passed for the affected persistence,
authorization, CLI and collection modules. Controls exercise independent job
identities, read-only reporting, current membership removal, Local-only
standalone authority, old clients and daemons, concurrent schema initialization,
upserts, restart, history limits and exact cache-activity association.

Storage controls preserve an unpublished architecture-profile miss, then retain
two authorized profile references to one identical payload. Local storage
charges one blob. PostgreSQL plus the production S3 client against a disposable
S3 fixture stores one payload object. Removing one reference preserves the
other. This checks compatibility isolation and byte accounting, not portability
of an actual compiled application or cross-project cloud deduplication.

The shared renderer passed 150 fixture checks across Chromium, Firefox and
WebKit with both production stylesheets. Checks include cross-project identity
and volume deduplication, newest resource facts, retained-period activity,
unknown limits, stale samples, provider evidence, partial coverage and mobile
layout. Existing isolated Chromium dashboard and cloud controls also passed.
The new fixture runs within each existing Dashboard CI job for its selected
browser.

Review caught an interrupted identity commit, unbounded optional filesystem
probes and missing project-routing headers. Regression controls cover atomic
identity recovery after a killed writer, a blocked Go collector that cannot hold
the caller open, and a real active-handle Node probe whose parent must exit
within its deadline. Native requests retain their project and credential scope.
Reporting uses an allowlisted, bounded payload and never changes artifact keys.

Follow-up review controls preserve the latest resource sample when one native
action starts after another finishes in the same CI job. Native integration
credentials can submit their own observations, but cannot read project-wide
inventory. Generic human, portal and Local operator reads retain current
membership checks. The portal passed 105 isolated browser scenarios across
the three engines, including a two-project period-change regression.
The 30-day inventory window is separate from physical row deletion, which runs
when a project next accepts a report.

The first hosted macOS Go job failed during concurrent installation identity
creation with an error that did not distinguish lock opening from acquisition.
The follow-up handles interrupted syscalls and preserves the stage and errno
without private paths. A subsequent macOS run passed, but the first merged main
run then exposed `openat` returning `ENOENT` while opening the final lock file.
[PR #74](https://github.com/ziahamza/layercache/pull/74) bounds that final-leaf
retry on Darwin to three attempts through the same validated parent descriptor.
It never recreates a missing parent. Deterministic error/cancellation controls,
Linux race repetitions and Darwin/Windows cross-compilation passed. Both the
final PR and normal post-merge macOS jobs passed full Go tests, vet, the binary
build and 20 repetitions of the unchanged 24-worker concurrency test. This
qualifies the bounded correction without establishing the filesystem's
underlying cause or explaining the original uninstrumented failure.

Initial live browser acceptance passed 131 of 132 checks. WebKit exposed two
snapshot requests starting during navigation after earlier requests were
canceled. A delayed-response replay reproduced the failure. PR #74 stops the
remaining request queue on transport loss, discards obsolete response effects
and revalidates restored sessions before showing private content. Twenty-four
isolated lifecycle cases passed across Chromium, Firefox and WebKit, including
current session expiry, stale responses and simulated persisted-page handlers.
Those simulations do not establish actual browser back-forward cache
eligibility. The later live rollout passed all 132 original checks.

An older backend rejects newly issued execution-aware credentials after
rollback. Clients must exchange fresh credentials against the older version or
wait for expiry. The source checks do not establish transparent in-flight token
rollback. A bounded shared local hot cache, portable task contracts and
Apple-managed compiler-cache transport remain separate TODOs.
