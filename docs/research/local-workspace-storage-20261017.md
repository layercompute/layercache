# Local compute and storage reuse decisions

Checked 2026-10-17. LayerCache prioritizes repeated work that native clients can
identify precisely, with bounded storage and ordinary failure recovery. This
note records decisions and evidence. Outstanding qualifications and acceptance
targets belong in [TODOs](../../TODOs.md).

## Ranked product choices

1. **Reuse completed immutable outputs through native keys.** Skipping an
   expensive execution has the broadest potential benefit. Preserve the native
   client's complete declared inputs, toolchain/profile and access boundary.
   Gradle already supports reusing task outputs across checkout locations;
   correctness depends on complete input/output declarations. A restored task
   output still occupies its consumer's filesystem. Measure full command time,
   including lookup, verification, transfer and publication, before claiming
   savings. [Gradle build cache](https://docs.gradle.org/current/userguide/build_cache.html)

2. **Keep one bounded canonical local artifact and reuse native package
   stores.** Multiple native keys may refer to identical immutable payloads
   within their authorized scope. Avoid extra snapshots of content the native
   tool already shares. pnpm's content-addressed store already shares package
   files across installs; placing its store on another filesystem causes copies
   instead of hardlinks. A per-workspace install can still have its own
   dependency graph. Existing sharing must be measured before counting it as a
   new LayerCache saving. [pnpm layout](https://pnpm.io/symlinked-node-modules-structure),
   [store placement](https://pnpm.io/settings/store)

3. **Bound run-owned generations and their lifetimes.** Mutable outputs,
   temporary extraction and retained build slots need explicit owners, byte
   limits and completion rules. A reusable artifact and a run's writable state
   have different lifetimes. Only the owner may retire a closed generation;
   active readers retain their leases. Native dependency caches and project
   state also need their own budgets. Gradle distinguishes shared user-home
   caches from project-local incremental/configuration state, and documents
   locking constraints for concurrent cache access.
   [Gradle directories](https://docs.gradle.org/current/userguide/directory_layout.html),
   [dependency cache](https://docs.gradle.org/current/userguide/dependency_caching.html)

4. **Share writable views only through a qualified native clone path.** This
   remains a conditional product choice. Independent inodes with filesystem
   CoW can preserve a cached baseline while a consumer writes. Btrfs supports
   reflinks; XFS requires reflink-enabled formatting; APFS supports same-volume
   clones. Actual source/destination placement must support the operation.
   Explicit ownership and separate correctness/performance qualification are
   required before adoption. [Btrfs reflinks](https://btrfs.readthedocs.io/en/latest/Reflink.html),
   [XFS format](https://www.man7.org/linux/man-pages/man8/mkfs.xfs.8.html),
   [APFS clones](https://developer.apple.com/documentation/foundation/about-apple-file-system)

## Evidence limits and excluded defaults

An exploratory ext4 fixture rejected native `FICLONE`. This agrees with
upstream ext4's missing remap operation. A library cannot add that filesystem
capability. Forced cloning must fail visibly; an automatic copy fallback proves
neither block sharing nor storage savings.
[ext4 source](https://raw.githubusercontent.com/torvalds/linux/master/fs/ext4/file.c),
[Linux clone API](https://www.man7.org/linux/man-pages/man2/FICLONE.2const.html),
[GNU copy policy](https://www.gnu.org/s/coreutils/manual/html_node/cp-invocation.html)

An existing FUSE view isolated one write in a 1 MiB exploratory fixture, but
copied the whole file. That establishes only the fixture's observed behavior.
Kernel OverlayFS also documents file copy-up, private upper/work directories
and immutable mounted lower trees. Rootless operation adds host-policy and
mount-lifecycle requirements. A general overlay framework, filesystem migration
and transparent mutable deduplication are outside the selected defaults.
[OverlayFS contract](https://www.kernel.org/doc/html/latest/filesystems/overlayfs.html),
[fuse-overlayfs requirements](https://github.com/containers/fuse-overlayfs)

Hardlinks do not isolate writable build outputs. Both names refer to the same
file, including its data and permissions. pnpm exposes clone/copy policies for
consumers requiring package-file mutation isolation; those policies do not make
ext4 support clones. [Linux hardlinks](https://www.man7.org/linux/man-pages/man2/link.2.html),
[pnpm import methods](https://pnpm.io/settings/node-modules)

## First capability: read-only storage inspection

The initial capability inventories explicitly selected roots:

```sh
layercache storage inspect --path /absolute/workspace --path /absolute/build-slots --json
```

The default output is human-readable. JSON contains roots and filesystem
capacity/availability, aggregate and category counts, hardlink aliases, skipped
mounts, issues and completeness. The command performs no configuration/cache
initialization or network access. It does not create clones, remove files,
mount filesystems or change cache policy.

On supported platforms, inspection counts each device/inode once across the
selected roots, including overlapping roots. It reports apparent bytes and
allocated bytes separately, with sparse-file indicators. Allocated bytes use
filesystem block counts; compression can also make allocation smaller than
apparent size. These totals are not exclusive physical usage, since distinct
reflinked files may share blocks. They also establish no reclaimable byte count.
[file metadata](https://www.man7.org/linux/man-pages/man3/stat.3type.html),
[filesystem capacity](https://www.man7.org/linux/man-pages/man2/statfs.2.html)

Traversal does not follow symlinks or descend into nested mounts; a mounted root
must be selected explicitly. A symlink root is counted without visiting its
target; a root reached through a symlink ancestor produces an incomplete report.
Linux mount-table checks also identify nested bind mounts sharing a device
number, including mounted files. Explicitly selected mounted files remain
inspectable. Categories use the path within its selected root and that root's
basename; ancestors outside the root do not supply hints. An inode's first
encountered path determines its category, with roots and entries visited in
lexical order. Categories never confer deletion authority.
[Linux mount information](https://www.man7.org/linux/man-pages/man5/proc_pid_mountinfo.5.html)

A live scan is not an atomic snapshot. Disappearing or inaccessible entries,
unsupported capabilities and other traversal failures remain visible as a
partial report. Incomplete inspection exits nonzero while preserving partial
JSON. The result identifies where storage accounting needs attention; it does
not certify cleanup safety or a compute/storage saving.
