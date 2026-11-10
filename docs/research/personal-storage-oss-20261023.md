# Personal storage and OSS sharing audit

Checked 2026-10-23. This is source research and a qualification proposal, not an
implementation or a measured saving. Outstanding acceptance belongs in
[TODOs](../../TODOs.md); the proposed product contract is in the
[personal storage design](../personal-storage-design.md). Read the existing
[local storage decision](local-workspace-storage-20261017.md) with this note.

The useful promise is a smaller bounded internal working set and reuse of
compatible immutable work from explicitly configured storage. Moving a cache
archive does not remove its restored outputs, a package manager's store, or a
VM's writable disk. An attached SSD can avoid a remote transfer or rebuild;
neither its capacity nor a cache hit proves that it beats internal storage.
Acceptance needs complete command time and internal allocated bytes, including
temporary peaks and later growth.

## Scope and source receipt

Only a new source checkout and this research note were created for this audit.
No upstream executable, build, test, installation, container, mount, SSH session
or live storage scan was run. Source test files and upstream benchmark receipts
were read; their presence does not constitute independent execution.

The GitHub link on [layerfs.ai](https://layerfs.ai/) identifies
[Ephemeral-AI-Lab/layerfs](https://github.com/Ephemeral-AI-Lab/layerfs) as upstream.
The isolated source checkout was detached
at `3690e8bd54e27feb7ae505b363acd94228799c7a`, a 2026-10-23 documentation commit.
The advertised `v1.0.0` tag resolves separately to
`2aaf2552ce21154928b9f9c83582e41a637f5910`. Unless specified otherwise, LayerFS
source links below pin the inspected checkout, not the moving default branch.

The root [LICENSE][lfs-license] is MIT, copyright 2026 Ephemeral-AI, and the Rust
workspace declares MIT. This is a permissive upstream seam, not a completed
transitive dependency or distribution-notice audit. The release is
[source-only][lfs-release], requiring version-matched SDK, native binding and
daemon; no upstream installation was attempted.

## What LayerFS actually implements

LayerFS is more capable than a full-file copy-up wrapper. A Branch fork changes
metadata, and a mounted Workspace combines an immutable committed tree with its
private mutable overlay. A write records the changed window without reading the
uncovered base payload. At Commit, the captured-file script distinguishes
inherited spans from replacement bytes and passes localized edits to the content
engine. This is application-level CoW over LayerFS objects, rather than an APFS
or Linux reflink requirement. It can therefore share unchanged bytes while
running on ext4. [Fork][lfs-fork], [write operation][lfs-write],
[captured edit script][lfs-script]

Large-file construction uses a streaming content-defined GEAR chunker with
8 KiB minimum, 16 KiB target and 32 KiB maximum chunks. Canonical object identity
is domain-separated BLAKE3; repeated canonical IDs are reused. Physical encoding
adds zstd and bounded predecessor-based delta selection. These are distinct
mechanisms: sharing an immutable base avoids initial copies, exact CAS reuse
deduplicates matching objects, and compression/deltas reduce physical
representation. They do not certify native build compatibility or exclusive
reclaimable allocation. [Chunker][lfs-chunker], [identity][lfs-identity],
[exact reuse][lfs-reuse], [codec][lfs-codec], [delta selection][lfs-delta]

The content crate exposes caller-supplied authenticated object providers and
finalized-object consumers without requiring a database or mount. The storage
crate has a `PackPersistence` port with immutable authenticated pack reads and
atomic publication. Those are real code boundaries worth studying. They are
Rust interfaces inside this product, not an existing Go LayerCache adapter or a
demonstrated remote storage service. The smallest future experiment should use
the version-matched upstream SDK in an isolated Linux environment before
considering new bindings or copying algorithms. [Content surface][lfs-content],
[persistence port][lfs-port]

## Why LayerFS is not the personal storage router

| Finding | Source evidence | Consequence for LayerCache |
| --- | --- | --- |
| Direct Store access is Linux-only; its directory must be on ext4, XFS or Btrfs. | The platform/filesystem guard refuses other systems before opening the Store. [Guard][lfs-guard] | No direct native APFS Store, Mac-to-SSD filesystem pool, SSHFS or NFS Store sharing. |
| Every opener must share the Store's Linux kernel and locking domain. | Guard contract; Store calls open the same local file in-process. [Guard][lfs-guard], [SDK guide][lfs-sdk] | Different physical machines or host/VM kernels cannot open one live SQLite Store safely by sharing a path. Remote agents may execute in a common Linux environment, but that is a different topology. |
| The active Store profile is Disposable. | `open_direct` explicitly selects Disposable; SQLite profile maps it to WAL with `synchronous=0`. Init makes the same selection. [Open][lfs-open], [profile][lfs-profile], [Init][lfs-init] | Do not use it as the only copy of source/history or as a retained handoff service. A compiled Durable enum does not establish a supported durable product. |
| Published packs cannot be updated or deleted through the active schema. | Immutable pack triggers. [Schema][lfs-schema] | A byte-bounded evictable cache cannot be assumed. History deletion/GC and reference closure need a separate contract; deleting a historical file does not remove its old content. |
| Each project is initialized into its configured project volume and Store path. | SDK project receipt and implementation. [SDK guide][lfs-sdk], [Init][lfs-init] | Shared chunks across Workspaces in that Store are supported by the model; machine-wide cross-project deduplication is not established by this audit. |
| FUSE is intentionally incomplete. | xattrs and fsync callbacks are absent; node grammar excludes sockets/FIFOs/devices; ownership is the configured runtime identity. [Callbacks][lfs-callbacks], [unsupported operations][lfs-unsupported] | An arbitrary macOS tree, Xcode/signing workload or process/database snapshot is not a qualified target. |

The [platform guide](https://layerfs.ai/docs/platform/) independently disclaims
power-loss durability, lists one-kernel local storage, and says history GC and
per-Workspace/Commit size caps are absent. It describes macOS use through a
Linux container and Docker-owned storage. Placing the VM's backing disk on an
SSD could relocate bytes, but neither missing-SSD behavior nor safe physical
reclamation follows from LayerFS's Workspace API.

Source inspection found documentation drift. The persistence crate README still
describes a former macOS FULL profile, while current callers and connection code
select Linux Disposable. Older architecture sections also describe superseded
SDK and writer-admission behavior. The current SDK guide, pinned call sites and
guard decide this audit's conclusions; an older design paragraph is not a
supported fallback. [Historical persistence README][lfs-persistence-readme],
[current open][lfs-open], [current Init][lfs-init]

Upstream reports deduplication, filesystem integrity and performance suites, but
labels its release results as one observation per case on a Linux container on
an ARM Mac. Some I/O cases are slower than its baseline. These are useful
upstream evidence and fixtures, not LayerCache qualification, native Mac
performance, a user workload forecast or proof of durable operation.
[Release measurements][lfs-results]

LayerFS also captures ignored files and generated dependencies as part of its
full tree. That explains how a mounted Workspace can avoid reinstalling them,
but it increases the authority and lifecycle scope relative to a native task
cache. A future pilot needs a deliberately prepared secret-free source, external
credentials and an explicit manifest; do not import a real developer's entire
working tree by default. The importer enumerates directory children without a
Git-ignore filter. [Initialization scan][lfs-scan]

## Native sharing before a new workspace filesystem

| Mechanism | Useful seam and limits | Proposed position |
| --- | --- | --- |
| Git worktrees | Git shares repository objects while retaining per-worktree checkout/index state. It does not make generated build trees shared. [Worktrees](https://git-scm.com/docs/git-worktree), [repository layout](https://git-scm.com/docs/gitrepository-layout) | Preserve the existing native owner. Keep Git as an external tool; its GPL source is not a permissive library-copy seam. |
| pnpm 10.x store | Native content store and clone/hardlink/copy import. Keeping store and installs on different filesystems forces copies rather than hardlinks. [Pinned importer](https://github.com/pnpm/pnpm/blob/v10.34.5/fs/indexed-pkg-importer/src/index.ts), [10.x settings](https://pnpm.io/10.x/settings) | Configure and qualify the package manager; do not add a second archive copy without evidence. |
| pnpm global virtual store | Experimental `enableGlobalVirtualStore` shares expanded dependency-graph-addressed package directories, with project links; disabled in CI and subject to version-specific module-resolution limits. [10.x contract](https://pnpm.io/10.x/settings#enableglobalvirtualstore) | A promising opt-in real-project experiment. Never point every worktree at one arbitrary mutable `node_modules` directory. |
| Gradle | Native user-home caches differ from project-local incremental/configuration state. Writable dependency-cache concurrency requires communicating processes; prepared read-only `GRADLE_RO_DEP_CACHE` is a separate supported seam. [Directories](https://docs.gradle.org/current/userguide/directory_layout.html), [dependency caching](https://docs.gradle.org/current/userguide/dependency_caching.html) | Reuse native HTTP task caching first; host/VM consumers keep isolated writable state. |
| Xcode DerivedData | Xcode permits choosing its location, but that alone does not make concurrent writable DerivedData a valid shared cache. [Xcode location settings](https://help.apple.com/xcode/mac/current/en.lproj/deva52afe8a4.html) | Explicit placement and native compiler-cache qualification remain separate from arbitrary shared output directories. |
| Shared expanded immutable artifact | A consumer may use one expanded baseline only when its interface explicitly accepts an immutable path and its lease protects the whole graph. | Narrow optional optimization. A consumer that writes, chmods or signs its output needs private materialization. |
| Writable materialization | Native CoW gives independent inode semantics where the actual source/destination filesystem supports it; otherwise use an isolated admitted copy. | Qualify native clone APIs before a general FUSE workspace product. |

pnpm's inspected `v10.34.5` [license is MIT](https://github.com/pnpm/pnpm/blob/v10.34.5/LICENSE).
Do not transfer that blanket description to every directory of a future version;
current upstream separately identifies additional component terms. Gradle's
[pinned source license](https://github.com/gradle/gradle/blob/v9.3.1/LICENSE) is
Apache-2.0 with component notices. Version and dependency notices remain part of
any integration decision.

## CoW, hardlinks and filesystem boundaries

Apple's native `clonefile` provides distinct metadata and private subsequent
writes, but returns `EXDEV` across filesystems and `ENOTSUP` for unsupported
capabilities. It does not share extents from the internal Mac disk to a separate
SSD. Even a successful clone can encounter `ENOSPC` on later modification.
`COPYFILE_CLONE` may fall back to copying; `COPYFILE_CLONE_FORCE` and
`COPYFILE_STATE_WAS_CLONED` let an adapter distinguish real cloning from that
fallback. A tree still requires correct entry, metadata, symlink and publication
handling. [Apple clonefile](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/man/man2/clonefile.2),
[Apple copyfile](https://github.com/apple-oss-distributions/copyfile/blob/main/copyfile.3)

Linux `FICLONE` likewise needs a qualified same-filesystem path. A per-file clone
does not create an atomic multi-file snapshot. Btrfs has mount/flag constraints;
XFS must support reflinks. Ordinary upstream ext4 has no `remap_file_range`
operation, so a library cannot add filesystem-native clones there. Report an
explicit copy fallback and admit its additional bytes before publication.
[FICLONE](https://man7.org/linux/man-pages/man2/FICLONE.2const.html),
[Btrfs](https://btrfs.readthedocs.io/en/stable/Reflink.html),
[XFS formatting](https://kernel.googlesource.com/pub/scm/fs/xfs/xfsprogs-dev/+/master/man/man8/mkfs.xfs.8.in),
[ext4 operations](https://github.com/torvalds/linux/blob/master/fs/ext4/file.c)

The existing `golang.org/x/sys` seam already supplies Darwin `Clonefile` and
Linux `IoctlFileClone`; a new cloning framework is not a prerequisite. Node's
`COPYFILE_FICLONE_FORCE` is another native refusal seam where the existing native
client is JavaScript. Node `copyFile` is not an atomic publication primitive.
[x/sys/unix](https://pkg.go.dev/golang.org/x/sys/unix),
[pinned Node API](https://nodejs.org/download/release/v24.13.0/docs/api/fs.html#fspromisescopyfilesrc-dest-mode)

Hardlinks share data and permissions. An owner can undo `0444`, and a writable
mapping can modify the shared inode. Consequently a chmod-only convention does
not isolate arbitrary same-user writable outputs. Let native package managers
own their hardlink contracts; never hardlink arbitrary build materializations.
Allocated block counts also do not measure exclusive reclaimable bytes once
snapshots or clones share extents. [Hardlinks](https://man7.org/linux/man-pages/man2/link.2.html),
[chmod](https://man7.org/linux/man-pages/man2/chmod.2.html),
[mmap](https://man7.org/linux/man-pages/man2/mmap.2.html),
[Btrfs accounting](https://btrfs.readthedocs.io/en/latest/Qgroups.html)

## Other OSS seams worth comparing

These are candidates, not dependencies selected by this note. Licenses below
describe the linked upstream boundary; any adoption needs an exact commit and
transitive/platform review.

| Candidate | Source and license | Narrow fit; reason to defer broader use |
| --- | --- | --- |
| uv | [Cache contract](https://docs.astral.sh/uv/concepts/cache/), [MIT/Apache-2.0](https://github.com/astral-sh/uv/blob/main/README.md) | Preserve native cache placement, link mode and pruning for Python workloads. Do not mutate its store directly. |
| desync | [Store chains](https://github.com/folbricht/desync/blob/master/docs/stores.md), [README](https://github.com/folbricht/desync/blob/master/README.md), [BSD-3-Clause](https://github.com/folbricht/desync/blob/master/LICENSE) | Go chunked closed-artifact transfer with local/remote stores and seeds. Useful only if repeated transfer/representation cost warrants chunking; not a general writable workspace. Check Go-version compatibility and macOS metadata limits at a selected commit. |
| rclone | [VFS behavior](https://rclone.org/commands/rclone_mount/), [MIT](https://github.com/rclone/rclone/blob/master/COPYING) | Existing transfer/operator tool. VFS limits are soft for open files and concurrent shared-cache use can corrupt data; a mount is not LayerCache's bounded artifact/authority contract. |
| Nydus | [Service boundary](https://github.com/dragonflyoss/nydus/blob/master/service/README.md), [Mac limits](https://github.com/dragonflyoss/nydus/blob/master/docs/nydus_with_macos.md), [Apache-2.0 OR BSD-3-Clause](https://github.com/dragonflyoss/nydus/blob/master/Cargo.toml) | Reusable chunk/lazy-read image machinery for a later Linux container experiment. Service library is Linux-only; Mac daemon documentation is not native APFS workspace qualification. |
| stargz-snapshotter | [Install targets](https://github.com/containerd/stargz-snapshotter/blob/main/docs/INSTALL.md), [Apache-2.0](https://github.com/containerd/stargz-snapshotter/blob/main/LICENSE) | Lazy container-image consumption through Linux container runtimes; does not solve native Mac generated-output placement. |
| Composefs / casync | [Composefs license breakdown](https://github.com/composefs/composefs/blob/main/COPYING), [casync source](https://github.com/systemd/casync) | Useful architectural references. Do not call them blanket permissive libraries or substitute their image/transfer model for writable isolation. |

Chunking an already compressed native cache archive may expose little reusable
content. Measure representative equal/near-equal artifacts before introducing a
second encoding, index, transfer protocol or garbage collector. Byte identity
never permits bypassing project/profile/native-key/visibility authorization.

## Proposed order and acceptance evidence

1. Finish one local artifact owner and budget through the existing Turbo path,
   including archive/extraction ownership. Preserve native package stores and
   each writable consumer's state.
2. Add an explicitly configured optional SSD pool for reconstructible cache
   bytes, with direct authorized reads and bounded internal promotion. Qualify
   missing/wrong volumes, removal mid-transfer, reconnect, restart, corruption,
   admission and active readers before claiming offload.
3. Qualify one manually configured self-hosted private endpoint. SSH access and
   network reachability do not establish native compatibility, retention or a
   safe shared mutable directory. Mac/VM/Linux clients exchange compatible
   immutable outputs while keeping local writable stores independent.
4. Test one native sharing opportunity only after the first three measurements
   show the remaining cost: pnpm's native expanded store or same-volume clone
   materialization is a smaller seam than adopting a new workspace filesystem.
5. Consider LayerFS only as a separate source-pinned Linux workspace experiment
   on disposable, secret-free generated trees. It must not block or replace the
   simpler personal-storage milestones.

Every materialization experiment needs equal baseline/consumer outputs;
changed-input misses; two independent consumers; write, truncate, rename,
delete and chmod isolation; explicit xattr/ACL/symlink behavior; cancellation,
restart and quota-pressure cleanup; and leases that cover active child readers.
Unknown/preexisting user paths are never adopted or deleted. CoW fallback is
accounted as a copy. Measure packed and expanded baselines, overlays, metadata,
staging, retained readers and dirty deltas together, distinguishing logical,
allocated and exclusive/reclaimable values.

For a LayerFS pilot, additionally require declared POSIX feature compatibility,
all Store openers in one qualified Linux kernel/filesystem, bounded whole-project
retirement, and an independently retained copy of anything unreconstructible.
Its append-only historical model cannot become LayerCache LRU by deleting live
chunks. No automatic workload migration, native Mac support, multi-host Store,
durability, storage reduction or compute saving is established by this audit.

[lfs-license]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/LICENSE
[lfs-release]: https://github.com/Ephemeral-AI-Lab/layerfs/releases/tag/v1.0.0
[lfs-fork]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-api/src/branch/fork.rs#L1-L12
[lfs-write]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-workspace/src/operations/file/write.rs#L68-L87
[lfs-script]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-workspace/src/construction/captured/script.rs#L1-L112
[lfs-chunker]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-content/src/file/cdc/gear.rs#L1-L47
[lfs-identity]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-content/src/object/id.rs#L1-L35
[lfs-reuse]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-storage/src/save/wave.rs#L38-L109
[lfs-codec]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-storage/src/encoding/codec.rs
[lfs-delta]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-storage/src/encoding/delta/select.rs
[lfs-content]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-content/README.md
[lfs-port]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-storage/src/port/persistence.rs#L84-L145
[lfs-guard]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-store-service/src/kernel_guard.rs#L1-L65
[lfs-open]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-store-service/src/store_service.rs#L74-L111
[lfs-profile]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-persistence/src/backend/sqlite/profile.rs#L9-L42
[lfs-init]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/sdk/js/native/src/project.rs#L1-L124
[lfs-scan]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-project/src/import/scan.rs#L179-L229
[lfs-schema]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-persistence/sql/sqlite/objects_units.sql#L18-L25
[lfs-sdk]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/sdk/js/README.md
[lfs-callbacks]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-fuse/src/request/callbacks.rs#L276-L325
[lfs-unsupported]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-fuse/src/operations/unsupported.rs#L17-L42
[lfs-persistence-readme]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/core/crates/layerfs-persistence/README.md
[lfs-results]: https://github.com/Ephemeral-AI-Lab/layerfs/blob/3690e8bd54e27feb7ae505b363acd94228799c7a/README.md#results
