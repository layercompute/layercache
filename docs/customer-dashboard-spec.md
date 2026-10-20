# Customer dashboard

The customer can inspect and manage configured project caches from an account-free
CLI or an authenticated hosted browser. Reuse the existing dashboard and native
measurement repositories. This specification owns the current implementation
scope; TODOs.md owns future work.

## Accepted behavior

- `layercache dashboard --config project.json` keeps its existing loopback mode.
  `--serve` also runs the selected Local backend for that process, with owned
  shutdown. Existing runtimes must not be replaced or stopped. `--backend URL`
  selects the remote connection already present in the configuration, without
  sending credentials to a different origin. The browser never supplies an
  upstream endpoint or configuration path.
  A configured remote with no initial capability must authenticate before owned
  backend construction, or fail before opening a backend. This prevents a later
  dashboard login from silently leaving the owned cache Local-only. Pure Local
  configurations continue to work offline without a cloud account.
- Local and hosted views share cache rendering, definitions and empty states.
  Hosted navigation and session authentication remain in the existing portal.
  The hosted project Overview places storage, hit rate and timing coverage first.
  Activity, Infrastructure, Storage and Settings have direct, reloadable paths.
  Period changes update the selected project without replacing its navigation,
  losing focus or collapsing scroll. An unavailable update labels previous data;
  expiry removes private content. Back/forward and encoded project IDs work.
- Show effective hit rate as hits divided by eligible reported outcomes, observed
  runs, transfer bytes and phase durations, source attribution, integration
  breakdown and net estimated time saved with evidence coverage. No outcomes
  means unknown rate, not zero. Reports from different runtimes may overlap and
  are never added into a claimed global savings total.
- Show Personal, Team and Public access separately from Local/remote placement.
  Hosted membership supplies Personal/Team identity. Standalone remote ownership
  may be unknown. Public means trusted-publisher Public Cache. Local source
  storage attribution is unavailable and must not be invented.
- Show logical artifact storage, limit, count, entries, eviction policy and
  backend. These values exclude host disk, registry storage and separate native
  engine stores, including configured Bazel engines. Current reports cover reconciled Turbo, Actions and BuildKit outcomes;
  other native protocols need separate metering.
- Partition artifact bytes using explicit integration metadata, counting shared
  blobs once. Turbo-transport snapshots cannot be relabeled as Xcode or Expo.
  Keep missing storage breakdowns separate from an explicitly empty store.
- Group CI jobs by authenticated repository/run/attempt identity with expandable
  detail. Persistent clients are separate from CI jobs. Default host rows require
  at least two distinct reported executions and a fresh resource sample. Keep
  one-off, stale and unknown-count clients collapsed. Deduplicate start/finish
  phases and count only timestamped primary observations in the selected period.
  These bounded observations do not establish unique physical hardware.
- Capacity changes require current administrator authority and a matching prior
  limit. Stay within the assigned allocation, reject insufficient committed or
  retained/metadata capacity and preserve restart persistence. Changes never
  evict entries or replace plan allocation. An uncertain write requires a fresh
  read before another attempt. Ordinary native managed-quota denial remains.
- Local maintenance is opt-in with `--allow-prune`; sharing a default read-only
  dashboard link must not confer administration. Hosted pruning requires current
  administrator membership. Require an explicit target and confirmation, bounded
  JSON and same-origin protections. Reuse `/v1/gc`; preserve pins, retained claims
  and active reads. Public maintenance is not exposed to customers. Refresh after
  success or failure without automatically retrying an uncertain mutation.
- A configured, bounded cost ledger may provide project or team period totals
  and line items. Keep currency, period, basis and coverage visible. Incomplete
  line items produce a known subtotal and an unknown total. Operator records and
  estimates are not provider-verified invoices. No ledger means unknown cost.
  Never sum overlapping Local/remote observations or convert estimated avoided
  time into actual money saved.
- Existing authenticated portal sessions and fresh membership checks protect all
  hosted reads and writes. Browser projections exclude runtime credentials,
  configuration paths, provider identifiers and internal storage endpoints.

## Qualification

Exercise real isolated Local backends, remote origin binding, backend lifecycle,
source attribution, missing/degraded evidence, ledger arithmetic/limits, reader
and writer prune denial, administrator pruning, retained/pinned preservation,
low-headroom pruning and unauthorized/foreign selectors. Browser checks cover
desktop/mobile layout, confirmation/cancellation, errors and fresh snapshots.
No live customer pruning, application workflows or production service changes
are part of qualification.

## Further work

Complete native transport and task metering, time-integrated storage accounting,
provider costs, shared overhead allocation, invoice reconciliation and historical
usage charts remain separate milestones. A current status snapshot is not a
historical byte-hour meter. This dashboard does not launch signup or billing.
