# Retained private artifacts

Proposed contract, 2026-10-16. This document defines the next implementation
and qualification slice. LayerCache currently exposes no retained-artifact
route or retained publication receipt. Keep the existing required-transfer
provider and its failing integrity/provenance checks until a replacement passes
source, deployment and fresh hosted-consumer qualification.

## Scope and existing guarantees

A required pipeline output needs an immutable producer identity and an explicit
retention deadline. An evictable cache can accelerate a transfer, but its miss
must lead to the retained provider or a supported fresh producer. Publication
fails visibly if neither required path succeeds. A Local acknowledgement or a
queued upload is never a Team retention receipt.

An observed required artifact of 124,078,900 bytes took 229 seconds in a
retrieval-and-provenance step. That step includes work outside body transfer;
the transferred ZIP size, request phases and retry cost were not measured.
It establishes a measurement target, not a network rate or a promised saving.

The first implementation should retain one opaque file in an authorized private
project. Use the existing PostgreSQL/S3 implementation and MinIO SDK. Preserve
Personal owner-only and Team membership rules; Public publication remains
outside this slice. Do not add an archive format, task executor, registry,
daemon or second S3 SDK. Directory packaging and package inspection stay with
the existing native client.

Current source was inspected at `09bf4f77621ef0f6c732ef83e957858f01c7ed7c`:

| Existing module | Reuse and limit |
| --- | --- |
| [Cloud publication](../internal/cloud/cache.go), `putWithCommit` | Verified staging, immutable content, project quota locking and a transaction hook for protocol metadata. Retained reservation and receipts are new work. |
| [Actions commit](../internal/cloud/actions.go) | An existing example of metadata published through that transaction hook. Its cache acknowledgement provides no future retention. |
| [S3 adapter](../internal/cloud/s3.go) | Production SDK multipart transport, input-error fence and complete size/digest verification. Backend availability and recovery remain separately qualified. |
| [Cloud pins](../internal/cloud/pins.go) and [schema](../internal/cloud/schema.go) | Eviction protection exists. Ordinary pin rows cascade when a cache entry is explicitly deleted. |
| [Cloud reads](../internal/cloud/cache.go), [maintenance](../internal/cloud/maintenance.go) | Active-read protection and collection machinery. An active-read lease does not reserve a future handoff. |

`cloud.Store.Delete` calls `deleteEntry` without testing ordinary pins. The
current pin foreign key uses `ON DELETE CASCADE`. That is existing cache
behavior, not a retained contract or an explanation of the historical Actions
lookup miss in issue #34. Adding an ordinary pin alone cannot implement the
promise below. Local pins and short graph-read leases also do not establish a
remote retained publication.

## Publication interface

The retained module should hide reservation, staging, commit and recovery behind
three caller operations:

```text
Publish(authorized producer, immutable declaration, body) -> receipt
Inspect(authorized principal, immutable publication identity) -> current state
Open(authorized reader, expected receipt) -> current receipt and verified stream
```

These are proposed operations, not CLI commands or existing endpoints.
Administrative revocation is a separate policy operation. Consumers receive no
publish, renew or revoke authority.

The declaration binds the project, immutable repository ID, source SHA,
workflow identity, run ID, run attempt, producing job identity, logical output
name, execution profile, exact byte count, SHA-256 and required retention deadline.
The server may reject a deadline outside policy; it must not silently shorten
the requested window. The approved deadline must still be valid at commit.
Its immutable publication
identity includes the producer attempt and output name. Reusing that identity
with different bytes or provenance conflicts; a display name or `latest` alias
does not select a required output.

An authenticated adapter must derive repository/run/attempt/workflow claims and
apply the approved producer policy. The existing OIDC verifier reads claims
that current cache capabilities do not all preserve. Carry the required facts
into publication authority explicitly. Caller-supplied JSON is an assertion,
not verified GitHub provenance; OIDC identity alone does not attest the package
contents. Storage-only qualification must state which producer assertions it
has not independently verified.

A receipt binds the immutable publication identity, scope, payload size/digest,
producer binding, storage domain, server-approved `retainUntil` and receipt
schema version. It carries no bearer token or signed storage URL. `Inspect`
returns authoritative current state; a receipt file alone cannot establish
current authorization, retention or availability.

An exact retry returns the original publication and deadline, without renewing
retention. A lost response is an unknown commit outcome. Resolve it through
`Inspect`; never delete possibly committed bytes merely because the client
did not receive success.

## Admission and acknowledgement

`Publish` must reserve declared bytes and bounded metadata under the project
quota lock before accepting an upload. Bound concurrent sessions, admitted
staging bytes and reservation lifetime. Use existing pool and project limits;
do not raise them as part of this feature. Reject admission that cannot fit
around existing retained obligations. A quota reduction must also respect live
obligations and outstanding reservations.

After admission, stage through the existing bounded transport and verify the
entire declared size and SHA-256. Before returning a retained acknowledgement,
the backend must have completed object publication and one metadata transaction
must commit the immutable receipt, active retention claim, eviction protection
and accounting. A temporary upload, object-only success, Local admission or
retry-queue entry cannot return a published receipt.

Committed content shared within a project is charged once physically; each
receipt/claim consumes bounded metadata. Conservatively reserve the full upload
size until commit, then release excess. Separate committed retained bytes,
evictable bytes, outstanding reservations and transient physical bytes in
reporting. Logical quota is not an instantaneous S3 billing bound during
multipart upload, cleanup grace or an object-endpoint outage.

Use a restrictive active-claim reference to the exact generic entry, plus a
system pin, committed through `putWithCommit`. Quota eviction and pruning must
exclude the claim. Every explicit deletion and invalidation path must respect
it; a restrictive foreign key can fence accidental deletion even if a caller
bypasses the eviction selector. Verify collection of the underlying object also
respects retained references.

## Lifecycle and reads

```text
reserved -> staging -> published -> expired
    |          |          |------> revoked
    |          |          `------> unavailable
    `----------`-> failed/abandoned
```

The first slice uses a fixed server-approved expiry, with no renewal or mutable
replacement. Expiry and authorized revocation atomically change receipt state
and release the claim/pin. Keep a bounded tombstone to distinguish these states
from never published. Bound tombstone count, bytes and lifetime; once that
history expires, report an unknown/not-found state. Tombstones must not retain
a restrictive entry reference forever.

An interrupted upload exposes no published receipt. Reservation expiry releases
its admitted budget; backend cleanup can remain pending and must stay bounded
and visible. Restart reconstructs obligations from SQL rather than process
memory or a client receipt. A retry must resolve its durable publication state
before creating another upload.

`Open` checks current scope/authority, expected identity, published state and
deadline before admitting a read. At expiry or revocation, stop new reads;
already admitted reads may finish under their existing bounded active-read
protection. Revocation of project membership denies subsequent operations.
Document this admitted-read policy rather than promising mid-stream recall of
bytes already delivered.

Read complete bytes through the existing integrity fence. A missing or corrupt
object becomes an explicit unavailable state with preserved receipt/audit
evidence, not successful publication or silent fallback to another identity.
The consumer writes to owned staging, checks complete payload size/digest and
producer manifest, then atomically publishes its local result. Existing APK,
signature, entitlement, package and fresh runtime assertions remain.

The retention promise covers authorized availability through ordinary quota
pressure, maintenance and restart until expiry or explicit revocation. It does
not create an unqualified replication, backup, disaster-recovery or uptime SLA.
Outages and detected corruption fail visibly and use the documented retained
fallback. Storage administration outside the module's failure model needs its
own operational safeguards.

## Transfer phase evidence

Instrument the actual retained provider path before replacing it. The observed
229-second step used a custom retained-artifact client; do not assign another
client version's compression, retry or extraction behavior to that observation.
This is the proposed receipt schema, not newly observed phase data.

Record monotonic start/end offsets, outcome and attempt number for:

| Phase | What the interval includes |
| --- | --- |
| Client setup | Tool initialization and owned local staging preparation. |
| Provider metadata | Exact artifact/run lookup, authentication, expiry and producer-identity checks. Split actual requests if they can be observed separately. |
| Delivery authorization | Resolving an authorized storage URL, without recording that URL or its query. |
| Request/headers | Connection acquisition, available DNS/connect/TLS/request timings and first response-header byte. Unavailable subphases stay unknown. |
| Body | First nonempty body chunk to verified stream EOF, with actual consumed bytes for each attempt. Record request-to-first-body latency separately. |
| Verification | Full digest and producer-manifest verification, distinguishing archive/container digest from payload digest. |
| Extraction | Unpack/decompression plus its bounded output checks. |
| Local publication | Final rename or equivalent atomic local commit. |
| Package/runtime validation | Existing inspection and current application assertions, outside successful transport alone. |

Record attempts, retry backoff, bytes consumed by failed attempts, accepted
payload bytes, complete step time and complete job time separately. Count body
bytes actually consumed; content length and provider metadata are declarations,
not observed transfer counters. Attribute partial-transfer and verification
failures without logging payloads, credentials, repository paths or signed URLs.

Hashing can overlap body reads; extraction may overlap them in some clients.
Preserve intervals and overlap instead of adding phase durations into a false
total. Body wall time can include storage/hash/extraction backpressure and is
not pure bandwidth. Do not disable verification or change the I/O path merely
to improve a reported transfer number. A download-to-file experiment needs its
own label if it changes the existing client path.

Use native client instrumentation. Go's standard `httptrace` exposes connection
and response-header events per round trip. curl's write-out supports request
timings and body counters. Neither automatically identifies time spent in a
separate provenance check or extraction process.
[Go trace contract](https://pkg.go.dev/net/http/httptrace),
[curl write-out](https://curl.se/docs/manpage.html#-w).

## OSS reuse and qualification order

GitHub's native artifact provider remains the required-transfer path. Resolve
immutable artifact IDs and their run/expiry metadata through its supported
interface. A ZIP/container digest and the contained payload digest are different
identities; retain explicit failing verification for both as applicable.
[GitHub artifact interface](https://docs.github.com/en/rest/actions/artifacts).

LayerCache already uses MinIO Go v7.0.95 and ORAS Go v2.6.2, both Apache-2.0.
Reuse the existing S3 adapter for this single-file slice. ORAS remains suitable
when native OCI graph delivery is needed. OCI digest identity/upload completion
does not itself reserve retention or project capacity.
[MinIO license](https://github.com/minio/minio-go/blob/bd91926e6b4d828b4b5072088fd8b4a6a7a44eab/LICENSE),
[ORAS license](https://github.com/oras-project/oras-go/blob/105715ee12eac6895ec736a075285c34d9f2eeb6/LICENSE),
[OCI distribution contract](https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md).

S3 Object Lock can protect an exact object version when the backend supports it.
It is optional backend protection, not quota admission, current authorization
or endpoint availability. Qualify support before depending on it.
[S3 Object Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html).

First implement the retained storage module without a live route, using the
existing disposable PostgreSQL and production-S3-SDK fixtures. Prove exact
publication/fresh consumption and a consumer-only rerun; duplicate/conflicting
identities; lost acknowledgement/restart; concurrent admission/quota shrink;
expiry/revocation/maintenance races; deletion fencing; partial/corrupt/missing
bytes; read-only and cross-project denial; and expiry during an admitted read.
Verify accounting and eventual object cleanup as well as returned status.

Then qualify the authenticated producer/consumer adapter and current phase
receipts. A separate reviewed deployment and fresh hosted-consumer proof are
required before changing the required-transfer fallback. This contract and its
source inspection establish no new retained service, transfer speedup or cause
for issue #34's historical miss.
