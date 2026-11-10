# Personal storage, shared artifacts and safe worktree materialization

Status: proposed design; no implementation or device configuration in this change.
Reviewed: 2026-10-23. Owners: LayerCache storage/native clients, CLI and product.
Repository baseline: `26b8386cb7e65e20b6ad9382d9ac9dd5356cf2cc`.

[TODOs](../TODOs.md) owns priority, milestone state and outstanding work.
[Source research](research/personal-storage-oss-20261023.md) records upstream
revisions, licenses and evidence. This document defines the proposed behavior
and completion criteria; it does not establish installed or deployed support.

## Outcome and product decision

Keep a small, useful working set on a laptop, put optional reusable bytes on
storage the user already owns, and avoid repeating compatible computation or
materializing the same immutable data for every worktree.

Internal storage, removable SSDs and explicitly configured self-hosted SSH cache
hosts remain free, account-free OSS. No LayerCache login, subscription or online
entitlement is required to operate those locations. Future manually configured
remote routing remains OSS too. Dedicated SSD routing and the SSH peer workflow
are proposed, not qualified. Hosted capacity, managed team administration and
automatic host enrollment/coordination are optional cloud services. Self-hosted
collaboration is not prohibited by the cloud-service policy.

The first target topology is one local owner with internal and optional SSD
pools, plus one explicitly configured private remote. This does not require a
discovery mesh. Preserve cloud-only operation without creating local artifact
storage. Personal, Team and trusted-publisher Public remain access scopes,
independent of physical placement.

| User location | Proposed responsibility |
| --- | --- |
| Mac internal filesystem | Coordinator state and a small bounded hot/offline set |
| Often-attached SSD | Larger optional immutable cache; direct reads while attached |
| Self-hosted SSH box | Authenticated remote cache host, storing and producing compatible results |
| SSD-backed Mac VM | Independent native client/producer; its disk is not additional physical capacity |
| Optional hosted service | Authorized private capacity and managed sharing; separately qualified Public reads |

An attached SSD expands the warm set and can avoid network fetches or rebuilding.
It need not beat an already-hot internal read. Storage sharing does not schedule
remote execution, launch simulators, or establish cross-platform compatibility.

## Current implementation and gaps

The following observations refer to the repository baseline above. Subsequent
mainline work added separately gated [direct object transfer](direct-object-transfer.md)
for hosted cache bytes; this proposal must coexist with that path.

| Existing behavior | Required change or limit |
| --- | --- |
| Go [artifact.Store](../internal/artifact/store.go) maps native references to SHA-256 blobs, verifies reads, deduplicates equal payloads and protects readers | One root per daemon; shared routing and multiple local pools do not exist |
| [Turbo read-through](../internal/server/server.go) and [durable upload queue](../internal/server/uploads.go) connect a Local daemon to one private endpoint and verified Public | Use this complete native path first; multiple private destinations require explicit routing/publication policy |
| [Configuration](../internal/config/config.go) has one `DataDir`, `TeamURL`, `MaxBytes` and `MinFreeBytes` | Changing `DataDir` is not hotplug, migration or an aggregate physical cap |
| [NativeCache](../native/client.ts) has a separate archive cache and budget; Expo uses [process-pinned extractions](../native/extractions.ts) | Archives are named by native key plus digest sidecars, not one canonical blob owner; migration/connection must be explicit |
| Native CLI/action restore destinations can be caller-owned | Their output bytes and lifetimes remain separate from managed archive accounting |
| [Graph read leases](../internal/artifact/read_leases.go) protect an exact object set for at most two minutes | A returned filesystem path needs consumer-lifetime protection; ordinary reads do not renew graph leases |
| [storage inspect](../internal/cli/storage.go) inventories explicit paths without opening a store | It proves neither deletion eligibility nor exclusive/reclaimable allocation |
| [vm-route issue](../internal/cli/vm_route.go) issues short-lived, scoped standalone Turbo/Actions credentials | Useful peer credential seam; it is not host enrollment or instant per-device revocation |
| Gradle/Nx support Local and direct private protocols | Their Local-to-private forwarding remains separate work; Xcode external Swift replay remains unqualified |

The current `artifact.Key` lacks origin/visibility fields because distinct
configuration/store roots supply isolation. A shared owner must restore that
isolation explicitly before enabling cross-project lookup. Hosted CAS remains
project-isolated; local work does not introduce global hosted deduplication.

## Scope and invariants

Start with closed immutable native artifacts, then explicitly owned expanded
generations. Keep the native client's complete declared inputs and identity.
Physical location, machine name and available RAM/disk do not change native keys.
Target architecture, ABI, OS, SDK and toolchain continue to matter where the
native contract requires them. Equal payload bytes alone do not prove compatible
execution or grant access.

Required invariants:

1. Authorize a complete logical reference before consulting any payload digest.
2. Every writable consumer gets independent inodes; arbitrary writable outputs
   never alias a canonical cached inode through hardlinks.
3. Storage absent at artifact acquisition produces bounded cache fallback. It
   never turns an otherwise runnable native build into a cloud-login or
   SSD-insertion requirement. A path used directly from removable media has a
   separate explicit connected-for-consumer-lifetime contract below.
4. Destination verification and durable commit precede retiring an offloaded
   source replica. No cross-filesystem atomic rename is assumed.
5. Active readers, pending uploads and owned materializations remain charged
   until their bytes can actually be released.
6. A cache acknowledgement, extra replica, pin or successful clone establishes
   no backup or required-handoff retention guarantee.
7. A source-qualified adapter, installed binary, actual workload acceptance and
   hosted deployment are distinct outcomes.

Excluded from the first implementation: arbitrary filesystem migration, active
worktree rewrites, shared mutable `node_modules`/DerivedData/Gradle directories,
moving live VM images, secret or signing-material ingestion, automatic peer
discovery, remote execution, filesystem reformatting and replacing native hashes.
Required pipeline handoffs keep the retained-artifact contract.

## Storage owner and interfaces

Use a deep local storage module behind existing native adapters. Keep local
filesystem and existing authenticated HTTP adapters behind its interface;
native adapters retain archive/graph semantics and compatibility decisions.

```mermaid
flowchart TD
  N[Native clients and worktrees] --> A[Authorize native reference]
  A --> O[Local storage owner]
  O --> I[Internal pool]
  O --> S[Optional SSD pool]
  O --> R[Explicit private remote]
  O --> M[Materialization owner]
  M --> T[Leased immutable generation]
  M --> W[Independent CoW clone or copy]
  V[VM native client] --> R
```

Keep coordinator configuration, namespace policy and transfer journal internal.
Initially reuse one existing `artifact.Store` per local pool, including that
pool's metadata, staging and blobs on its filesystem. An internal catalog may
remember SSD references while absent, but those records are availability hints,
not proof of residency. Each pool must be recoverable without an atomic
transaction spanning two filesystems.

The conceptual interface, not an implemented SDK, is:

| Operation | Contract |
| --- | --- |
| Acquire authorized artifact | Resolve complete native reference; return verified stream/handle, source and read lifetime |
| Commit native artifact | Admit capacity, stage and verify; publish an immutable reference in the selected pool |
| Relocate replica | Pin source, reserve destination, copy/verify/commit, then retire eligible source |
| Acquire materialization | Return an owned read-only generation or isolated writable view, method and consumer lifetime |
| Release/recover ownership | Protect live consumers and reconcile abandoned operations conservatively |
| Inspect/plan | Report placement, available sources, ownership, allocation limits and expected fallback without mutation |

This is not a generic filesystem interface. Native tools still request their
normal artifacts and execute their normal commands. Long-lived path consumers
need renewal/release or an explicitly qualified process-lifetime adapter;
the two-minute graph lease cannot be reused for that purpose.

### Identity, ownership and deduplication

Separate three identities:

- **Logical reference:** stable authority/origin, Personal/Team/Public authority,
  project, integration, compatibility profile and the full native coordinates
  including version/ref where required.
- **Content identity:** verified digest and length of immutable bytes, plus the
  native graph closure when applicable.
- **Residency:** pool identity, generation, availability and verified committed
  representation. A host address or mount path is not an artifact key.

Default byte deduplication stays inside an authorized storage scope. One owner
can enforce one aggregate budget while keeping project/authority partitions.
Cross-worktree references within a supported scope may share a blob; duplicate
replicas on separate filesystems each consume capacity. Cross-project physical
deduplication needs a separately qualified policy and cannot expose digest
existence, another project's metadata, or access through a digest-only route.

Coalesce concurrent fetches by complete authorized reference. Once bytes are
verified, deduplicate within the allowed content scope. Cancelling one waiter
must not cancel a fetch still needed by another. Conflicting bytes for the same
immutable reference are a conflict/quarantine event, not last-writer-wins.

One coordinator exclusively owns each local pool. Host and VM communicate over
the cache protocol; they must never open the same live SQLite store. Existing
store open performs staging/orphan cleanup, so ownership must be established
before open, including reconnect and migration.

## Placement, removable SSD and remote policy

Configure read order, local admission and remote publication separately. Initial
defaults: internal hit, attached SSD hit, explicitly configured private remote,
then existing qualified Public lookup where supported. Public resolution keeps
its own identity, signature, expiry and revocation rules. It is not a fallback
that authorizes arbitrary public bytes as private native results.

Keep a bounded internal hot set. While SSD is safely available, admit selected
colder artifacts directly there; otherwise admit within the internal budget or
decline optional cache storage. SSD hits should stream directly, without
mandatory internal promotion. A remote download stages into its selected local
destination. Explicit hot/offline selection can promote artifacts, subject to
budget; it does not guarantee all tools/dependencies needed for offline builds.

Do not publish to every readable destination. Initial remote publication has
one explicit private destination and its existing durable queue policy. Queue
bytes and pins are bounded; outage cannot cause indefinite internal growth.
The user may select remote-only operation independently.
M3 selects one private remote, self-hosted or hosted; simultaneous SSH-plus-hosted
private lookup is deferred until multi-destination precedence/publication is
qualified. Future manual routing stays OSS.

### Removable pool lifecycle

Pool states: absent, probing, ready, draining, unavailable and quarantined.

Enrollment records the actual volume identity, an owner-only LayerCache marker,
store generation and supported filesystem capabilities. Volume names and paths
are display/lookup hints. UUIDs identify volumes; they are not authentication.
Unsupported or ambiguous identities require refusal or a separately qualified
adapter, not path-only auto-initialization.

Before opening or writing, verify expected mounted volume, marker, ownership,
exclusive lock and headroom. Hold directory handles and revalidate around
operations to address mount replacement races. Never create a replacement
directory under an absent `/Volumes/...` mount. A renamed mount can be accepted
only after the enrolled identity is verified; replacement/reformat needs new
enrollment. Duplicate identity observations are an explicit error.

On unplug, invalidate readiness, cancel affected cache operations and preserve
the native protocol's failure/fallback behavior. A transfer whose body has
already started may need to fail and restart verification; transparent recovery
is qualified per adapter, not assumed. Do not expose partial restored outputs
as a successful artifact. Reconnect reconciles interrupted staging and
journals under exclusive ownership before serving. Preparing to disconnect can
drain this owner's operations later; it cannot make unplugging an active VM or
another app's SSD data safe.

Replica relocation is journaled:

```text
pin source -> reserve destination -> stage on destination -> verify digest/size
-> flush -> commit destination reference -> record completion
-> release transfer pin -> source eligible for retirement after other leases
```

Crash before destination commit leaves reclaimable staging. Crash after commit
may leave two valid copies. Recovery prefers a recoverable duplicate to loss.
Offloading removes a replica, not every logical reference or every other copy.
Optional cache content remains rebuildable even when its only cached replica
is on an absent SSD. Required retained data follows its separate contract.

### Free OSS SSH peer and VM access

Use the configured SSH alias and normal host-key/user verification to reach a
user-operated, rootless cache host. Carry the existing authenticated native HTTP
protocol through a tunnel or explicitly protected network endpoint. Do not mount
a remote cache database or invent cross-host SQLite/file synchronization.
Tailscale/SSH connectivity does not itself authorize project reads or writes.

A later bounded helper may mint project/profile/integration-scoped capabilities
on the remote side using the existing `vm-route issue` model. Keep administrator
and `LocalToken` secrets on the cache host. Initial support is limited to the
qualified native integration; it does not imply a generic credential adapter.
Endpoints have stable authority distinct from a changing tunnel address.

Use explicit deadlines and bounded retries; distinguish unreachable, denied,
expired, corrupt, missing and incompatible. Native computation proceeds when
an optional cache fails, while the native command's actual failure status is
preserved. Do not add tailnet discovery or connect to machines automatically.

Offline authority remains integration-specific. A standalone owner can reuse
their own local scope without a LayerCache account; scoped hosted snapshots
retain their existing fresh authorization requirements. Revocation cannot
recall downloaded bytes or instantly reach a disconnected client. Removing an
SSH key prevents renewal; already issued standalone capabilities can survive
to expiry unless a new revocation registry is separately implemented.

## Sharing expanded data across worktrees

Treat archive reuse, expanded-tree reuse and writable worktree state as separate
representations with separate owners and accounting. Moving packed archives
does not remove output files materialized in each internal checkout.

### Native sharing before another copy

Git already shares repository objects across worktrees; working files, indexes
and generated outputs still have their native lifetimes. pnpm already shares
package content and supports native clone/hardlink/copy policies. Its pinned
10.x global virtual store is an optional future experiment with ESM/CI caveats,
not a reason to share one arbitrary mutable `node_modules`. Moving only a pnpm
store across filesystems turns linking into copies. Gradle dependency caches
have native lock/read-only-seed constraints; host and VM cannot assume one
writable shared cache. Preserve these mechanisms rather than layering another
archive over already shared content. Primary sources and exact pinned behavior
are in the [research note](research/personal-storage-oss-20261023.md).

### Managed immutable expanded generations

When a native consumer explicitly accepts a read-only path, extract once into
an owned immutable generation and return a leased path. Reuse the current Expo
materialization seam where compatible, after qualifying that the consumer does
not sign, instrument, chmod, or otherwise modify the shared baseline. Other
consumers receive independent writable views.

Default long-lived materializations live internally when the workload must
survive SSD disconnection. A read-only path returned directly from SSD is an
explicit opt-in requiring that volume to stay connected for the consumer's
lifetime. A lease protects against GC, not physical removal; an active consumer
can receive I/O errors after unplug, and remote fallback cannot repair its open
path transparently. Do not automatically rerun a native command to hide this
failure. Lazy network/filesystem views have the same mid-execution availability
tradeoff and require their own acceptance.

The expanded representation needs a versioned manifest and materializer policy:
authorized source artifact/graph, normalized relative paths, entry types,
content digests/lengths, executable modes and relevant native metadata. Preserve
the original archive identity; an expanded manifest does not replace its native
key, signature or successful-result evidence. Metadata-sensitive native formats
must be preserved or refused, not silently normalized away.

Reuse existing extraction controls: declared expanded-size/entry limits,
traversal/case-collision refusal and no following destination symlinks. Refuse
hardlink archive entries and unsupported special files. Permit relative symlinks
only under a specifically qualified native policy that cannot escape its owned
generation. Never adopt an arbitrary existing destination tree. Stage, validate
and atomically publish a complete generation; per-file cloning does not make
a live multi-file directory into a consistent snapshot.

One canonical expanded baseline per supported manifest avoids repeated unpacking.
Different archives with identical files may later reuse immutable file content
within the same allowed scope. Byte-equal files with different modes/metadata
cannot simply share a hardlinked inode. Retain original packed bytes only when
needed for native protocol delivery or policy; an expanded copy is additional
allocation until that lifecycle is explicitly qualified.

### Materialization decision

| Consumer/placement | Selected mechanism | Storage claim |
| --- | --- | --- |
| Native client already manages package/object sharing | Native store/link policy | Attribute existing sharing to that client |
| Qualified read-only path consumer | One leased immutable expanded generation | Avoid repeated extraction and consumer copies |
| Writable consumer, same supported filesystem | Independent native CoW clone | Initially shared blocks; later dirty blocks consume space |
| Writable consumer across filesystems or unsupported clone path | Admitted independent copy, or explicit require-clone refusal | Correct isolation; no automatic space-saving claim |
| Daemon-internal immutable aliases with identical metadata | Optional narrowly controlled hardlink | One inode; no writer or mutable-source exposure |
| Arbitrary writable worktree, live mutable source or shared build directory | Refuse hardlink/shared-tree optimization | No safe break-on-write interception exists merely by calling link |

Ordinary `chmod 0444` is not enforced immutability against the file owner. The
owner can change it back; permissions, data and ownership alias through hardlinks.
Existing writable file descriptors/mappings also defeat a read-only-path promise.
Never ingest mutable source files into the canonical store by hardlink. Shared
paths are a qualified trusted-consumer contract, not a same-user security sandbox.
Use clones/copies for uncertain consumers and verify canonical integrity before
reuse; quarantine detected mutation. No daemon-controlled immutable hardlink
may escape into a consumer path that can be modified.

### Native CoW policy

Use existing permissive runtime seams first: the repository's BSD-licensed
`golang.org/x/sys` exposes Darwin clone and Linux ioctl wrappers; pinned Node
supports forced-clone copy flags. A new filesystem dependency is unnecessary
for this narrow adapter. See [source audit](research/personal-storage-oss-20261023.md).

Define policy independently of command spelling:

- `require-clone`: use a native forced clone and refuse unsupported/cross-volume
  placement. No surprise full copy.
- `prefer-clone`: attempt forced clone, then admit an explicit independent copy
  only for a recognized unsupported/cross-device result and sufficient capacity.
- `copy`: independent-file semantics. Report the known mechanism; generic copy
  acceleration may itself share extents, so physical block sharing can be unknown.

APFS cloning requires the same volume; internal-to-external moves copy data.
Linux Btrfs and reflink-enabled XFS require actual source/destination capability
qualification. Ordinary upstream ext4 is copy-only for this design. Do not
swallow permission, collision, quota or I/O errors as unsupported capability.
Apple's best-effort clone flag can silently copy; successful completion alone
does not prove CoW. Record actual per-file clone/copy/mixed outcome and errors.
Native CoW isolates inode metadata and subsequent writes, but does not create
an independent backup or reserve capacity for later divergence.

Keep canonical expanded baselines on each filesystem where enough local reuse
justifies them. For example, an SSD baseline serves read-only SSD consumers,
but an internal writable view needs a transfer; an internal baseline can CoW
into several internal worktree outputs. Retaining both is a measured tradeoff.
File/chunk deduplication of closed generations is a later optional representation;
chunking compressed archives alone must not be assumed effective.

## Capacity, quotas and lifecycle

Account per owner, per representation and per actual capacity domain:

```text
committed packed blobs + expanded baselines + metadata/WAL + staging
+ transfer/migration overlap + reader/upload-held retired bytes
+ owned writable views and permitted divergence + deletion backlog
```

Do not double-count one blob through multiple references. Do charge its actual
replicas on different filesystems. Pool caps and project fairness share one
admission owner per filesystem/capacity domain. Distinct APFS volumes can share
a container, so their available capacity cannot be summed as independent disks.
The VM's guest allocation and outer sparse backing file are separate views of
the same storage, not additive capacity or independent durable replicas.

Reservations cover concurrent staging and expansion before publication. Clone
success can be cheap initially and still fail later with ENOSPC. For managed
writable views, declare an admitted divergence/growth envelope and how it is
enforced. If the filesystem/consumer cannot enforce it, report the bound as
advisory and require full-copy-sized admission for the known baseline; no hard
physical ceiling is claimed for arbitrary subsequent consumer writes. A native
quota/bounded filesystem is required for a hard allocation guarantee. Free-space
checks cannot prevent unrelated processes from filling the filesystem.

Start with existing LRU and explicit hot/offline budgets. Defer predictive
placement and new value heuristics until representative observations justify
them. Leases protect complete native graph closures, pending publication and
live materializations. Preserve process-lifetime behavior where a native API
has no release callback; qualify child processes, PID reuse, crashes and stale
recovery. Unlinked bytes held by readers remain charged until released.

Report apparent size, managed/reference bytes, per-file allocated bytes and
filesystem capacity changes with their measurement limits. Inode-aware totals
handle hardlinks but cannot establish exclusive reflink use. Snapshots can keep
deleted blocks alive. Exclusive/reclaimable bytes remain unknown unless native
evidence supports them; never present uploaded bytes or logical archive size
as reclaimed Mac storage.

## CLI contract and migration

The user journey is inspect -> preview -> explicitly apply owned changes ->
status -> optionally prepare an offline set. Proposed command sketches below
are not current commands or a commitment to final spelling:

```sh
# Existing read-only capability, explicit roots only.
layercache storage inspect --path /selected/workspaces --json

# Proposed commands; not implemented.
layercache storage pool add ssd --path /Volumes/WorkSSD/LayerCache --optional
layercache cache remote add home --via-ssh my-cache-host --read-only
layercache storage plan --goal free-space
layercache storage apply --plan /protected/accepted-plan.json
layercache storage status
```

Plans are bounded, versioned and bound to owner/pool generations. Show eligible
objects, copy versus retirement, expected peak/admission, authority, absent-pool
fallback and unknown reclaimed bytes. Apply revalidates identities, authority,
leases and space; a stale preview never authorizes deleting newly active data.
Use protected credential references outside examples/repositories. Read routes
do not imply publication authority. No full host paths need upload to cloud.

Migrate stopped legacy Go/TS stores by verified resumable import under exclusive
ownership. Do not copy live SQLite files piecemeal, let two old/new owners GC
the same root, or silently rewrite operator configurations. Keep source stores
until verified import and explicit retirement. Record old standalone support
and old-client fallback; never require cloud enrollment to complete migration.

Legacy ordinary TS archives contain a non-reversible hash of project/profile/key
and a digest sidecar; they do not record origin/visibility or complete logical
coordinates. Do not bulk-import them under the currently selected backend.
Permit lazy import only when a current authorized reference and trusted original
configuration/provenance establish the old entry's association. Preserve scoped
snapshot origin partitions and fresh authorization. Unattributable entries
produce a cold miss and stay untouched pending explicit retirement; digest
verification alone cannot turn them into a trusted reference. Apply the same
provenance requirement to Go stores whose scope came from their old configuration.

## OSS reuse decisions

[LayerFS](https://layerfs.ai) is explicitly investigated in the
[source audit](research/personal-storage-oss-20261023.md), including its cloned
revision and source/license evidence. It offers shared chunked committed trees
with writable workspace overlays, so it addresses more than byte-identical
whole-file copies. It is a candidate for a bounded Linux workspace experiment.
Current constraints include a Disposable-only Store without crash/power-loss
durability, one Linux-kernel/local-filesystem Store access contract, and no
history GC. It is not the default Mac/SSD/SSH storage adapter.

Prefer native pnpm/uv sharing and the existing Go/Node clone wrappers first.
Investigate a permissive chunk-transfer engine such as desync only if measured
closed-generation transfer/dedup bottlenecks justify another representation.
Linux container snapshotters/lazy image formats can inform a future container
adapter; they do not establish native macOS worktree compatibility. Other
projects' reported benchmarks remain their evidence, not LayerCache savings.

Do not infer permissive embedding from the words open source. Pin selected
source and review the actual dependency graph/licenses, notices, runtime/toolchain
compatibility, maintenance and security before adoption. GPL/LGPL/native system
tools can be research or external-client references without being a permissive
vendored library. This design adds no dependency, copied engine code or daemon.

## Milestones and acceptance

All runtime milestones remain open in [TODOs](../TODOs.md). M0 records design
research only. Dependencies are delivery gates, not dates or permission to
change user hardware. Each gate needs source qualification, a verified installed
release, and actual workload acceptance separately; hosted rollout adds another
receipt where relevant.

| Milestone | Owner and dependency | Completion target |
| --- | --- | --- |
| M0: design/source audit | Product + storage/native; this documentation change | Record current source, upstream revisions/licenses, OSS policy, unsupported behavior, interfaces and acceptance; no runtime savings/adoption claim |
| M1: one bounded local owner through Turbo | Storage/native; M0 | Share compatible work across independent worktrees, remove duplicate cache ownership/budgets, preserve scoped references and qualify safe migration |
| M2: optional SSD pool | Storage/CLI; M1 lifecycle/accounting | Verified enrollment/offload/direct reads, graceful absence/reconnect, and sustained measured reduction in internal allocation |
| M3: one free OSS SSH peer + VM client | CLI/auth/native; M1, M2 when needed for SSD-backed VM | Exact compatible producer/consumer reuse without LayerCache signup; scoped credentials, bounded fallback and honest physical accounting |
| M4: immutable expanded generations and native CoW | Materialization/native owner; M1 plus measured extraction/copy bottleneck | One leased read-only baseline or independent clone/copy per writer, with write isolation and demonstrated net footprint/time tradeoff |
| M5: conditional OSS workspace-engine experiment | Storage/native research; M4 evidence and explicit workload | Decide whether a pinned engine adds enough value under platform, durability, GC/quota, semantics and license gates; adopt none by default |

M3 need not wait for all removable-storage work when qualifying an independent
remote host. Gradle forwarding follows the complete Turbo path. Other native
adapters, automatic managed coordination and trusted Public production keep their
own acceptance gates; they cannot be closed by a generic storage fixture.

### M1-M3 qualification matrix

| Control | Required evidence |
| --- | --- |
| Native correctness | Independent empty consumer restores equivalent outputs; changed input/profile/toolchain/target misses; actual assertions/status retained |
| Scope isolation | Same project/native key under another origin or authority cannot resolve data; digest possession grants no access; no private-to-Public promotion |
| Dedup/concurrency | Equal authorized payloads deduplicate within declared scope; concurrent worktrees single-fetch; one cancelled waiter does not harm others |
| Corruption/failure | Reject partial/corrupt bytes and incomplete native graphs; interrupted publication never becomes a hit |
| Capacity/leases | Include staging, metadata, readers, upload pins and migration overlap; concurrent quota pressure refuses safely; active-reader GC remains correct |
| SSD identity | Absent startup, renamed mount, replacement at same path, reformat/duplicate marker, read-only/full volume and writable underlying mountpoint |
| SSD interruption | Remove during lookup, verification, transfer, commit and read; reconnect/restart recovers without publishing partial results or internal mountpoint spill |
| Remote authority | Read-only and writer roles, expired/known-revoked capability, SSH host identity change, slow/offline peer and cancelled transfers; documented revocation limits |
| VM ownership | Host/VM cannot own one pool concurrently; compatible native reuse proven rather than assumed from repository name |
| Lifecycle/migration | Crash at every transfer/import phase; rollback preserves legacy source; unattributable hash-only archives cold-miss, scoped snapshot origin/auth preserved; old config and remote-only behavior retained |
| Standalone availability | Internal, SSD and one peer operate without cloud account; absent optional cache preserves native build usability and native failure status |

### M4-M5 qualification matrix

| Control | Required evidence |
| --- | --- |
| Expanded identity | Safe manifest/metadata policy, bounds, case collisions, traversal/symlinks and special-file refusal; no live-directory snapshot assumption |
| Shared-path lifetime | Two declared read-only consumers reuse one generation; live/child consumers, cancellation, process exit, PID reuse and restart do not cause premature GC; direct SSD paths opt into a connected lifetime and removal surfaces explicit failure |
| Write isolation | Write, truncate, writable mmap, chmod/xattr, rename/delete and source-replacement attempts leave canonical tree and other consumers intact |
| Hardlink refusal | Reject writable output aliases and mutable-source ingestion; changing read-only bits or using an existing writer cannot corrupt the canonical baseline |
| Clone truth | Actual APFS/Btrfs/XFS pairs qualified; distinct inode/metadata and supported clone outcome recorded; ext4/cross-volume require-clone refusal explicit |
| Fallback/admission | Unsupported clone may copy only under policy and fresh reservation; permission/quota/I/O failures preserved; mixed-tree outcomes reported |
| Divergence/ENOSPC | Concurrent dirty clones and growth bounded as declared; later low-space failure preserves baseline and surfaces native failure; snapshots do not produce false reclaimed bytes |
| Whole-operation benefit | Include extraction, clone/copy, verification, cleanup, packed/expanded baselines and dirty deltas; retain slower or larger observations |
| Engine-specific gate | Pin source/dependencies; qualify native semantics, storage limits/GC, durability/recovery, mount/unmount, auth separation and platform; unsupported capability is an explicit no-go |

### Measurement and go/no-go

First choose one real workload and record its source/native profile, internal
filesystem, SSD capability, network and existing native-cache policy. No user
device inventory or baseline was gathered for this design. Numerical capacities
and acceptable regression envelopes are chosen from that evidence before the
runtime pilot, not invented as savings targets here.

Compare equivalent internal-only native-warm, fresh-output, first offload,
SSD-connected repeat, SSD-absent, peer-connected and peer-outage operations.
Use the [complete-operation recorder](operation-measurement.md) where practical:
prepare/authenticate, restore, native work, close and publication. Report migration
and bootstrap separately; never trigger extra application builds just to fill
charts. Retain negative observations, sample spread and unmatched contexts.

Acceptance needs equivalent outputs/status and safe recovery first, then
sustained positive net internal allocation reduction across repeated sessions.
Measure peak internal/SSD/host headroom and total physical footprint separately
from internal reclamation. A storage improvement with slower full commands must
be reported as a measured tradeoff; claim a speedup only for a matched observed
comparison. No hit-count-derived CPU, price, portability, backup or fleet-wide
savings claims. Independent review and normal required repository checks precede
implementation delivery; no runtime tests or hardware trials belong to M0.

M0 documentation receipt, 2026-10-23: source-alignment and acceptance/policy
reviews have no remaining findings after clarifying direct-SSD path lifetime
and provenance-safe legacy import. All 142 local links/anchors across the eight
affected Markdown files, code fences and whitespace checks pass. These checks
qualify the documentation only. No implementation, dependency installation,
runtime test, benchmark, user-storage scan, mount or SSH configuration was made.
