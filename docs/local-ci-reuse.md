# Reuse local tests and builds in CI

Turbo can restore successful tests as well as build outputs. LayerCache keeps
those results in a bounded Local Cache and shares them through Team Cache.
Required PR checks still run their task commands. A matching result restores
instead of executing; a miss executes normally.

## Publish local results

Use a Local Cache configuration with an authenticated Team Cache connection.
See [setup and login](../README.md#build-and-configure). Match the execution
profile used by CI, including OS, architecture, runtime and package-manager
versions. Set an explicit `--compatibility-id` during setup when those details
affect outputs. Different profiles must use different identities.

Run the audited tasks before pushing:

```bash
layercache run --config /path/to/project.json \
  --sync-team --sync-timeout 30s -- \
  pnpm exec turbo run typecheck test build
```

`--sync-team` sets `TURBO_CACHE=local:,remote:rw`. Turbo's remote endpoint is
the loopback Local Cache daemon, so repeats use LayerCache's bounded storage
instead of a separate workspace cache. Remove explicit Turbo `--cache` flags
that would override this setting. Run from the Turbo workspace root so the CLI
can collect the invocation's run summaries.
Nested commands that bypass LayerCache restore the caller's original Turbo
cache policy, so ordinary workspace reuse remains available.

After the child finishes, LayerCache checks only its successful cacheable task
hashes in Team Cache. It republishes matching Local artifacts when needed and
prints:

```text
Team Cache ready: 3/3 successful tasks
```

This confirms availability at that moment. Storage eviction can remove results
later, in which case CI recomputes them. Confirmation does not wait for unrelated
queued uploads. The wait defaults to 30 seconds and accepts 1 millisecond through
60 seconds. An outage, missing result or timeout prints an unconfirmed-publication
warning and preserves the task command's exit status. Pending Local artifacts use
the existing durable upload queue, including after a daemon restart.

A failed test remains a failed test and executes again on the next run.
Independently successful tasks can still be shared when another task fails.
The ready message does not certify that every check on a commit passed.

## Reuse results in required PR checks

Configure the same Team project and execution profile in CI, after installing
the matching tools and dependencies:

```yaml
permissions:
  contents: read
  id-token: write
steps:
  # Check out the project and install its pinned tools and dependencies first.
  - uses: ziahamza/layercache/action/turbo@ca3d5889a52815bce9565d3825b081839ac7077b
    with:
      team-url: https://cache.example.com
      compatibility: linux-x64-node24.13.0-pnpm10.34.5-tests-v1
      cache-mode: read-only
  - run: pnpm exec turbo run typecheck test build
```

Pin a reviewed LayerCache commit for reproducible action code. GitHub OIDC
authenticates the project; PR credentials are read-only. The action's cache mode
does not grant write permissions. Keep the required checks present so misses
still execute and report their result through GitHub.

Use only tasks with complete deterministic inputs. Declare dependency files,
shared configuration, relevant environment variables and outputs in `turbo.json`.
Source, dependency or environment changes then produce a different Turbo hash.
Do not reuse a live-service test, a mutable Git-base comparison or a task with
undeclared inputs as a test verdict. Fix its inputs or give it a fresh policy.

## Run fresh checks on main

For a fresh test lane, select `cache-mode: fresh` and run the normal task command.
This sets `TURBO_CACHE=local:,remote:` and disables both task-result reads and
writes. You can also set that policy directly:

```bash
pnpm exec turbo run test --cache=local:,remote:
```

Turbo 2.10.12 rejects combining `--force` and `--cache`; the empty cache modes
already force execution. Test runners and compiler tools can have separate
caches, which this flag does not configure. A build lane may use `read-write`
while the fresh test lane executes independently.

## Limits and evidence

Team project writers are trusted to publish results. Digest checks verify
artifact transfer integrity; they do not attest that an arbitrary writer ran a
test. No commit-level test receipt or provider check bypass is introduced here.
Native compiler reuse, signed releases and live validation retain their own
execution policies.

The [real Turbo acceptance](../qa/local-ci-reuse/README.md) proves Local and
fresh Team restores without task execution, failed-test reruns, changed-input
misses, fresh-main execution and offline publication recovery. It compares
restored output digests and records the source of each cache hit. The fixture
is a correctness check, not an application workflow performance benchmark.
