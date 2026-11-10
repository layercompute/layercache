# Direct object transfer

Status: sections 2 and 3 are implemented in the gateway, the Go clients
(`remote.TurboClient`, `actionscache.RemoteStorage`) and `native/client.ts`, off
by default. `action/cache` implements direct reads (section 2) only. Owner:
LayerCache cloud/storage.

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

For Actions, the same object goes into the lookup response as
`layerCacheTransfer`, next to an unchanged `archiveLocation`. That saves a round
trip, and `archiveLocation` stays the proxied download and the completion target.

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

- **At issue:** for Turbo, recording the lease and `last_accessed_at` stays as it
  is today. The read is recorded as `ResultUnknown`. An Actions lookup is not a
  read (many are never followed by a download), so an opted-in lookup inserts
  only the lease and the verified completion records the access.
- **Verified hit:** after verification, the client sends a single-use completion
  to `POST <route>/transfers/<lease>/complete` with the verified digest and bytes.
  That records the verified read and the hit measurement, on the client's
  report. Turbo gateway observations are provisional transport evidence that the
  Run Summary reconciles, so the hit is an ordinary observation: no basis column
  is added. Actions instead reuses the signed `outcome` already carried by its
  archive URL: the client `POST`s the completion to that URL with its bearer
  token, and the gateway records the outcome once. The gateway accepts it while
  the lease is live, even after the archive URL expired, because a download may
  outlast the URL; the signature still binds the authority and outcome. When the
  archive URL is on another origin (a distinct `ArchiveBaseURL`), the client
  sends the completion to its configured endpoint with the same path and query,
  so the bearer token never leaves that endpoint.
- **Single use:** a completion is accepted once, only for a live lease issued to
  the same principal for the same entry, with the leased digest and size. It is
  self-attested: the gateway cannot observe the transfer, so it trusts the
  reader's report. Each issued descriptor allows at most one, so a reader can't
  inflate retention or hits beyond the reads it was admitted for.
- **What the dashboard shows:** proxied bytes, direct bytes, fallbacks and
  integrity rejections, each counted separately.

### 3. Direct writes

The client opts in on its existing publish route. The steps:

1. The gateway runs today's write authorization and the project quota check on
   the declared size. The bytes never reach the gateway's disk, so the storage
   pool's disk-headroom admission applies only to the small request itself.
2. It inserts the `uploading` row.
3. It returns a presigned `PUT` for a fresh staging key, with
   `x-amz-checksum-sha256` and `Content-Length` as signed headers.
4. The client uploads.
5. The client calls `POST <route>/transfers/<upload>/commit`.
6. The gateway checks the staged object's size with `StatObject` and, where
   storage reports one, its stored SHA-256 checksum. It then runs the commit
   half of `putWithCommit`: first-writer conflict, quota, `evictToFit`,
   server-side `CopyObject`, then the SQL rows. A direct commit never replaces a
   canonical `blobs/sha256/<digest>` object that already has the right size and
   `sha256` metadata, since other entries may share it; only a proxied
   publication, whose digest the gateway computed itself, repairs one.

**R2 enforces the checksum and the length.** Qualifications against the real
bucket on 7 and 8 Nov 2026 showed:

| Upload | R2 response |
|---|---|
| Matching body | `200` |
| Tampered body | `400 BadDigest` |
| Signed checksum header omitted | `403 SignatureDoesNotMatch` |
| Signed checksum header swapped | `403 SignatureDoesNotMatch` |
| Body of 4096 bytes against a signed `Content-Length` of 33 | `403` |
| Body of exactly the signed `Content-Length` | `200` |

A staged object therefore always has the declared digest, and commit needs no
re-read. A declared size that doesn't match fails commit, and the staging object
is discarded. Unknown outcomes reconcile through the existing upload rows.

**Upload bounds.** Issued but unfinished uploads cannot hold more staging than
the project may store:

- Storage accepts only the declared length, because `Content-Length` is signed.
- The declared bytes of the project's live `uploading` direct rows count against
  its quota at admission; admissions are serialized on the project row.
- Each principal may hold at most 16 unfinished direct uploads (`429` beyond
  that; the client uses the proxied `PUT`).
- A direct `uploading` row expires at the URL expiry plus the larger of 5 minutes
  and `size / 1 MiB/s`, not the 24-hour staging TTL, so the sweeper reclaims an
  abandoned staging key soon after its URL can no longer start a transfer.

A commit that fails for any reason other than a first-writer `409` or a quota
`507` (unknown outcome, size mismatch, expired upload) makes the client repeat
the publication once through the proxied `PUT`, which is idempotent for the same
bytes. A `507` at admission or commit ends the publication: a proxied repeat
would be refused too. The caller's own cancellation is never counted as a
direct failure and is never repeated.

**Clients in this slice:** the daemon's Team publication, both the immediate
publish and the upload worker (Turbo), and `native/client.ts` (`save` and
`saveImmutable`). Actions chunked uploads, including `action/cache`'s through the
stock client, and retained publication stay proxied for now.

### Wire API

All routes take the existing bearer token and compatibility selector. Digests are
`sha256:<lowercase hex>`. Presigned URLs are HTTPS (loopback HTTP only in
tests), are never logged, and get no LayerCache credentials.

| Request | Answer |
|---|---|
| `GET /v8/artifacts/<hash>` with `Accept: application/vnd.layercache.transfer.v1+json` | `200` `Content-Type: application/vnd.layercache.transfer.v1+json`, `Cache-Control: no-store`, `Vary: Accept`, the usual `x-artifact-duration`, `x-artifact-tag`, `x-layercache-digest` headers and the descriptor body. `404` is a miss. Any other `200` is the proxied bytes. |
| `POST /v8/artifacts/<hash>/transfers/<lease>/complete` `{"digest","size"}` (read capability) | `204` once. `404` for an unknown, expired, replayed, other-principal or other-entry lease, or a mismatched digest or size. |
| `POST /v8/artifacts/<hash>/transfers/upload` `{"digest","size"}` (write capability) | `200` `{"url","headers","upload","expiresAt"}`: `PUT` the exact bytes to `url` with every header in `headers`. `headers` carries `x-amz-checksum-sha256` and `Content-Length`. `404` means direct write is unavailable and `429` that the principal has 16 unfinished uploads: use the proxied `PUT`. `400` invalid declaration, `507` quota. |
| `POST /v8/artifacts/<hash>/transfers/<upload>/commit`, Turbo metadata headers as on `PUT` | `200` `{"stored":true}`. `409` first-writer conflict, `400` staged object missing or of another size, `404` unknown, replayed or other-principal upload, `507` quota. |
| `GET …/_apis/artifactcache/cache` with the same `Accept` | The lookup JSON gains `layerCacheTransfer`, the descriptor capped by the archive URL's expiry. |
| `POST <archiveLocation>` `{"lease","digest","size"}` (read capability) | `204` once, recording the signed hit outcome. `404` as for Turbo completion. |

## Local read-through

The local daemon gets the most from this. On a local miss it fetches the
descriptor, reads from R2, verifies, and fills the bounded local store with
`PutVerified`. Repeat reads then never leave the machine. Native binaries reach it
through `action/setup`'s local daemon, not by talking to the gateway directly.

## Stock Turbo redirect

Hosted CI runs the stock Turbo binary, which cannot read a descriptor. With
`turboRedirect` on, the gateway answers its `GET` with a `302` to a presigned
object URL. It runs the same lookup, lease and membership check as a descriptor.
Tested behaviour for each pinned Turbo version is in
[`qa/turbo-redirect/RESULT.md`](../qa/turbo-redirect/RESULT.md).

Only a `User-Agent` of `turbo <version>` at or above 2.9.14 gets the redirect,
because LayerCache's own clients reject cross-origin redirects. These stay on the
proxied path:

- `PUT`. Turbo treats a 3xx on upload as success and stores nothing.
- `HEAD`. A URL signed for `GET` fails on `HEAD`.
- Signed artifacts. Turbo reads `x-artifact-tag` from the final response, and
  object storage cannot send it.
- Public-origin entries, as with descriptors.

These reads skip the gateway's digest check, and Turbo does not check one itself.
An altered object that is still a valid archive restores as a normal hit.

Turbo reports nothing when a redirected restore fails. It rebuilds and uploads. The
gateway treats a `PUT` within an hour of a redirect for the same key as that
failure, and the next read of that key is proxied. The proxied read verifies the
object and deletes a corrupt entry, so the following rebuild can replace it. Three
such failures within 10 minutes stop all redirects for that project for 10
minutes. This state is per gateway process and resets on restart.

Turbo's Run Summary still decides hit or miss. Object storage cannot send
`x-artifact-duration`, so Turbo reports `timeSaved: 0`. The redirect's observation
carries the entry's recorded build time, and reconciliation uses it for that hit.

## Switch, rollout and recovery

- **Pool config:** `directTransfer: {read: bool, write: bool, maxURLSeconds: int}`
  on the hosted pool's project template, off by default. `maxURLSeconds` is 0
  (meaning 120) through 900. Changing it is not a storage migration: the pool's
  backend fingerprint and the operator project binding both ignore it, so
  turning it on or off only needs a restart.
- **Env override:** `LAYER_CACHE_DIRECT_TRANSFER=off` on the gateway forces it off
  without editing config.
- **Turbo redirect:** `directTransfer.turboRedirect` needs `read` and is off by
  default. `LAYER_CACHE_TURBO_REDIRECT=off` on the gateway forces only the redirect
  off. See [Stock Turbo redirect](#stock-turbo-redirect).
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
