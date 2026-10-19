# Hosted storage pools and capacity plans

LayerCache uses PostgreSQL for cache metadata and authority, and an
S3-compatible store for immutable artifact bytes. Managed hosting defaults to
Cloudflare R2. Hosting does not change Personal, Team or trusted-publisher
Public visibility. This source adds named storage pools and operator-assigned
capacity plans; it does not launch payment collection or unrestricted signup.

## Protected configuration

`serve-cloud` keeps its existing protected legacy project template and local
filesystem `storagePool` guard. Add named hosted pools through separate protected
project-template files:

```json
{
  "hostedStoragePools": {
    "r2-primary": {
      "projectTemplateFile": "/etc/layercache-cloud/pools/r2-primary.json"
    },
    "r2-secondary": {
      "projectTemplateFile": "/etc/layercache-cloud/pools/r2-secondary.json"
    }
  },
  "storagePlans": {
    "beta": {
      "poolId": "r2-primary",
      "projectMaxBytes": 5368709120,
      "teamMaxBytes": 10737418240,
      "maxProjects": 2
    },
    "larger": {
      "poolId": "r2-secondary",
      "projectMaxBytes": 21474836480,
      "teamMaxBytes": 107374182400,
      "maxProjects": 5
    }
  },
  "defaultStoragePlan": "beta",
  "teamCreatorStoragePlans": {
    "123456": "larger"
  }
}
```

These are illustrative capacities, not commercial offers. The numeric creator
ID is a placeholder. An override applies only when that authenticated GitHub
identity creates a new Team or Personal collection; it grants no signup or
project access. Requests cannot choose their own plan, pool or quota.

Each pool template reuses the existing private project configuration and
PostgreSQL/S3 fields. For R2, use its account or jurisdiction-specific HTTPS
endpoint, a private bucket, region `auto`, and bucket-scoped object read/write
credentials. Store credentials only in protected runtime files or an approved
secret facility. Configuration examples and public evidence contain no live
credentials. Use the existing setup secret-file flags rather than placing keys
in command arguments.
[R2 authentication](https://developers.cloudflare.com/r2/api/tokens/),
[S3 compatibility](https://developers.cloudflare.com/r2/api/s3/api/).

Hosted PostgreSQL connections require an explicit single-schema `search_path`,
such as `public`, with no startup-options override. This makes the metadata
namespace stable when credentials rotate. The persisted backend identity
includes the database namespace, S3 endpoint, bucket and region, while excluding
rotatable passwords and access keys.

## Capacity and lifecycle

A Team's plan is frozen at creation. Project provisioning reserves its complete
allocation against that Team's capacity in the same database transaction as the
project record. Concurrent creates cannot allocate more than the plan allows.
Personal collections retain owner-only authority and their own allocation.
The runtime applies the persisted per-project allocation to the cache quota.
Managed customers cannot raise that quota through the administrator endpoint;
standalone operator quota administration is unchanged.

An allocation bounds admitted project cache capacity. It is not current usage
or a physical R2 invoice ceiling. Upload staging, deletion grace, uncollected
objects, retained claims, metadata and local native outputs have their own
lifecycle and budgets. The local filesystem pool remains a separate physical
guard; an R2 pool does not replace it.

Existing Teams and projects retain an explicit legacy assignment. Restart
preserves each project's pool and allocation. Removing a referenced pool,
changing its backend, or changing an assigned plan fails closed. Credential
rotation within the same backend identity is supported by the configuration
model. Moving existing bytes, applying paid upgrades and shrinking allocations
require explicit migration and quota reconciliation; editing defaults does
none of those things. An older portal binary is not a qualified writer for
these managed entitlements.

## Qualification and launch

SQLite and PostgreSQL provisioning tests cover concurrent allocation, restart,
legacy assignments, invalid configuration and credential-safe identities.
Runtime tests use PostgreSQL 16 and two independent S3 fixtures to check exact
bytes, Personal/Team isolation, quota, read-only access and revocation. Actual
R2 SDK qualification and service deployment remain separate gates in
[TODOs.md](../TODOs.md).

Current cloud reads download and verify complete artifacts in bounded local
staging before returning success. R2 alone therefore establishes no faster-fetch
claim. Measure metadata lookup, S3 transfer, verification, response transfer and
complete command time independently. A direct-download optimization needs its
own authorization, read-retention and client-integrity contract.

Operator-provisioned private hosting can precede public signup. Real OAuth,
cohort access, recovery and two-user acceptance remain required before declaring
the self-serve cloud live. Required application handoffs continue using GitHub
artifacts until their retained-service adoption passes.
