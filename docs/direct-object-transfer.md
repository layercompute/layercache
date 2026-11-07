# Direct object transfer

Status: design, not implemented. Owner: LayerCache cloud/storage.

Today the project gateway streams every hosted byte. With direct transfer, the
gateway keeps all authority and moves only metadata. LayerCache's own clients get
a short-lived presigned URL for one immutable object and transfer it directly
with R2. PostgreSQL remains the only authority, as [hosted storage](hosted-storage.md)
requires. Object storage still grants no visibility.

It ships as one slice behind a per-pool switch. Every client falls back to
today's proxied path on any failure, so rolling back means turning the switch off.

## Why

The gateway pays for every hit several times over:

| Read | Today |
|---|---|
| Turbo, Nx and Gradle `GET` | The gateway stages and verifies the whole object on its own disk, then streams it again to the client (`server.go` `getTurbo` → `stageVerifiedArtifactRead`). |
| `HEAD` | The same full staging, even though no body is returned. |
| Actions cache hit | Lookup reads the whole blob and throws the bytes away (`actions.go` `actionsEntryAvailable`). The download then reads it again, unstaged. A corrupt object is only detected after the `200` and all of its bytes have been sent (`actionscache/handler.go` `download`, `s3.go` `verifiedObject`). |

R2 egress is free, so the gateway's bandwidth, CPU, staging disk and latency are
the cost.

## What changes

### 1. Probes stop reading objects

- **`HEAD`** answers from `Store.Head` plus `blobs.Stat`: committed SQL entry,
  object size, and the stored `sha256` metadata where present. It no longer
  increments `read_count`, which matches the intent stated in `retention.go`. Impact
  eviction stops counting probes as reads.
- **Actions lookup** uses the same metadata check, with no full read.
- **Proxied Actions download** now stages and verifies through
  `stageVerifiedArtifactRead` before sending its `200`, like Turbo already does.
  Each proxied Actions hit drops from two S3 reads to one, and corrupt bytes are
  never sent with a `200`.
- **Upload-conflict confirmation** (`server/uploads.go`, after a `409`) used to
  rely on `HEAD` deleting a corrupt entry. It now does a verified read (direct or
  proxied) before it releases the local upload pin. A corrupt remote copy is then
  invalidated and repaired by the retried upload, as before.

**Contract change** (`actions-cache-contract.md`): `integrity-rejected` at lookup
means a missing object, or a size or stored-digest mismatch. Corruption found
during download returns `404` and invalidates the entry, and the client rebuilds.
Falling back to an older healthy candidate is no longer guaranteed for content
corruption that only a full read can detect. `actions_http_integration_test.go`'s
`corrupt-object` case moves to the download step.

### 2. Direct reads

A client opts in with `Accept: application/vnd.layercache.transfer.v1+json` on
its existing read route. The gateway runs everything it runs today:

- token authentication
- selector and live-membership checks
- the entry lookup
- the `readEntryWithLease` transaction

Then, instead of the bytes, it answers `200` with `Cache-Control: no-store` and
`Vary: Accept`:

```json
{"transfer": "direct-v1", "method": "GET", "url": "https://…",
 "digest": "sha256:<hex>", "size": 123456,
 "expiresAt": "…", "lease": "<existing read-lease id>"}
```

For Actions, these fields go into the lookup response's `archiveLocation`, which
saves a round trip.

**Why a descriptor, not a `307` redirect:** existing clients reject cross-origin
redirects (`remote/turbo.go`, `redirect: 'error'`). A redirect also can't carry
the digest that Actions currently lacks.

**When the gateway answers with the proxied response instead**, unchanged:

- the client didn't opt in;
- the pool switch is off;
- the read is Public Cache or retained;
- the store has no static credentials to presign with.

#### Authority

- **One URL, one object.** A URL is a SigV4 query signature for one committed,
  content-addressed key under the requesting project's `projects/<ns>/` prefix.
  The gateway signs only keys it resolved from that project's committed entry.
  It never signs staging keys, and it never signs a key the client chose.
- **Expiry is capped.** `expiresAt` is the earliest of:
  - `maxURLSeconds` (default 120)
  - the capability token's expiry
  - the archive URL's expiry, for Actions
- **Issuing the descriptor is the admission.** Membership is re-checked when the
  descriptor is issued. New reads may start until `expiresAt`, so revocation
  takes effect within at most `maxURLSeconds`. That bound replaces "already-admitted
  reads may finish" for direct reads. `cache-tiers.md` records it.
- **URLs are bearer credentials and never leave the request/response pair.**
  - They are never logged and never written to SQL, measurement or audit rows.
  - Clients strip the query from any URL before wrapping an error, because Go's
    `*url.Error` includes it.
- **The client sends no LayerCache credentials to the URL.**
- **HTTPS only.** The URL is trusted because it came from the authenticated
  gateway over TLS. The bytes are trusted only after digest and size verification.
- **Retained reads stay proxied** until the retained contract adopts the same
  admission rule.

#### Retention

The descriptor's lease is the existing `layercache_blob_read_leases_v1` row.
Direct reads can't renew it, so its expiry is `expiresAt` plus the larger of the
current 15-minute allowance and `size / 1 MiB/s`. `deleteUnreferencedBlobs`
already skips digests with a live lease.

#### Integrity

Every client checks the complete size and SHA-256 against the descriptor before
it installs, caches or hands over any byte. Several clients gain checks they
don't have today, and each one is a precondition for that client to opt in:

| Client | Today | Adds |
|---|---|---|
| Local daemon → hosted Turbo (`remote.TurboClient` + `artifact.Store.PutVerified`) | digest only | explicit size |
| `actionscache.RemoteStorage` | none | size + digest from the descriptor |
| `action/cache` `verified-cache.ts` | digest for Public only | size + digest for Team |
| `native/client.ts` | size + digest | — |

**Fallback:** any direct failure makes the client repeat the request once
without the opt-in header. That covers transport error, `403`, an expired URL,
truncation and digest or size mismatch. It never retries the URL itself. The
proxied path then applies server-side verification and invalidation.

**Breaker:** if direct transfer fails three times in a row, the client stops
opting in for 10 minutes. A broken R2 path then costs one extra round trip, not
three per read.

#### Accounting

- **At issue:** recording the lease and `last_accessed_at` stays as it is today.
  The read is recorded as `ResultUnknown`.
- **Verified hit:** after verification, the client sends a single-use completion
  to `POST <route>/transfers/<lease>/complete` with the verified digest and bytes.
  That records the verified read and the hit measurement, with the existing
  `client-reported` basis. Actions instead reuses the signed `outcome` already
  carried by its archive URL.
- **Single use:** a completion is accepted once, only for a live lease issued to
  the same principal. A reader therefore can't inflate the retention of objects
  it never read.
- **What the dashboard shows:** proxied bytes, direct bytes, fallbacks and
  integrity rejections, each counted separately.

### 3. Direct writes

The client opts in on its existing publish route. The steps:

1. The gateway runs today's admission and quota checks on the declared size.
2. It inserts the `uploading` row.
3. It returns a presigned `PUT` for a fresh staging key, with
   `x-amz-checksum-sha256` as a signed header.
4. The client uploads.
5. The client calls `POST <route>/transfers/<upload>/commit`.
6. The gateway checks the staged object's size with `StatObject`, then runs the
   commit half of `putWithCommit`: first-writer conflict, quota, `evictToFit`,
   server-side `CopyObject`, then the SQL rows.

**R2 enforces the checksum.** A qualification against the real bucket on
7 Nov 2026 showed:

| Upload | R2 response |
|---|---|
| Matching body | `200` |
| Tampered body | `400 BadDigest` |
| Signed checksum header omitted | `403 SignatureDoesNotMatch` |
| Signed checksum header swapped | `403 SignatureDoesNotMatch` |

A staged object therefore always has the declared digest, and commit needs no
re-read. A declared size that doesn't match fails commit, and the staging object
is discarded. Unknown outcomes reconcile through the existing upload rows. Staging
TTL bounds abandoned uploads.

**Clients in this slice:** the daemon's upload worker (Turbo) and
`native/client.ts`. Actions chunked uploads and retained publication stay proxied
for now.

## Local read-through

The local daemon gets the most from this. On a local miss it fetches the
descriptor, reads from R2, verifies, and fills the bounded local store with
`PutVerified`. Repeat reads then never leave the machine. Native binaries reach it
through `action/setup`'s local daemon, not by talking to the gateway directly.

## Switch, rollout and recovery

- **Pool config:** `directTransfer: {read: bool, write: bool, maxURLSeconds: int}`,
  off by default.
- **Env override:** `LAYER_CACHE_DIRECT_TRANSFER=off` on the gateway forces it off
  without editing config.
- **Credentials:** presigning needs the pool's static S3 key with `GetObject` and
  `PutObject`. With chain or temporary credentials, direct transfer stays off.
- **Rollout:** enable read and write on one low-traffic operator project, watch
  fallbacks and integrity rejections for a day of normal CI, then enable the rest.
- **Rollback:** turn the switch off and restart the gateway, which takes seconds.
  Clients receive proxied responses again on the next request. Outstanding URLs
  expire within `maxURLSeconds`.
- **Probe and contract changes** in section 1 ship with or before the slice. They
  aren't behind the switch, so they get their own tests and land first.

## Tests

- **Fixture S3:**
  - descriptor issue
  - opt-in versus each fallback condition
  - cross-project and staging key denial
  - the expiry cap (including after token or membership revocation)
  - lease versus GC with a short `BlobGrace`
  - each client's size and digest rejection falling back once
  - breaker
  - single-use and cross-principal completion rejection
  - write admission, tampered staging and size mismatch at commit
  - no URL or query string in logs or error text
- **Updated tests:** the Actions corrupt-object case at download, and `HEAD` no
  longer reading or counting.
- **Real R2:** extend `qa/r2-native-oidc`. An independent read-only consumer
  restores through direct reads and a producer publishes through direct writes.
  Check matching bytes, gateway response bytes near zero for artifact bodies, and
  correct hit accounting after completion. Also delete an object mid-GET to record
  R2's behavior, which the client handles as truncation either way.
- **Before enabling on a pool:** independent Standards and Spec reviews and green
  required CI.
