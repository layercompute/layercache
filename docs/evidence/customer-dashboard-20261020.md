# Customer dashboard qualification

Checked on October 20, 2026. The
[reviewed implementation](https://github.com/ziahamza/layercache/commit/3e13358f72845d4b9d9cce3038ed71371b3c8a59)
extends the existing CLI dashboard and authenticated cloud portal. Both use
the same cache, scope and cost renderer. This receipt describes source
qualification with isolated synthetic data. It is not a CLI release, hosted
deployment, customer pruning or billing acceptance.

## Local backend and authorization

- Default CLI dashboard links remain read-only. `--serve` starts only owned Local
  backends; conflicts and partial startup preserve existing runtimes. Shutdown
  releases owned listeners and runtime locks. Credentials refresh in memory
  without modifying project configuration.
- A configured remote with an empty initial capability authenticates before any
  backend is constructed. Actual Local HTTP restoration and publication use the
  fresh authority. Unavailable, malformed or missing first login initializes no
  stores and preserves existing listeners. Pure Local configurations stay
  account-free and offline. This follow-up closes an external review finding;
  the earlier browser qualification alone did not cover first-login bootstrap.
- `--backend` selects the configured remote origin without a Local daemon.
  Changed origins cannot receive previously issued or copied credentials.
  Default-mode live reload still obtains fresh authority for a new connection.
- Opt-in CLI and administrator-only hosted pruning require current authority,
  same-origin checks and explicit confirmed targets. Foreign projects, readers,
  writers, revoked members and malformed requests are denied.
- Actual fixture pruning removes eligible artifact bytes. Durable pins and
  active read leases survive blocked targets. Existing cloud GC supplies its
  retained-claim guarantees; this dashboard change does not add a new live
  retained-artifact qualification.
- Low-headroom gateway tests reject new growth while permitting authenticated
  administrative pruning. Removal is logical accounting, not measured physical
  disk reclamation or an immediate object-storage invoice reduction.

## Browser and cost controls

The [shared renderer and browser fixture source](https://github.com/ziahamza/layercache/commit/90a5a8b6cf52311c49af4dfcfe1c96c5601ffeda)
passed **222 browser checks** across Chromium, Firefox and
WebKit: 40 CLI checks and 34 hosted checks per engine. Checks include:

- Personal/Team authority, trusted Public source attribution, eligible-outcome
  hit rate and explicitly incomplete native coverage.
- Default read-only views, cancellation without a POST, actual isolated pruning,
  denied lower-role writes and refresh after an uncertain result without an
  automatic mutation retry.
- Separate project and account costs, complete operator records, partial
  estimates with unknown totals, and unavailable or absent ledgers.
- Exact rendering of the maximum supported minor-unit amount for USD, JPY and
  BHD, preserving two, zero and three decimal places respectively.
- Missing legacy allocation displayed as unavailable, rather than zero; remote
  selection omitting the unselected Local card.
- Desktop/mobile layouts, same-origin request URLs, session lifecycle, failed
  refresh recovery and no browser exceptions.

The cost reader also rejects malformed or oversized ledgers, duplicate scope
records, unsafe integers, subtotal overflow, missing amounts and invalid periods.
Independent Standards and Spec reviews have no remaining actionable findings.

## Other checks and limits

The affected server, portal, dashboard and CLI Go suites pass, including their
race checks. TypeScript checks, generated browser bundles, Go vet, workflow/shell
lint and whitespace checks pass. Local cache fixtures use an isolated temporary
filesystem with enough real headroom; admission policies are unchanged. Required
hosted CI remains visible on [PR #67](https://github.com/ziahamza/layercache/pull/67).
The first-login follow-up changes CLI startup and its Go fixtures; the renderer
and browser fixtures retain the same bytes as their 222-check qualification.

Reports currently cover reconciled Turbo, Actions and BuildKit outcomes. Native
Gradle, Nx, Bazel, Xcode and Expo reporting is incomplete. Artifact-store usage
and pruning exclude separate native engine stores, including configured Bazel
engines, registry storage and host disk. Local storage cannot be divided into
Personal/Team/Public bytes from current metadata. Runtime observations may
overlap and are not summed into global savings.

Costs are operator records or estimates, not provider-verified invoices.
Historical storage metering, provider reconciliation and usage charts remain
in [TODOs](../../TODOs.md). No application workflow, production cache, service
configuration, signup or paid-plan activation changed during qualification.
