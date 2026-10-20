# Native metering qualification

Date: 2026-10-20 UTC. This change is based on
[`f49b92a`](https://github.com/ziahamza/layercache/commit/f49b92adf365b0a877f588f884d7e3b885b1fdfd).
This record covers source qualification. Deployed service, published client
versions and application adoption require their own receipts.

[Native measurement](../native-metering.md) defines the units, report authority,
limits and remaining coverage. Gradle uses its public completed-task listener
and qualified native info output. Xcode uses an offline terminal-diagnostic
exporter, with compiler verdicts separate from RPC counters and lifecycle phases.
The dashboard explains reporting support and missing evidence for each cache
type; the same native evidence is visible in the local dashboard.

Source controls passed:

- 35 Gradle action/parser/projector tests, including invalid acknowledgements
  and legitimate identical retries. Optional reporting failure preserves the
  build result and never retries native compilation.
- Seven real Gradle 9.3.1/JDK 17 observer stages, covering unchanged keys,
  multiple and included projects, parallel tasks, configuration-cache reuse,
  accepted restores, disabled caching, failures and shared native keys.
- All 22 real native HTTP acceptance commands, extended with authenticated
  native report submission and source/original-producer assertions. Restart
  preserves reports. A later equivalent archive and different producer duration
  cannot replace the original retained-byte provenance.
- 133 Node tests with the built Go server available, zero skips. Source and
  bundled offline exporters also passed their 15 focused command controls;
  their output passed the real Go decoder and clock-window validator.
- The full Go suite, vet and repository lint. All packages passed race testing;
  after transactional quota accounting changed, the full measurement package
  was rerun successfully on SQLite and real PostgreSQL. Focused native server,
  resource and authorization race controls also passed on both databases.
- 498 browser checks across Chromium, Firefox and WebKit on the first
  candidate. A final focused run passed 96 checks across those engines after
  exposing timing coverage directly beside estimates for mobile readers.
- Independent Standards and Spec reviews, with no remaining findings. Review
  corrected receipt validation, importer-observation coverage, uncertain 5xx
  reconciliation, scoped compiler ratios and compiler-limitation aggregation.

Follow-up PR review added an actual mixed Turbo/Xcode endpoint regression:
counter-only native evidence preserves the independent generic graph estimate,
and a forced native-store error returns available generic data with explicit
native unavailability. Bounded daily loss markers follow the affected workload
period and integration, including delayed uploads, disjoint periods, multiple
losses, restart and half-open boundaries. Transport losses intersect the full
selected invocation/task window with the existing five-second matching grace.
Controls include losses after the query end, losses received after the report,
task-window extensions and the 124-row project bound. These passed race testing
on SQLite and fresh PostgreSQL without database skips. The follow-up passed 22 projection
tests and 21 focused browser controls across all three engines. Generic graph
scope is visible beside its estimates. The earlier 498 and 96 browser checks
remain separate qualification records rather than additional distinct scenarios.

The database controls include immutable source/report conflicts, separate
matrix jobs, current membership removal, reader write denial, snapshot grant
denial, report-only Xcode credentials, bounded admission, expiry, restart,
concurrency, original resource capture and incomplete delivery. Native cache
acceptance alone establishes a hit; exact key and transport evidence are still
required to attribute its source and original producer timing.

A synthetic SQLite query-cost control used 32,768 documents totaling 32 MiB.
Across 31 warm samples, the previous admission scan had a median of 34.241 ms;
the replacement quota-counter lookup had a median of 0.01146 ms. Indexed
zero-expired-range lookup had a median of 0.01974 ms. These are local metadata
query observations, not HTTP, application or PostgreSQL speedup measurements.
Optional transport observation has a 100 ms budget and can leave provenance
unknown without failing cache execution.

The public source contains no operational credentials, private application
logs, raw task paths or machine inventory receipts. Known protected credential
values and added-content private-path/credential patterns were checked before
publication. Synthetic browser numbers are presentation fixtures.

No application workflow edits, application native/provider builds, live pruning
or customer capacity changes were used for this qualification. The fixtures do
not establish application hit rates, signed builds, critical-path savings,
measured CPU savings, monthly cost or invoice savings. Required application
handoffs continue to use their existing retained-transfer path.
