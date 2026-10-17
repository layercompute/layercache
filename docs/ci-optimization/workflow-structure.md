# Remove unnecessary workflow work

Changing the workflow often saves more than making its commands faster. Start
with duplicate runs, unrelated changes, obsolete validations and overly broad
matrices. Preserve a documented coverage policy throughout.

## Cancel superseded PRs and avoid duplicate triggers

A common policy runs PR checks plus main checks, without also running a branch
push workflow for the same PR commit. A workflow-level fragment is:

```yaml
on:
  pull_request:
  push:
    branches: [main]
  workflow_dispatch:

concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}
```

Include the workflow name in the group so unrelated workflows do not cancel
each other. Keep the group stable across PR revisions: putting the commit SHA
or run ID in it prevents replacement of that PR's previous run. Reusable
workflows inherit the caller's `github.workflow`; give the caller and each
callee distinct group prefixes if both set concurrency, or a callee can cancel
its own caller. [Reusable workflow concurrency](https://docs.github.com/en/actions/reference/workflows-and-actions/reusing-workflow-configurations#supported-keywords-for-jobs-that-call-a-reusable-workflow)

Keep signing/publication in their own serialized groups. This
fragment preserves running main work, but default concurrency can still replace
an older pending run. If every release must queue, qualify the platform's queue
policy separately. [GitHub concurrency contract](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)

Cancellation must reach child processes and remote work. A canceled controller
does not prove its remote builder stopped. Keep bounded polling and request
timeouts, stop owned process groups, and test cancellation against the actual
provider contract. Failed work remains failed; retry infrastructure failures
within a bound rather than retrying the entire pipeline indefinitely.

## Defer selected expensive jobs on drafts

The [requested tip from Jinjing Liang](https://x.com/JinjingLiang/status/2111211135542362334)
combines superseded-run cancellation with draft gating. Treat its reported
savings as that author's workload observation. Adopt a repository-specific
policy: cheap feedback can stay on drafts while selected native builds or
large matrices wait for review readiness.

Preserve existing triggers, filters and activity types when adding the two
draft transitions. For a workflow that previously used the default PR types,
extend its `pull_request` mapping with:

```yaml
pull_request:
  types: [opened, synchronize, reopened, ready_for_review, converted_to_draft]
```

`ready_for_review` starts qualification without requiring another commit.
`converted_to_draft` creates a replacement run that can cancel the running PR
work through the stable group above. Cancellation needs a replacement run;
workflow-level path filters can prevent that event from creating one.
[PR activity types](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request)

Put the draft predicate on the expensive job, including a job that calls a
reusable workflow, to avoid allocating its runners. A step guard leaves the
job allocation. Combine this fragment with the job's existing condition rather
than replacing its scope, dependency or authority checks:

```yaml
if: ${{ github.event_name != 'pull_request' || github.event.pull_request.draft == false }}
```

A skipped job reports success even when required. Record what was skipped;
do not turn that status into evidence that a test executed. Audit downstream
`needs`, aggregate checks and trusted-validation reuse so an intentional draft
skip cannot hide a missing ready-PR or release gate.
[Job condition semantics](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-jobs-with-conditions)

Before adopting, check two successive commits, separate PRs/workflows,
caller/callee execution, draft-to-ready without a commit, and conversion back
to draft while expensive work is running. Preserve main/manual/release
coverage and verify remote children actually stop. If path filters require a
separate cancellation controller, review its current-head races and authority
separately; the fragments here do not implement one.

## Keep required checks present

Workflow-level path filters and commit skip directives can leave required checks
pending. Keep required jobs materialized and condition expensive steps on a
conservative scope decision. [GitHub's skipped-check behavior](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/skip-workflow-runs)

A prose-only lane needs an explicit allowlist. Source, lockfiles, workflow
configuration, executable files, symlinks, policy files and unknown paths retain
full qualification. Compare the exact PR base/head and verify the checkout you
actually execute. Handle deletions and renames, use NUL-safe native Git output,
and fall back to full checks on missing history or malformed inputs.

LayerCache's [scope action](../../.github/actions/ci-scope/) and its real-Git
controls provide one implementation. It requires the expected synthetic merge
and keeps matrix/reusable job names unchanged. Main and manual events run full
qualification. Copy its policy only after reviewing the new repository's input
and generated-file rules.

Keep workflow/shell checks fresh on the prose lane. Upload evidence only for
work that actually ran. Record the selected coverage in the summary; a passing
prose lane does not claim native or runtime execution. Some job services start
before a step-level decision, so count that remaining overhead.

## Overlap independent work within a job

The [native parallel-step recipe](upstream-techniques.md#run-independent-steps-on-the-same-runner)
uses GitHub's scheduler and separate logs. Audit dependencies before grouping:

| Pair | What must be checked first |
| --- | --- |
| Node tooling and Python resource contracts | Independent temporary paths, bounded child workers and no shared generated outputs |
| Browser installation and application compilation | Compilation must not need the browser; join before browser tests |
| Two source linters | Read-only inputs, distinct caches and enough CPU/RAM for both |
| Package installation and a preparation command | The command must not consume packages or race the same store/lockfile |
| Background server and its test | Explicit readiness, bounded lifetime and cleanup on failure/cancellation |

Do not put `pnpm install` and commands that import those packages in the same
independent group. Do not overlap full worker pools merely because the YAML
allows it. A step's environment changes and outputs also have an ordering
contract; preserve the join before using them.

Measure sequential and parallel command envelopes on the same CPU/memory
limits. Alternate samples to reduce ordering effects. Keep cache warmth and
toolchain constant. A 35-second stage inside an eleven-minute job cannot yield
a 25% whole-job gain merely by overlapping that stage.

## Choose the required matrix deliberately

Use a fast representative PR lane where repository policy permits it, while
retaining the stated main/release coverage. For example, a browser matrix may
run one engine on PRs and the remaining engines on main. Changes to platform
code may still need platform-specific PR checks. Keep protected publication and
fresh runtime gates independent from a cached unit result.

Make expensive optional suites explicit inputs or documented schedules. A
manual diagnostic should not allocate a second native build when its artifact
already has a compatible producer. Remove obsolete inputs, jobs, scripts and
credentials together only after verifying that no required coverage depends
on them. Do not present a smaller suite's time as an equivalent-coverage speedup.

## Pick the next change by the remaining cost

After hits improve, time checkout, dependency installation, tooling contracts,
device boot, artifact transfer and post-job cache save. Install only what that
job uses. Keep a long-running service or VM separate from a compilation phase
when contention and duplicated setup justify it. Compare complete job and
provider cost before buying a larger runner or adding shards.

The [native build recipe](native-builds.md) covers Linux/Mac placement and
fingerprint lookup. The [measurement recipe](measurement.md) separates elapsed
feedback, allocated compute and the actual bill.
