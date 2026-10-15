# LayerCache's own CI reuse

LayerCache uses standard GitHub-hosted runners for this public MIT repository.
[GitHub's billing contract](https://docs.github.com/en/billing/concepts/product-billing/github-actions)
makes those runner minutes free. Reducing execution still lowers resource use
and feedback time. Cache storage has a separate allowance; this change does not
raise the repository's storage limit or enable paid runners.

## Prose-only PRs

The shared scope action compares the PR's exact base and head with native Git.
It requires both commits and the expected synthetic merge checkout. Only
regular, non-executable `README.md`, `TODOs.md` and Markdown under `docs/` qualify.
Renames are inspected as deletions and additions. Unknown files, evidence JSON,
policy files, executable bits, symlinks, unavailable history or malformed input
run full qualification. The classifier uses no GitHub API or extra token.

Matrix jobs and reusable performance checks retain their existing names. The
prose lane skips their expensive steps and records its decision. Workflow,
shell, whitespace and classifier checks remain fresh. PostgreSQL services may
still start before their job's scope decision. Main pushes, manual dispatches
and other events always execute full qualification.

The immutable beta installer no longer reruns merely for root README or launch
plan prose. Installer/workflow-specific documentation retains its existing
triggers. Release acceptance remains independently fresh.

## Native Go compilation reuse

The ordinary Linux x64, Linux arm64 and macOS Go lanes use the pinned
[`actions/setup-go` cache implementation](https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/src/cache-restore.ts).
Its existing key includes Go version, OS/architecture and dependency-file
hashes. An additional owner-only temporary profile includes runner image,
job and effective build settings. Unknown runner identity disables caching.
The profile is removed before source builds, while the action retains its
restore key for post-job saving. Go retains source/compiler/flag invalidation.

Tests still use `-count=1`; race detection and vet remain required. The
[Go cache contract](https://pkg.go.dev/cmd/go@go1.25.13#hdr-Build_and_test_caching)
separates compiler reuse from test-result replay. Native client, calibrated
performance, release-installation and local-to-CI qualification jobs retain
their independent cache setup. No fixture output is warmed by these caches.

This uses GitHub's native cache transport. It does not establish LayerCache
archive transport savings or live local-to-cloud Go reuse. The
[opt-in standalone probe](../qa/go-dogfood/README.md) separately qualifies
bounded repository-owned Go caches through the existing stock archive client
and account-free daemon. It records loopback transfer/setup, native source and
profile misses, fresh tests and forced-failure cleanup. Its first use is slower;
the current native GitHub transport remains the routine CI choice. Measure
real network/setup costs before adopting another transport. Cloud signup is
not a prerequisite for this standalone qualification.

## Baseline and acceptance

The prose-only [PR #42](https://github.com/ziahamza/layercache/pull/42) changed
only TODOs and launch-plan Markdown. Its selected PR workflows allocated
40m54s across 17 jobs; main allocated 36m52s across 14 jobs. These are summed
job spans, excluding queue time and billing multipliers. The primary
[PR CI run](https://github.com/ziahamza/layercache/actions/runs/38733579628)
and [main CI run](https://github.com/ziahamza/layercache/actions/runs/38734588412)
account for 33m11s and 33m57s respectively; companion workflows make up the rest.

The full implementation [PR #43](https://github.com/ziahamza/layercache/pull/43)
passed all 18 reported checks, including all 13 required contexts, on source
`0045810b3c69cf448633fd34a236b96d95d505f7`. Independent Standards and Spec
reviews found no issues. The merged tree at
`29f5d51eeaf3f398b815c7582387c10d894d67f8` is identical to that reviewed source.
Its three ordinary Go jobs correctly began with cache misses.

The prose-only [PR #44](https://github.com/ziahamza/layercache/pull/44) at
`e7d0030e55e098986d121425ba187d9ee55030db` preserved all 13 required contexts.
All 14 GitHub jobs passed. Its
[CI](https://github.com/ziahamza/layercache/actions/runs/38741889687),
[PostgreSQL](https://github.com/ziahamza/layercache/actions/runs/38741889363) and
[local-to-CI](https://github.com/ziahamza/layercache/actions/runs/38741889396)
workflows allocated **2m15s**, down from 40m54s in the prior prose-only PR
(94.5% less runner time). Their first-job-start to last-job-completion span
fell from 5m21s to 27s. Job allocation excludes queue time; the active span
starts with the first job and may include scheduling gaps. External review is
excluded. These measurements do not establish dollar savings for free public
runners.

The ordinary Go lanes' setup/build/test steps and native, browser, protocol and
performance work were skipped as intended. Workflow/shell checks and all 16
classifier and profile controls executed. Main continues to run full
qualification.

Measure a later main run with native Go cache hits while tests execute fresh.
Report observed job spans and cache bytes rather than inferred dollar savings.
Outstanding work remains in [TODOs](../TODOs.md).
