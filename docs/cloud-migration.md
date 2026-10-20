# Moving existing projects to hosted storage

Moving cache bytes to R2 and adding a project to the hosted dashboard are
separate operations. A fresh dashboard project has a new identity and signing
key. It cannot read an existing cache merely because the repository name is
the same. Preserve those identities when adopting an existing project.

## Prepare the move

Inventory the actual configured projects and their clients. Include native
task keys, compiler archives, Actions caches, container registries, pending
uploads, retained receipts, read leases and measurements. Distinguish fixtures
from application projects. Deduplicate symlink and hardlink aliases before
reporting physical storage; a sparse filesystem's capacity is not its current
allocation.

Keep source, credentials and recovery records outside the retirement scope.
Record current project IDs, signing identities, repository/default-ref policy,
memberships and quota. Obtain numeric repository and user IDs from GitHub for
portal adoption. Keep all operator configuration and migration manifests out
of a public repository.

Provision private S3-compatible destinations with scoped credentials and
isolated metadata namespaces. Preserve object keys and complete SHA256/size
identities. Copy PostgreSQL metadata together with the objects it references;
copying archives alone loses native lookup keys and publication state. Verify
complete destination bytes and metadata before changing clients. Reconcile an
unknown upload by reading its destination rather than publishing it blindly
again.

## Cut over and qualify

1. Copy existing immutable objects while the old service remains available.
2. Freeze old publications and maintenance, drain readers, and take a final
   consistent metadata snapshot. Recheck pins, retained obligations, pending
   uploads and active leases. An earlier empty inventory is insufficient.
3. Copy the final delta and verify full object digests, sizes, metadata,
   memberships, native keys and publication identities against that snapshot.
4. Start only the intended replacement runtimes. Keep an existing HTTPS route
   compatible when possible so installed CI clients retain their project
   identity and signed repository/ref authority.
5. Verify authenticated reads of pre-migration bytes, a bounded new publication,
   changed-key misses, read-only denial and project isolation. Restart the
   replacement and repeat reads before retiring the old copy.
6. Verify the hosted dashboard lists each adopted project and reports its real
   remote backend. Use configured real GitHub OAuth or explicitly provisioned
   operator password accounts for imported owners. Password sign-in does not
   supply a GitHub provider token or grant native cache authority. A local
   authentication fixture does not qualify a deployed service.

Keep container-registry migration separate from artifact-store migration. Use
the registry's native object-storage driver and preserve its authentication,
immutable manifest/blob graph and required handoffs. A filesystem retention
scanner cannot manage a registry after its objects move to S3. Qualify the
replacement lifecycle before claiming bounded remote storage or cost.

## Retire verified local copies

Cloud-only clients must work before stopping their Local daemons. Switching a
dashboard view alone does not change where build commands publish or restore.
Preserve standalone Local operation as an independent option.

After cutover, check current clients, services, processes, mounts and reverse
dependency links again. Remove only the exact migrated, unreferenced stores.
Preserve useful measurement records and a supported recovery copy. A retired
sparse pool must be unmounted and its exact startup wiring disabled before
removing its backing file. Do not run a broad Docker volume prune or remove a
shared parent directory.

Record logical bytes migrated, destination verification, allocated bytes
removed and the observed filesystem-availability change separately. Concurrent
work can make those numbers differ. Required pipeline handoffs keep their
existing durable fallback until their own retained-service acceptance passes.

R2 hosts artifact bytes. API processes, PostgreSQL and bounded verification
staging still need compute and storage somewhere. Moving them off a developer
machine requires a separate hosting and recovery migration.

## Disconnect a cloud client

`layercache uninstall --config FILE --preserve-cache` removes a cloud-only
client configuration and its LayerCache-owned credential. It leaves hosted
artifacts, local path hints and the GitHub CLI login untouched. A client using
GitHub CLI has no LayerCache-owned keychain credential, so disconnecting it
requires no recovery-copy write, even when storage is full.

A client with a LayerCache-owned keychain credential first saves and syncs a
0600 recovery copy beside its configuration. Keep enough space for that small
file, usually a few KiB, and one inode; the bounded configuration can be up to
1 MiB. If that copy cannot be persisted, disconnect stops before changing the
configuration or credential. There is no force option that skips recovery.

If restoration fails, the error includes the protected recovery file's exact
path. After a completed disconnect, failure to remove that remaining copy is a
warning, and JSON reports success with `pendingRecoveryCleanupPath`. Remove
that exact file after directory access is restored; repeating uninstall is
unnecessary. Both uninstall modes preserve the cloud-only data-directory hint.

See [hosted storage pools](hosted-storage.md), the
[customer dashboard](dashboard.md) and [retained artifacts](retained-artifacts.md).
