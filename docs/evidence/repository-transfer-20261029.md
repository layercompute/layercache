# Repository transfer and beta.10 qualification

Recorded 2026-10-29 UTC. LayerCache's canonical repository is
[`layercompute/layercache`](https://github.com/layercompute/layercache).
The explicit repository binding authorizes the current repository while the
cache project keeps its original identity. Existing visibility tiers and native
input rules still apply. Changes to the native client's declared source or
workflow inputs continue to invalidate its keys.

## Source and release

[PR #89](https://github.com/layercompute/layercache/pull/89) merged as
`9e29b2695e6816d95504f2f7bb0b91014e50d5dc` after all seventeen PR checks passed,
including the thirteen protected contexts. Normal
[main CI](https://github.com/layercompute/layercache/actions/runs/40790731412),
[PostgreSQL](https://github.com/layercompute/layercache/actions/runs/40790731318)
and [local-to-CI reuse](https://github.com/layercompute/layercache/actions/runs/40790731440)
passed on that exact source. Main CI was created October 28 at 23:56:22 UTC; its
final job completed October 29 at 00:04:49 UTC.

The [release workflow](https://github.com/layercompute/layercache/actions/runs/40791848078)
published immutable [beta.10](https://github.com/layercompute/layercache/releases/tag/v0.1.0-beta.10)
from that source. The
[fresh installer qualification](https://github.com/layercompute/layercache/actions/runs/40792963587)
passed on Linux x64, Linux arm64 and macOS arm64. It checked the locked tag,
repository/workflow/source attestations, archive checksum and installed binary,
then ran the release's native acceptance fixtures. See the
[installation guide](../../scripts/README.md) for each fixture's scope.

Beta.9 remains a separate historical regression fixture. Its preserved public
Sigstore bundles verify only its exact original source, tag and signer.
Beta.10 uses live attestations from the new repository. A failed verification
never activates another signer policy. The
[provenance policy](../../scripts/provenance/README.md) describes that boundary.

The published Linux CLI was installed after provenance verification. Source
`9e29b269` was deployed to the managed cohort and checked separately, including
authenticated project listing and repository-scope denials. Operational receipts
and account data remain private. One offline development client still needs its own update.

## Hosted remote reuse

The [new-organization producer](https://github.com/layercompute/layercache/actions/runs/40793461618)
ran from main source `9e29b2695e6816d95504f2f7bb0b91014e50d5dc`. It executed two
cold tasks and received two artifact PUT acknowledgements with HTTP 200.

After the controlled gateway restart, an
[independent non-default-branch reader](https://github.com/layercompute/layercache/actions/runs/40794158271)
ran from `9d2f7d88c7327dd9207e5417b156ac1324770872`. It restored two matching
native task keys and full output SHA-256 digests through two verified archive
GETs. It recorded zero local hits, task executions or artifact uploads. An
actual reader PUT was denied with HTTP 401. Source, environment and compatibility
changes missed; failed tasks ran twice without publication.

The reader was created October 29 at 00:27:17 UTC, after the operator's restart
and deployment checks completed. Both jobs used the locked native Turbo fixture.
The workflow summary records the source and selected runtime versions.

This is a tiny synthetic correctness and persistence proof. Application build
performance, required-artifact retention, saved CPU time and financial savings
remain separate acceptance targets. Consumer migration rollout remains open in
[TODOs.md](../../TODOs.md).
