# Where LayerCache runs

Execution inventory answers which clients and CI jobs contacted LayerCache,
which projects they used, and what resources were visible to those processes.
It does not register machines for remote control or establish provider charges.

## Identity and authority

A persistent CLI installation uses one randomly generated identity outside its
repository checkouts. Its reported identity is scoped to the backend origin;
the backend binds it to the verified user's numeric identity. Five project
configurations and several worktrees can therefore appear as one installation.
Separate OS users remain separate installations. Copying a home directory can
copy its identity too, so this is not a hardware fingerprint or a count of
physical computers. The client sends no hostname, IP address, MAC address,
hardware serial number, raw filesystem path or machine ID.

Standalone Local operation needs no cloud account. Its existing locally signed
run credential authorizes a separate local installation identity. Local
installations share a reserved local scope across project listener ports;
hosted backends reject that local owner namespace. A new CLI first checks the
local runtime's advertised feature, so an older daemon keeps its original
credential shape and cache behavior.

GitHub Actions jobs use the verified repository ID, run, attempt and check-run
ID. Several native cache actions in that job share one execution identity.
Independent jobs remain separate even if they have the same runner name. A job
does not establish whether its physical worker was reused. The orchestrator is
GitHub Actions; a compute provider such as Ubicloud is a separate fact. The UI
distinguishes a provider claim from information supplied by signed GitHub
identity claims.

Resource observations use existing project authorization. A read-only PR job
can report its own observation without gaining artifact publication authority.
The server supplies execution and project identity from the signed capability,
and checks current membership. The report cannot select another job or owner.
An observation associated with recorded cache activity is labeled separately
from a client that has only authenticated.

## Resource facts and freshness

CPU values describe logical processors visible to the process, affinity and
known CPU quotas. Effective capacity is reported only when the client can
establish the relevant limits. Unknown limits remain unknown. Memory follows
the same rule for visible memory and container limits. These are client-reported
resource views, not an attestation of provider hardware.

Filesystem observations distinguish:

- Capacity, the filesystem's total size.
- Free bytes, including filesystem-reserved free space.
- Available bytes, the free space available to the reporting user.
- Used bytes, capacity minus free bytes.
- Reserved bytes, free bytes minus available bytes.

Workspace and cache paths on the same filesystem share a locally derived opaque
filesystem identity. The dashboard does not add that capacity again for each
project. These filesystem totals are different from cache blob bytes, staging,
expanded outputs and provider invoices. Neither filesystem totals nor standalone
directory sizes establish reclaimable storage.

The inventory shows when the server received an observation. Old samples are
labeled stale, rather than offline. Observation history has age and count
bounds, with truncation visible. It is independent of cache eviction and
retained-artifact obligations. Old servers and clients can omit this inventory;
an unavailable projection does not become a claim of zero usage.

## Collection and compatibility

The backend advertises the observation protocol in a successful credential
exchange. A supporting client then sends a bounded, allowlisted snapshot using
that exchange's scoped native credential. Authentication request bodies and
native cache keys do not change. Collection or reporting failure cannot fail the
underlying cache setup or build. Start and finish observations are samples, not
continuous machine monitoring.

The protocol is described by `internal/execution`. Its request body is at most
16 KiB and contains at most four filesystem observations. The existing
measurement repository stores inventory independently of task outcomes. The
local CLI dashboard and authenticated web dashboard use the same presentation.
Source tests, installed-client behavior and live deployment are separate
qualification stages; consult [TODOs](../TODOs.md) for their current state.

An older backend rejects the additional execution context in newly issued
credentials. Rolling back the backend therefore requires clients to exchange
fresh credentials against that older version, or wait for the newer credentials
to expire. Old credentials continue to work on the newer backend. This is an
additive metadata schema, but it is not transparent in-flight token rollback.

## Local residency strategy

Compatibility and storage identity have different jobs. A logical cache
reference keeps the complete native input identity and any required target,
architecture, ABI, SDK and toolchain dimensions. Inventory hardware fields do
not participate in those keys. Native outputs depend on their target platform,
which can differ from the host executing the compiler. A portable task may
share a logical identity only under an explicitly qualified contract covering
all its declared inputs and dependencies. Current measured runtime profiles
conservatively separate platforms; this feature does not remove that isolation.

Different logical references can still point at one SHA256 payload. The Local
store already deduplicates equal bytes within one store root. Hosted storage
does so within one project's object namespace across its profiles. Separate
project namespaces remain isolated; this is not global cross-project
deduplication. Equal unpacked files inside different archive bytes are also not
the same stored payload. Avoiding unnecessary logical dimensions and reducing
duplicate payload bytes need separate qualification.

R2 remains the persistent hosted byte store. A local hot cache should have one
explicit machine budget shared across authorized project routes. Reuse the
existing content-addressed artifact store, verified read-through, LRU and reader
protection. Preserve backend, project, visibility, integration and profile
authority when resolving every logical reference, even when payload bytes are
identical. Cloud-only operation remains useful for disposable or disk-constrained
workers.

The first target is Turbo's existing complete read-through path. Gradle needs
the missing local forwarding connection before it can use the same policy.
Native build tools keep ownership of their task identities and extraction
semantics. A first consumer should prove one remote payload fetch across two
concurrent worktrees, valid residency after restart, corruption recovery and
bounded admission under pressure.

A resident archive avoids another download. It does not eliminate copies
extracted into writable workspaces. Prefer a leased canonical immutable path
when a consumer only reads a final product. Writable destinations need isolated
copies or native CoW clones whose mutation isolation was verified. Hardlinks
must not expose cached bytes to arbitrary writers. Keep existing package-manager
deduplication rather than adding another package snapshot layer.

The budget must include metadata, staging, queued uploads and blocks held by
active readers. The existing `maxBytes` limits committed blob bytes; it is not a
ceiling on all physical disk allocation. Expanded output copies need separate
ownership and accounting. Rollout requires complete command time and allocated
byte measurements, rather than adding apparent sizes from overlapping trees.
See the [storage decision](research/local-workspace-storage-20261017.md) for the
existing native-clone and rootless-overlay limits.
