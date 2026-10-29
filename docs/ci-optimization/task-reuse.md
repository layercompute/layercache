# Reuse tasks across local work and CI

The largest saving is often avoiding an equivalent successful task. Keep the
required CI job, run its normal task command, and let the build system restore a
matching result. An unknown key runs the task. A previously failing task runs
again.

Start with deterministic typechecks, lint, unit tests and build outputs. Keep
network tests, device assertions and checks against changing external state
fresh until their complete inputs have a separate qualification.

## Keep clocks and timing assertions deterministic

A test that combines an old fixed fixture date with the real clock can pass
from cache and fail when it next executes after a retention or UTC-day boundary.
Give every component the same fixture clock. With Vitest, fake only `Date` when
the asynchronous scheduler should keep running, set the fixture time, and restore
real timers after each test. See Vitest's
[fake-timer controls](https://vitest.dev/api/vi#vi-usefaketimers) and
[system-time control](https://vitest.dev/api/vi#vi-setsystemtime).

Measure a cancellation response from the actual cancellation request. Keep
process startup and the whole fixture's watchdog separate. A delayed startup
control should preserve the response assertion; a deliberately slow cleanup
must still fail it. Do not relax the response bound to cover unrelated setup.

## Split the task that loses all its work on every edit

A command such as `check` often hides many independent tests behind one cache
key. A small edit then invalidates the whole command. Expose useful groups to
the existing scheduler instead of writing a second scheduler in the workflow.

For example, keep `check` as the contributor-facing command, but make it invoke
Turbo tasks for typechecking, unit tests and several workflow-test groups. Each
group should keep the original test runner flags, process isolation and failure
behavior. A group may still run several isolated test processes in sequence.
The required aggregate must depend on every group and prerequisite typecheck.

Choose groups that change independently and take long enough to justify their
restore cost. Avoid one CI job per tiny test; each job adds checkout,
installation, cache transfer and scheduling overhead. A useful first change is
several native tasks inside the existing job.

One anonymized qualification exposed ten test groups while preserving all
thirty isolated test processes. A producer executed the ten groups in a
4m10.355s native stage. A separate empty consumer restored twelve successful
results, including two prerequisite typechecks, in 965ms with zero task
executions and 396,756 downloaded bytes. Both consumers used compatible Linux
profiles and a private shared endpoint. This is a task-stage observation from
separate workspaces on one host, not a whole-job or monthly savings estimate.

## Make the key cover the work

Let Turbo, Nx, Gradle or Bazel own the task key and dependency graph. A transport
cannot repair missing task inputs. Audit these inputs before sharing results:

| Input | What commonly gets missed |
| --- | --- |
| Source closure | Shared package source, imported test helpers, generated source consumed by the task |
| Dependencies | Lockfiles, package manifests, native dependency versions |
| Tooling | Test-runner configuration, compiler configuration, shared lint rules, scripts that wrap the command |
| Environment | Public application configuration, feature variants, runtime-affecting variables |
| Execution profile | Actual OS, architecture, runtime, package manager and native compiler assumptions |
| Outputs | Every file a successful consumer needs, including generated declarations or test evidence |

Turbo documents its own [hash inputs, outputs and run summaries](https://turborepo.dev/docs/crafting-your-repository/caching).
Use `--dry=json` to inspect the plan and `--summarize` to compare actual runs.
Keep the pinned native client as the authority for what those fields mean.

Runtime credentials do not automatically belong in a key. A credential that
only authorizes cache transfer is different from an environment value that
changes a build. If a task consults mutable provider state, adding a secret to
the key does not make that state deterministic. Execute that check fresh or
declare a stable snapshot of the state.

Cross-machine profiles must describe the tools actually installed. Sharing a
profile label between different Node, JDK or Xcode versions does not establish
compatibility. Start conservatively and broaden only after paired controls.
For portable JavaScript tasks, do not assume that a successful macOS run covers
Linux behavior.

## Stop generated evidence from invalidating its own producer

A broad root input glob can ingest logs and reports restored by another task.
The first restore changes the analysis task's key; a later run changes it again.
The source is unchanged, but the cache appears unreliable.

Keep generated files in a dedicated directory. Declare that directory as an
output for its producer. Exclude it from tasks that do not consume it. This
small Turbo configuration illustrates the exclusion, rather than a complete
test setup:

```json
{
  "tasks": {
    "lint": {
      "inputs": ["$TURBO_DEFAULT$", "!artifacts/generated/**"],
      "outputs": []
    }
  }
}
```

Preserve the native default input set when adding an exclusion. If another task
uses generated source, retain that input and its producer dependency. Excluding
all generated files, all configuration or all tests trades misses for incorrect
hits. Turbo's [configuration contract](https://turborepo.dev/docs/reference/configuration#inputs)
describes custom inputs and `$TURBO_DEFAULT$`.

In one observed workspace, a first replay restored 25 of 28 tasks but three
root analyzers missed after generated evidence appeared. A stable second replay
restored all 28 in an 807ms native stage. The fix excluded only the declared
evidence directory. Real native controls then kept analysis hashes stable when
evidence changed and changed them for real source and analyzer-configuration
edits. The 807ms observation is a warm task stage, not total command time.

Git-dependent analyzers need special treatment. Their result can depend on a
merge base or repository history outside normal source inputs. Bind that context
explicitly and test it, or run the analyzer fresh. A source-only key is
insufficient merely because the script is deterministic for one checkout.

## Bound concurrency at every level

Count the whole process tree. Turbo concurrency, test-worker pools, Gradle
workers, compiler processes and emulator processes can each multiply demand.
A task limit of two does not limit a test runner to two processes.

On a two-core runner, start with two scheduler tasks and bounded child workers.
Measure peak memory, swap, CPU use and whole-job duration before increasing the
limit. Keep memory headroom for the operating system and any database or device
runtime. Use the runner's effective CPU and memory limits rather than assuming
that every host has the developer workstation's capacity.

For an existing Turbo task graph, a starting command is:

```sh
pnpm exec turbo run typecheck test:unit test:workflow --concurrency=2 --summarize
```

The task names must exist in that repository. Turbo's
[run reference](https://turborepo.dev/docs/reference/run#--concurrency)
defines the scheduler limit; configure the child test runner separately. Preserve
explicit OOM failures. Repeatedly retrying an oversized process wastes compute
and can hide the resource problem.

## Publish locally, then consume with the same profile

Standalone LayerCache is OSS and requires no account for Local Cache or one
user-managed remote host. Managed Team collaboration, multiple enrolled hosts
and LayerCache-hosted storage require signup and a scoped token. That product
boundary is separate from Personal, Team and trusted-publisher Public visibility.
Private reuse keeps the native client's full declared context.

For an already configured, authenticated Team project, the qualified Turbo
producer path is:

```sh
layercache run --config /absolute/path/project.json \
  --sync-team --sync-timeout 30s -- \
  pnpm exec turbo run typecheck test:unit build --concurrency=2 --summarize
```

Run from the workspace root. The CLI uses its bounded Local daemon, then checks
the invocation's successful cacheable task hashes at the configured Team
destination. `Team Cache ready: N/N successful tasks` confirms availability at
that moment. Eviction can later cause CI to recompute. An outage or incomplete
publication reports a warning and preserves the child command's exit status.
The message is not an attestation that all checks on a commit passed.

CI still invokes the same tasks. Use read-only remote authority for PRs and
trusted writers for publication. Credentials stay in external configuration or
the CI secret facility. Digest verification checks transferred bytes; an
authorized writer remains trusted to produce correct task outputs. Public
publication is reserved for LayerCache-controlled builds.

The documented managed flow is a product contract. An existing private endpoint
qualification does not establish that hosted self-service has launched. CLI
installation, endpoint deployment and application adoption require their own
acceptance.

## Keep a fresh lane and prove failures still fail

To run Turbo tasks without reading or writing task-result caches:

```sh
pnpm exec turbo run test:unit test:workflow --cache=local:,remote:
```

This does not configure caches inside the test runner or compiler. Inspect
those separately. With the qualified Turbo 2.10.12 client, `--force` disables
reads but can still write, and combining it with `--cache` is rejected. The
empty cache policy already requests execution.

Use fresh main or periodic lanes according to the repository's policy, while
keeping PR task commands present. Release signing, publication, live-provider
checks and device runtime acceptance retain their own current checks.

Before adopting any shared task result, require these controls:

1. A trusted producer succeeds and records native hashes plus output digests.
2. An empty compatible consumer restores those exact outputs with zero task
   executions. Attribute the hit to the intended endpoint, rather than an
   already warm workspace cache.
3. Relevant source, helper, lockfile, environment and profile changes miss.
   Unrelated generated evidence remains stable after the narrow input fix.
4. An intentional failure executes on two consecutive runs and returns failure
   both times. Keep its execution counter outside restored outputs.
5. Explicit fresh mode executes despite populated caches. Read-only access,
   revocation, outage and bounded storage keep their documented behavior.

Report native task time, complete job time and transferred bytes separately.
One hosted example had a 3m20s native stage inside an 8m28s job. Setup and other
fresh checks still occupied the runner. After task hits improve, measure that
remaining work before adding another cache.
