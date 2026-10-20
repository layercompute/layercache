# Operator cloud migration and retirement, October 20, 2026

The existing five-project operator cohort now uses R2-backed artifact storage,
an authenticated hosted dashboard and cloud-only CLI connections. The legacy
cache filesystem has been retired after verified encrypted recovery.

Operational identities, credentials, paths and raw receipts remain private.

## Exact source

| Component | Qualified source |
| --- | --- |
| Deployed backend | [`95c418b92e2f41076d97c975972b71bc706cc206`](https://github.com/ziahamza/layercache/commit/95c418b92e2f41076d97c975972b71bc706cc206) |
| Installed cloud-only CLI | [`3da910fc6d9486517230b63bd96e63db81dec2dc`](https://github.com/ziahamza/layercache/commit/3da910fc6d9486517230b63bd96e63db81dec2dc) |

Both revisions are included in [PR #68](https://github.com/ziahamza/layercache/pull/68),
which merged with all seventeen source checks green. The installed CLI includes
client changes after the deployed backend revision.
These are the migration-time pins. The later
[dashboard and integration audit](dashboard-integration-audit-20261020.md)
records the subsequent reviewed backend/CLI update and live regression.

## Migration and live acceptance

- All 8,246 migrated objects passed complete size and SHA256 verification.
  Existing project identities, signing keys, native lookup metadata and
  membership records were preserved.
- All five adopted projects authenticated with real GitHub CLI credentials.
  Ten pre-migration artifact reads passed in total.
- A bounded synthetic publication and an operator-signed read-only fixture
  passed through separate HTTP sessions. Reader and scope-denial controls
  passed. The fixture represents the same human operator, not a second live
  GitHub identity or an OIDC-issued consumer.
- Authenticated reads passed after service restart and again after legacy
  storage retirement. The hosted dashboard lists the adopted projects and
  cloud-only commands use the configured remote backend.
- The physical metadata recovery copy uses the matching PostgreSQL major
  version and an explicitly verified schema. TCP password authentication
  succeeded, and the server rejected an incorrect password.

Live acceptance did not mutate membership to demonstrate revocation.
Separate PostgreSQL source tests verify that adoption and restart preserve
revoked native capabilities and that dashboard enrollment cannot override
native authority. Application OIDC and live two-user acceptance remain open.

These checks establish no build speedup, transfer improvement or monthly saving.

## Recovery and retirement

Full R2 download, authenticated decryption and every archived file's comparison
with the frozen manifest passed. The decryption identity and recovery records
remain protected outside the retired filesystem.

Retirement removed the obsolete containers, normally unmounted the exact old
filesystem and detached its backing device. Every active mount namespace was
clear before the exact sparse backing file was deleted. Source, credentials,
shared stores and durable evidence remain protected.

The removed backing file had 1,726,095,360 allocated bytes, about 1.6076 GiB.
This is allocated-file accounting, not nominal filesystem capacity or an
isolated measurement of the host's concurrent free-space change.

The [migration guide](../cloud-migration.md) records the reusable checks.

## Remaining gates

API processes, PostgreSQL and bounded verification staging still run on the
operator host. Remote compute, independent key recovery and ongoing off-host
SQL backup/restore qualification remain open. The legacy archive does not
protect later writes to the replacement service.

Required application handoffs continue using GitHub artifacts until their own
retained-service acceptance passes. Live second-identity and application OIDC
acceptance, public signup, billing and the immutable CLI release remain
separate gates in [TODOs.md](../../TODOs.md).
