# Actions cache acknowledgement and misses

The Actions v1 endpoint is an evictable cache. A successful
`POST /_apis/artifactcache/caches/{id}` returns HTTP 204 after publication. A
`GET /_apis/artifactcache/cache` returns HTTP 204 when no available match remains.
The same status has different meanings on those two operations.

In PostgreSQL/S3 persistence, commit verifies the assembled archive and publishes
the Actions index and generic artifact reference in one transaction. It does
not pin the entry for a later consumer. Quota pressure, idle expiry and explicit
deletion can remove acknowledged entries. Reader leases protect an active
download; the signed archive URL's lifetime does not reserve future availability.
Explicit artifact pins have a separate lifecycle and prevent admission from
evicting their entries. Required build outputs, APKs and receipts should use
retained artifact storage with an exact producer/run identity and full digest
verification. See [retention](retention.md).

## Lookup diagnostics

HTTP 204 lookup responses include `X-LayerCache-Miss-Reason`. The header contains
one fixed value and never backend errors, credentials, keys or artifact bytes.
Existing v1 clients may ignore it; status codes and lookup ordering are unchanged.

| Value | Observation from this lookup |
| --- | --- |
| `no-matching-entry` | The PostgreSQL Actions index has no available match for the authorized project, repository, profile, ref, version and ordered keys. |
| `entry-removed` | A selected PostgreSQL candidate lost its underlying cache entry before availability verification. |
| `integrity-rejected` | A PostgreSQL candidate failed full archive or identity verification, including a missing S3 object. |
| `remote-unavailable` | A Local Cache hierarchy degraded an upstream failure to a miss. |
| `not-found` | The adapter returned a miss without a more specific observation. |

These observations are not a durable removal history. Quota eviction, expiry
and deletion cascade through the index; a subsequent lookup cannot distinguish
them from a never-published or differently scoped entry. A later lookup after
integrity invalidation may also report `no-matching-entry`. A healthy later
candidate still wins over a rejected candidate. Remote headers are not relayed
through the Local Cache hierarchy by this change.

## Isolated qualification

[The integration tests](../internal/cloud/actions_http_integration_test.go) use
real PostgreSQL 16, the production S3 SDK and an in-memory HTTP S3 fixture. Each
test gets a disposable database schema. They load no gateway configuration or
production credentials. The loop exercises real reserve, ranged PATCH, commit,
lookup and signed archive GET, then verifies the entire payload's byte count
and SHA-256 digest. It includes a varied 124,108,800-byte payload, not the private
Phone archive's contents.

Point `LAYER_CACHE_QA_POSTGRES_URL` at a disposable PostgreSQL 16 database. Run:

```bash
TMPDIR=/dev/shm mise exec -- go test ./internal/cloud \
  -run 'TestActions' -count=1 -timeout=120s -v
TMPDIR=/dev/shm mise exec -- go test -race ./internal/cloud ./internal/actionscache \
  -run 'TestActions|TestLookupMissReason' -count=1 -timeout=180s
```

The existing Linux x64 CI PostgreSQL service runs these tests. Without the
explicit QA database URL, PostgreSQL integration tests skip. Static fixture
scope tests cover project, repository, profile, ref, key and version isolation;
gateway authentication remains covered by its separate server tests.

The controls distinguish quota/expiry/deletion misses, missing or corrupt
objects, scoped misses, a failed pre-publication copy, loss of a successful
commit response, healthy fallback, explicit pins and concurrent maintenance.
They establish observable cache behavior, rather than identifying a historical
failure. In the October 14 diagnosis, the deployed `16868d9` source and current
`768e002` baseline both restored the exact-size payload immediately after
acknowledgement. The reported Phone upload-to-miss cause in
[issue #34](https://github.com/ziahamza/layercache/issues/34) remains unproven.
This qualification does not change the deployed gateway.

The [Phone owner's separate client regressions](https://github.com/ziahamza/layercache/issues/34#issuecomment-6168779142)
show that its `54e03cca` helper could publish a trusted producer reference after
commit 204 without verifying stored bytes, and could DELETE after an unknown
commit acknowledgement. Phone's `caf863ec` fixes and tests address those client
defects. They do not establish the original backend cause. Phone owns the
retained-artifact integration and its hosted qualification; this adapter adds
no required-handoff retention protocol.
