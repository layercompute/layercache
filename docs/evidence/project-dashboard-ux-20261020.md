# Hosted project dashboard qualification

Date: 2026-10-20 UTC. Implementation source:
[`52944c0`](https://github.com/ziahamza/layercache/commit/52944c0).
Source behavior and the subsequent authenticated hosted rollout are recorded
separately below.

The hosted portal now has focused project pages and stable direct links.
Overview places cache bytes, hit rate, timing coverage and measurement runs
first. Changing its period requests only the selected project and preserves
navigation, focus and scroll. Settings and account pages hide activity controls.
CI jobs group by authenticated repository/run/attempt. Default client rows need
repeated distinct executions and a fresh sample; other observations are collapsed.

Stored-artifact categories partition explicit SQL integration metadata. Shared
blobs count once, including during concurrent updates. Unknown purposes remain
unknown; Turbo-protocol archives include Xcode/Expo snapshots. Registry and
separate native engine storage remain outside these totals.

Common artifact reports use opaque references and bounded lists. Timing-backed
task and build estimates retain coverage and confidence, including negative
estimates. Partial native hits cannot claim an entire producer's avoided work.
CPU-hours and financial savings remain unmeasured.

Capacity changes use current project/native authority, compare the previous
limit and stay within the assigned allocation. SQL transactions protect
committed bytes, retained obligations and metadata. Changes never evict entries
or replace plan allocations. Uncertain responses require reconciliation.

Validation:

- All 201 hosted browser checks passed across Chromium, Firefox and WebKit,
  including account switching, shrinking reports, direct links/history, encoded IDs, selective requests, stale
  responses, scroll/focus, recurrence, hidden controls and capacity reconciliation.
- The unchanged local/shared views also passed 273 checks across those browsers.
- Type checking, generated bundle builds, the full Go suite, vet and repository
  lint passed. All 103 Node tests passed, including both installed-server controls.
- Affected storage/reporting/portal/server packages passed PostgreSQL race
  suites. Focused recurrence controls passed on SQLite and real PostgreSQL.
- Independent Standards review's hidden-control finding was corrected and
  regression-tested. Final Standards and Spec reviews had no remaining findings.

No application CI workflows, native builds, provider dispatches, live pruning or
customer capacity changes were used for source qualification. Synthetic browser
values are presentation fixtures, not application savings measurements.

Follow-up review also qualified zero-duration backtest bucket totals while
preserving ordinary half-open report selection. Top-artifact selection retains
only twenty heap candidates; exact frequency aggregation still scales with
unique artifacts and artifact/run pairs. A persistent SQLite covering index
replaces repeated automatic digest-index construction. Query-plan controls and
synthetic metadata-only benchmarks qualify that lookup, not application speedup.

## Hosted rollout

[PR #79](https://github.com/ziahamza/layercache/pull/79) merged on October 20 UTC
after all eighteen PR checks passed. The merged source
[`c6d120db672fc81b5d5a81faa6cb6c539c075962`](https://github.com/ziahamza/layercache/commit/c6d120db672fc81b5d5a81faa6cb6c539c075962)
has the same tree as the qualified PR head and is deployed. The served dashboard
bundle matches that image's build output.

All 39 authenticated live checks passed, thirteen each in Chromium, Firefox and
WebKit. These checks used normal sign-in and the active project's actual API
responses. They covered the direct project URL and reload, top summary/API
agreement, period changes with retained navigation, focus and scroll, CI run
rollups, recurring client selection, stored-byte category totals, capacity bounds,
project and account settings, and mobile navigation. There were no browser
exceptions. Each browser signed out its own session.

The rollout changed only the cloud service container. Before/after controls
verified that existing account and project authority, session state, snapshot
grants, protected configuration and other containers were preserved. Live QA
read capacity without changing limits or pruning data. It did not rotate a
password, dispatch application CI or start native/provider builds. Mutation,
quota-pressure and restart behavior remain qualified by the source controls above.

A post-merge Chromium check exposed an asynchronous membership assertion. The
test searched rendered page text for a lowercase role before member rows loaded;
the visible heading uses uppercase styling. A controlled held membership response
reproduced the failure. The corrected check waits for the actual member row and
its exact reader role before checking that administrator controls are absent.
Existing API permission-denial checks remain in place.
All 201 hosted-fixture checks passed again across Chromium, Firefox and WebKit
with that controlled-response regression.

Live UI/API agreement does not establish complete native measurement. Reported
Turbo outcomes do not cover all Gradle or Xcode reuse, and a shared archive
transport does not identify its native purpose. Those reporting gaps and matched
producer resource/pricing data remain separate TODOs. This rollout makes no
compute, invoice or application speedup claim.
