# Hosted project dashboard qualification

Date: 2026-10-20 UTC. Implementation source:
[`e951ad0`](https://github.com/ziahamza/layercache/commit/e951ad0).
This receipt qualifies source behavior. Hosted deployment and authenticated
application acceptance are separate checks.

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

- All 192 hosted browser checks passed across Chromium, Firefox and WebKit,
  including direct links/history, encoded IDs, selective requests, stale
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
