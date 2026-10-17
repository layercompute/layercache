# Keep caches bounded and useful

A fast cache that grows without a limit eventually becomes an operational
problem. Set byte limits and reserve free space before collecting more kinds of
outputs. Treat retained release data and optional reusable work separately.

## Account for the complete footprint

Distinguish logical artifact size, transferred archive size and physical disk
usage. Compression, deduplication and multiple keys make those different
numbers. Include metadata, temporary uploads, extraction, deletion backlog and
native caches outside the service's directory.

LayerCache's `--max-size` bounds committed Local artifact bytes, rather than
every byte on the host. Its free-space checks and bounded staging have separate
contracts. A compiler's private disk cache, package store, DerivedData or Nx
filesystem cache may remain outside that budget. Inventory and bound those
owners instead of assuming the daemon's limit covers them.

The account-free CLI can inspect explicit roots without opening, repairing or
evicting a cache:

```sh
layercache storage inspect --path /absolute/workspaces --path /absolute/build-slots --json
```

It counts hardlinked inodes once across the selected roots and separates
apparent size from allocated blocks. Sparse files can have a large apparent
size and little allocation. Path categories are hints; they do not establish
that a directory is finished or safe to delete. Errors and unsupported
capabilities produce a partial report and a nonzero exit. Use a filesystem's
native tools when already available; LayerCache's report gives automation one
portable schema without changing the inspected data. It neither scans content
for duplicates nor reports bytes that deletion would reclaim.

The [local storage decision](../research/local-workspace-storage-20261017.md)
ranks immutable output reuse and bounded run-owned storage ahead of writable
workspace sharing. CoW support depends on the actual filesystem; a copy fallback
does not save blocks. Keep native package-store sharing and account for each
mutable build slot separately.

Use an owned cache directory per configured service and explicit profiles for
the workloads it serves. Do not archive a developer's global home, unrelated
projects, package credentials, signing keys or a running database. Save closed
native cache snapshots through their qualified lifecycle.

## Start with LRU and representative traces

LRU is LayerCache's default eviction policy. Keep a hard byte cap regardless of
policy. Measure the workloads, object sizes, restore cost and expensive work
avoided before choosing another ranking. A frequently used tiny task and a
rare large native build can have very different value per retained byte.

LayerCache also has an opt-in producer-cost-weighted policy. It needs the same
budget, pin and integrity controls; it does not make historical producer time
equal to current net savings. Include lookup, verification and transport when
assessing a retained result. Compare policies on the same trace before changing
a shared cache.

Content-addressed native caches need graph-aware eviction. An action hit is
useful only while all referenced objects remain available. Protect active
readers with bounded leases, reject admission when protected data prevents
fitting a new artifact, and return a miss for an incomplete graph. An LRU index
alone cannot establish that lifecycle.

## Keep optional cache failures optional

Quota refusal, expiry, eviction, revoked access or an outage can make a result
unavailable. The native tool should execute its normal work. Preserve compiler
and test exit statuses; an infrastructure warning cannot turn a failed command
into a pass.

A successful cache upload confirms admission according to that protocol at
that moment. It does not establish later retention, a remote private copy, a
whole commit's passing checks or the correctness of an untrusted producer.
For Actions v1, HTTP 204 on commit and HTTP 204 on lookup describe different
operations: successful publication versus no available match. Record exact
operation, profile, ref and key version before diagnosing a reported miss.

Use retained artifacts for mandatory handoffs. Required publication and
consumer verification fail visibly. The [handoff recipe](artifacts-and-releases.md)
defines that separate contract.

## Avoid snapshots that cannot help another run

GitHub PR caches are scoped to the PR's merge ref. A closed PR's snapshots do
not populate main or other PRs. After verifying the PR is closed and no rerun
needs them, prune only those entries. Retain useful main profiles while the
runner fleet still uses them. [GitHub cache scope and eviction](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching)

Keep snapshot rotation bounded by count, age and bytes where the backing store
requires it. A timestamp-only key can create a full new archive every run.
Measure whether a smaller native cache or rebuild is cheaper than restoring a
large mostly irrelevant snapshot. Reuse the existing backend's quota and
administration tools; do not build another storage controller in each workflow.

## Separate storage location from access

Standalone OSS use is account-free on local storage or one user-managed remote
host. Managed collaboration, enrolled multiple hosts and hosted capacity use
signup and scoped tokens. Authentication and network controls still apply to
self-managed remote access.

Personal, Team and trusted-publisher Public define visibility. They do not name
different disk technologies. Keep private project/profile boundaries and full
native input context intact across local and remote restores. CI readers need
no administrator credential; untrusted PRs should not acquire publication
authority merely to improve their hit rate. Public population stays limited to
LayerCache-controlled producers.

Digest verification detects transfer corruption, while writer authorization
defines whose outputs the project trusts. Neither replaces current runtime
assertions against mutable services or an actual device.
