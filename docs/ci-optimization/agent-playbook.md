# CI optimization for agents

Use this procedure when asked to make builds, tests or CI/CD faster and cheaper.
Read the repository's workflow and coverage policy before applying a tip. The
[recipe index](README.md) maps observed costs to specific changes. Sources below
were checked on 2026-10-17; platform support and pinned clients still need
verification at adoption time.

## Work in this order

1. **Map the actual work.** Read triggers, reusable workflows, scripts, required
   check names and runner placement. Trace producers, consumers and generated
   inputs. Record which PR, draft, main and release assertions must execute.
   Read the owning repository's TODOs and coordinate changes with its CI owner.
2. **Capture a baseline.** Record source/toolchain, selected coverage, resource
   limits, cache warmth and complete job time. Include setup, remote builders,
   publication, transfer, retries and canceled work. Use the
   [measurement template](measurement.md); distinguish feedback latency,
   allocated compute and actual provider charges.
3. **Remove unnecessary executions first.** Check duplicate events, superseded
   runs, selected draft jobs, unrelated changes and repeated release validation.
   Preserve required contexts and fresh fallback. The
   [workflow recipe](workflow-structure.md) covers draft transitions and stable
   concurrency groups; a successful skipped job does not establish execution.
4. **Choose one measured expensive slice.** Follow the table below. Prefer the
   native build/test client or an existing OSS seam. Keep complete declared
   inputs and current CI commands. Compare setup/save/transfer costs with the
   work avoided before adding another cache or runner.
5. **Prove the policy still works.** Check normal execution and the failure,
   cancellation, invalidation and fresh paths affected by the change. Cached
   planner/build output does not replace current runtime assertions. Required
   handoffs need retained publication and consumer verification.
6. **Record the result and next decision.** Report stage and whole-job times
   separately, including cold/publisher overhead. Link exact source and evidence
   in the owning TODO. Separate source qualification, installation, deployment
   and application adoption. Keep unsupported savings claims out of the result.

| Measured cost | Existing recipe |
| --- | --- |
| Repeated deterministic tests or local work not reaching CI | [Task graph, inputs and native reuse](task-reuse.md) |
| Compiler/native build work or unnecessary native builder allocation | [Native builds and fingerprints](native-builds.md) |
| Repeated release checks, artifact transfer or model planner calls | [Handoffs, validation and recorded inference](artifacts-and-releases.md) |
| Setup in sequence, oversized tasks or excess nested workers | [Upstream techniques](upstream-techniques.md) and [resource measurement](measurement.md) |
| Disk pressure, low-value snapshots or unbounded growth | [Storage budgets and eviction](cache-storage.md) |

## Keep the agent edit loop cheap and truthful

During edits, use the existing runner's file/test filter or affected-test mode
for fast feedback. For example, Vitest supplies `run`, `related` and `--changed`;
its related-test analysis cannot resolve arbitrary dynamic imports.
[Native test selection](https://vitest.dev/guide/cli.html#commands)
Record the focused command and run the repository's required final
qualification. Missing history, shared configuration, generated inputs or
dynamic loading can require broader checks. Focused feedback only covers the
selected tests.

Capture complete command output once; show a useful excerpt while preserving
the command's failure status. A pipeline ending in `tail` can conceal the test
exit code without `pipefail`. Actions uses different flags for an unspecified
shell and explicit `shell: bash`; qualify the wrapper with a deliberate failure
instead of assuming the shell contract.
[Actions shell behavior](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_iddefaultsrun)
Keep full evidence under the repository's storage and credential policy.

Before rerunning an expensive producer, check whether the failed consumer can
use its original exact retained input. Before recompressing a binary handoff,
measure whether packing saves bytes. Both controls are in the
[transfer recipe](artifacts-and-releases.md#measure-packing-and-transfer-separately).

## Save the next useful tip

Paraphrase the idea in its matching recipe and link the original author plus
the primary platform/tool contract. Record when it was checked, prerequisites,
what stays fresh, fallback and a concrete adoption check. Reuse the recipe links
instead of copying the same advice into several agent files. Add work to the
owning TODO only when there is an actual adoption or qualification task.

The [requested cancellation/draft post](https://x.com/JinjingLiang/status/2111211135542362334)
is saved in the workflow recipe. Its author's reported savings are not a
LayerCache measurement. A new tip is ready to use when an agent can identify
the applicable workload, implement through the existing tool, verify preserved
coverage and measure complete cost.
