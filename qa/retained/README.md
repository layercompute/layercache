# Retained transport qualification

`go test -race ./qa/retained -run TestActualRetainedServerAndClient -count=1 -v`
uses the production server, PostgreSQL persistence, the existing MinIO SDK, a
real S3-compatible backend and `action/retained/client.mjs`. It streams a
124,078,900-byte synthetic opaque file, then checks a fresh consumer after a
complete server restart and a consumer-only later run attempt.

Supply disposable loopback endpoints only:

- `LAYER_CACHE_QA_POSTGRES_URL`
- `LAYER_CACHE_QA_S3_ENDPOINT` (HTTP loopback)
- `LAYER_CACHE_QA_S3_ACCESS_KEY` and `LAYER_CACHE_QA_S3_SECRET_KEY`
- optional `LAYER_CACHE_QA_S3_REGION` (default `us-east-1`)
- optional `LAYER_CACHE_QA_RETAINED_REPORT` (owned output file)

The test creates a unique PostgreSQL schema and MinIO bucket and deletes only
those fixtures. Give the disposable object backend at least 1 GiB of available
scratch capacity; staging, copy-on-publish and asynchronous backend deletion
can overlap. Recreate a bounded disposable backend before repeating a large
qualification when its own cleanup has not reclaimed space yet. `TMPDIR=/dev/shm` keeps owned source/consumer files off the root
filesystem when sufficient tmpfs capacity exists. Node 24 or newer is required
for the direct client's native TypeScript utility import.

Capabilities are minted locally with the real signing code. This deliberately
bypasses GitHub's issuer and capability exchange; signed OIDC and same-repository
policy have separate adapter tests. The qualifier provides local source evidence,
not a hosted workflow, deployed service or transfer performance claim. Reported
body intervals include hashing, local disk and backpressure and are not bandwidth.
The test verifies immutable identity, SQL claim/pin/accounting, read-only and
foreign-run rejection, admission before an oversized body, exact-retry
reconciliation after a deliberately lost committed HTTP response, corruption failure and removal of a stale local destination.
