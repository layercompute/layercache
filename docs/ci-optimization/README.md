# Faster, cheaper CI

Start by removing work that does not need to run. Then reuse successful work
through the build system's own cache. Finally overlap the remaining independent
commands and choose a runner that fits their resource needs.

These recipes collect production workflow lessons and isolated native-client
qualifications. Application examples are anonymized. Measurements distinguish
task time, job allocation and bills; a cache hit alone proves none of those
savings. Each page states the limits of its examples.

Agents: start with the [optimization playbook](agent-playbook.md) for the edit
loop, recipe selection and completion evidence. Save new source-backed tips in
the matching recipe so future agents can find and qualify them.

## Choose the work to avoid

| What you observe | First useful change | Recipe |
| --- | --- | --- |
| New PR commits leave old checks running | Cancel superseded PR runs, avoid duplicate triggers | [Workflow structure](workflow-structure.md) |
| Draft PRs allocate expensive runners before review | Gate selected jobs and handle both draft transitions | [Draft policy](workflow-structure.md#defer-selected-expensive-jobs-on-drafts) |
| Prose edits compile every platform | Conservative change classification with stable required checks | [Workflow structure](workflow-structure.md#keep-required-checks-present) |
| Small source edits rerun a large test command | Split useful groups inside the existing task graph | [Task reuse](task-reuse.md) |
| Restoring generated reports causes more misses | Correct the declared input/output boundary | [Task reuse](task-reuse.md#stop-generated-evidence-from-invalidating-its-own-producer) |
| A developer just ran the same deterministic tests | Publish native task results, keep normal CI commands and fresh fallbacks | [Local to CI](task-reuse.md#publish-locally-then-consume-with-the-same-profile) |
| Independent setup/check commands run in sequence | Measure native parallel steps on the same runner | [Upstream techniques](upstream-techniques.md#run-independent-steps-on-the-same-runner) |
| CPU, memory or swap saturates | Bound nested workers and compare runner sizes by whole-job cost | [Measurement](measurement.md) and [task concurrency](task-reuse.md#bound-concurrency-at-every-level) |
| Native compiler hits are high but jobs remain long | Time dependency setup, linking, packaging and runtime checks | [Native builds](native-builds.md) |
| JavaScript-only edits schedule another native build | Resolve a compatible framework fingerprint before allocating a builder | [Native builds](native-builds.md#reuse-a-compatible-development-build-before-scheduling-a-mac) |
| Release preparation repeats successful general validation | Verify trusted results for that exact source; retain fresh fallback | [Release preparation](artifacts-and-releases.md#reuse-trusted-general-validation) |
| A later job cannot find an acknowledged upload | Use retained artifacts for required handoffs; verify the consumer | [Artifacts and releases](artifacts-and-releases.md) |
| Upload CPU dominates a compressed binary handoff | Compare packing levels and preserve consumer verification | [Transfer cost](artifacts-and-releases.md#measure-packing-and-transfer-separately) |
| Snapshots crowd out useful work or exhaust disk | Budget physical storage, use bounded eviction and prune closed-PR snapshots | [Cache storage](cache-storage.md) |
| Model-driven tests repeat identical planner calls | Replay recorded inference while executing current actions/assertions | [Inference and E2E](artifacts-and-releases.md#recorded-inference-is-an-input) |
| A warm task is fast but the bill stays high | Include setup, transfer, remote workers, retries and all runner SKUs | [Measurement](measurement.md) |

## A practical order

1. Record a representative baseline and the actual required coverage. Include
   PRs, main, manual releases, failures and canceled runs.
2. Remove duplicate triggers, obsolete work and unrelated validation. Keep
   required check names and fail back to full work when classification is unknown.
3. Split deterministic tasks, fix their complete inputs and expose native cache
   outcomes. Prove an empty consumer and repeated failure execution.
4. Reuse compiler outputs and compatible native products. Verify current
   application behavior separately.
5. Overlap independent work, then tune worker counts and runner size. Retain
   separate logs and join required work before its consumer starts.
6. Re-measure the whole job. Stop adding caches when setup and transfer cost
   more than the work avoided.

This order is a starting point. A large repeated Mac build or duplicate release
validation can outweigh every small lint improvement. Use the measured expensive
stage to choose the next change.

## Where LayerCache fits

LayerCache supplies bounded storage and native protocol adapters. Keep task
graphs, compiler keys, packing and execution in existing tools. This guide also
covers improvements that need no LayerCache integration.

The OSS CLI and daemon require no LayerCache account for local storage or one
user-managed remote host. Managed Team collaboration, multiple enrolled hosts
and hosted storage require signup and a scoped token. That service contract
does not announce a live hosted deployment. Personal, Team and trusted-publisher
Public visibility are separate from storage hosting. Private reuse retains the
native client's complete declared context. Only LayerCache-controlled builds
populate Public Cache.

Try [standalone setup](../../README.md#build-and-configure), then use a protocol
the workload already speaks. The [task recipe](task-reuse.md) describes qualified
Turbo reuse; the [native recipe](native-builds.md) separates released support,
source qualification and application adoption. The [upstream note](upstream-techniques.md)
records native platform features and experiments that still need qualification.

## Before calling an optimization done

Require a real success, an independent empty consumer, relevant input/profile
misses, two fresh executions of an intentional failure, and an explicit fresh
path. Check cancellation, outage, capacity and required artifact handoffs.
Record the exact producing source and toolchain, current coverage, transfer
bytes, setup/save cost and complete job duration. Keep those details out of
credentials and public examples when they identify a private application.

Read the [measurement examples and experiment template](measurement.md) before
publishing a percentage or a monthly cost claim.
