# Measure time, compute and cost separately

Choose the result before changing the workflow. Faster required feedback,
fewer allocated runner minutes and a smaller invoice are related outcomes, but
each needs its own measurement. A short cached task can leave a long job, and
free public runners can consume fewer resources without changing the bill.

## Keep three clocks

| Measure | How to collect it | What it answers |
| --- | --- | --- |
| Contributor feedback | Push or dispatch until all selected required results are available, including queue time | How long does the contributor wait? |
| Active workflow span and critical path | First selected job start to last completion; inspect the dependency chain that controls completion | Which setup, execution, transfer or scheduling dependency delays the result? |
| Summed job spans | Sum each allocated job's completion minus start, including parallel jobs | How much runner allocation did these workflows consume? |

The active span includes gaps after its first job starts. It excludes earlier
queue time. The critical path is the dependency chain controlling completion;
summing every parallel job does not calculate it. Job spans are allocation
observations, not measured CPU time or a billing receipt.

Select workflows consistently. Include companion workflows, post-job cache
saving, artifact transfer and waiting controller jobs when they belong to the
scenario. Record optional coverage separately. Count failed attempts, reruns,
superseded runs before cancellation and remote build workers. A Linux job
waiting for a remote Mac may incur controller and worker usage simultaneously.

## Compare equivalent work

Record the actual source, task graph, coverage, tool versions, runner image,
architecture and effective CPU/memory limits. Keep fresh runtime assertions and
other required checks equivalent between samples. If coverage or runner class
changes, describe it as a different experiment.

Use a trusted producer and an independent empty consumer. Remove the native
workspace cache or prove that it could not serve the hit. Name the intended
cache source and attribute requests to it. Two workspaces on one host can prove
archive compatibility without establishing cross-host network performance.

Collect cold, first-use and warm observations separately. First use includes
bootstrap, publication and daemon startup where applicable. Alternate paired
samples or repeat across representative workloads so a quiet host or faster
provider allocation does not become the claimed saving. Preserve failed,
slower and negative samples. Report sample count and spread alongside medians.

Change source, dependencies, declared environment and profile separately to
prove misses. Repeat an intentional failure and exercise explicit fresh mode.
Keep exact restored output digests and native execution counters. A transfer
hit proves that bytes were retrieved; it does not prove all equivalent work was
avoided or that a current runtime test passed.

## Include the cost of reuse

Time lookup, download, verification, extraction, native task restoration,
execution on misses, upload and post-job saving. Capture transferred bytes,
retained physical bytes and the retention period. Large snapshots can cost more
to move than the work they avoid. Small fixtures may mostly measure startup.

Set command, step and job timeouts from observed behavior plus a stated margin.
Use the provider's [timeout controls](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idtimeout-minutes)
and ensure cancellation stops owned child processes and remote work where
supported. A timed-out controller is not evidence that its remote worker stopped.
Treat bounded retry policy and abandoned-work cleanup as part of the experiment.

For a paid estimate, use the actual billing SKU, rates, rounding, minimums and
allowances for the measurement period. Verify any applicable allowance
conversion or billing multiplier rather than applying a historical OS factor
to every job. Add remote-worker charges, storage, transfer and fixed fees; apply
included quota and credits once. Compare with the billing export before
reporting a monthly saving.

GitHub's [billing contract](https://docs.github.com/en/billing/concepts/product-billing/github-actions)
makes standard hosted runner use free for public repositories. Larger runners
and storage have separate rules. LayerCache's public-repository observations
below establish reduced allocation and feedback time, not runner-dollar savings.

## Observed examples and their limits

These are historical observations from qualified paths, not forecasts. The
application cases omit operational identifiers intentionally. Their evidence
was checked during qualification, but this public summary does not expose the
private repositories or provide an independently reproducible application
benchmark. Keep each row's measurement boundary when quoting it.

The [mobile CI case study](case-study/ci-cd-optimization-guide.md) explains six
further observations and failed approaches. Its
[machine-readable measurements](case-study/case-study-measurements.json) keep
allocation, wall time, observed ZIP size differences, publisher overhead and
accepted-result reuse separate. Their percentages cannot be added or treated as
invoice savings.

| Change | Observed before and after | Boundary and remaining work |
| --- | --- | --- |
| Independent mobile test groups | Producer native stage 4m10.355s; empty consumer 965ms, twelve restored results and zero task executions | Separate compatible workspaces on one host; read-only remote consumer; 396,756 downloaded bytes. Setup and whole-job time excluded. |
| Exact trusted validation reused in manual release preparation | Preparation 28m18s to 3m16s | Repeated validation removed after exact-source trusted-check lookup with fresh fallback. Subsequent native build, signing, publication and runtime work are outside these preparation times. |
| Compatible accepted standard QA result reused | Linux QA controller job 19m46s to 2m12s | Reused the matching app and successful QA receipt within its age bound; no new Xcode build or fresh runtime execution. Does not qualify full Contacts XCTest, dense fixtures or carrier audio. |
| Native parallel tooling steps | Earlier sequential stage 85s; hosted parallel group 56s, with Node at 56s and Python at 42s | One earlier main observation and one PR observation on the same two-vCPU Ubuntu runner class. Both command families passed and joined before workspace tasks. No controlled whole-job or cost improvement established. |
| LayerCache prose-only PR classification | Summed selected job spans 40m54s to 2m15s, seventeen jobs to fourteen | All thirteen required contexts retained; full main qualification remains. Active span 5m21s to 27s excludes initial queue time. Standard public runners have no runner charge. |
| Standalone Go compiler archive over loopback | Cold build plus fresh tests 4.623s; restore plus relocated build and fresh tests 0.755s | 13,076,823 archive bytes; compiler reuse, with test execution fresh. Single-host fixture, not hosted CI or Team savings. |

The sequential tooling sample spent 50s on Node and 35s on Python. The hosted
parallel sample started both steps together and joined after 56s, a 29s shorter
observed stage. The individual commands took longer while overlapping. Both
used the two-vCPU Ubuntu 24.04 runner class. The baseline printed Node 24.21.0
and pnpm 10.7.1, but not Python's exact runtime version. Source changes,
setup and cache warmth were not controlled between these two runs. This is
successful hosted overlap, not a paired performance experiment or a forecast.

LayerCache's [self-CI qualification](../self-ci.md) records the public prose-only
workflow evidence. The [Go receipt](../evidence/go-dogfood-20261015.json) and
[opt-in runner](../../qa/go-dogfood/run.mjs) retain the standalone experiment's
scope and controls. That Go producer also paid 0.694s to save; setup and daemon
startup added 0.221s, and the one-time candidate build took 1.339s. First use was
slower. Do not subtract only the two displayed command times to describe the
complete experiment or a new user's experience.

## Copy an experiment receipt

Use this template before collecting numbers. Keep operational source identifiers
and detailed evidence in the appropriate access-controlled record. For a public
summary, remove credentials, private hosts, personal data and application
identifiers while retaining the measurement boundary and limitations.

```text
Question and proposed change:
Date and measurement period:
Source and workflow definition:
Scenario and required/optional coverage:
Producer trust and consumer read/write authority:
Native client and pinned tool versions:
Runner/provider/image/architecture/CPU/memory:
Compatibility profile and relevant declared inputs:
Cold/first-use/warm definition and cache source:
Commands and cache read/write/fresh policy:
Native executed/restored/skipped/failed task counts:
Exact restored output digest comparison:
Lookup/download/verify/extract/upload durations and bytes:
Bootstrap/install/setup/cleanup/post-job durations:
Feedback duration including queue:
Active workflow span and controlling dependency chain:
Summed selected job spans and remote-worker time:
Retained physical bytes and retention period:
Changed-input/profile, repeated-failure and fresh controls:
Timeout/cancellation/retry/outage outcome:
Sample count, all observations, median and spread:
Billing SKUs/rates/allowances/fees and billing evidence:
Excluded work, missing attribution and uncertainty:
Adoption decision and next acceptance target:
```

Leave unavailable measurements explicitly unknown. A native hit without source
attribution stays unattributed. A confirmed upload can later be evicted; it does
not promise future retention. Source qualification, installed tooling,
application adoption and a deployed service remain distinct results.
