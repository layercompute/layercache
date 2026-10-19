# External native Turbo and OIDC acceptance

This manual Linux qualification uses real GitHub OIDC, the existing pinned
`action/turbo` and the locked Turbo 2.10.12 fixture from source
`170582ba6caccb5f5dcdf60171d8d65196075993`. It makes no service or workflow
traffic during ordinary pushes, pull requests or fork checks.

An operator must configure these repository settings before dispatch:

- Variable `LAYERCACHE_R2_QA_ENABLED`: exactly `true` enables the manual job.
- Variable `LAYERCACHE_R2_QA_ORIGIN`: the approved public HTTPS cache origin.
- Secret `LAYERCACHE_R2_QA_PROJECT`: the exact configured project selector. This
  selector grants no access. Secret storage hides it from the runner's initial
  input/environment display; real short-lived OIDC capabilities grant access.

No R2 keys, database credentials, administrator tokens or persistent cache
capabilities belong in GitHub. The project must authorize this repository's
workload identity. An operator must review the selected branch's workflow and
helper before allowing it to contact the service.

Dispatch `Hosted native Turbo acceptance` with `mode=producer` on the actual
default branch and a new random 32-character lowercase hexadecimal `sample`.
After it passes, dispatch `mode=reader` with that same sample on a reviewed
non-default branch containing this workflow and the identical pinned fixture.
These are separate fresh hosted jobs. Selecting reader while dispatching main
does not reduce server authority and is rejected before authentication. The
server grants only read authority to the real non-default-ref reader.

Both roles verify the checked-out source equals GitHub's signed source and
inspect the returned capability in memory. Native publication/restoration and
the reader's denied PUT establish actual server acceptance; decoding claims
alone does not verify their signature. Turbo's current denied-write status is
401. Successful authenticated remote restores distinguish that denial from an
unusable credential.
Zero task-scenario artifact PUTs excludes the deliberately denied authority
probe. The action's post hook can send filtered reporting POSTs.

The native compatibility names the actual Linux x64 runtime, Node, Turbo, pnpm
and fixture schema. It excludes the source commit, branch, run ID and paths.
The sample is a declared task environment input, allowing a new cold experiment
without changing compatibility. Reusing an old sample for another producer can
fail the cold-execution assertion. The helper verifies the pinned fixture bytes
before modifying its owned copy; `EXECUTION_COUNTER` remains a pass-through
environment variable outside native outputs and native task identity.

Every Turbo invocation disables filesystem task caching explicitly. The
producer must execute both tasks and receive two native publication
acknowledgements, each from an HTTP 200 artifact PUT. Successful status/events
requests cannot establish publication. The independent reader must restore
both native task keys, verify both complete output hashes and execute zero tasks.
Source/environment
changes must alter native keys and execute. A separately scoped OIDC credential
with changed compatibility must retain those keys but miss its namespace.
The intentionally failed test executes twice; a producer allows remote writes
for this control and must observe zero artifact PUTs from failed tasks.

A streaming loopback relay forwards only the native artifact protocol to the
configured HTTPS origin. It never follows redirects, measures complete body
bytes and request timings, and verifies SHA256 on successful archive reads.
After each native command, it allows up to five seconds for real pending
responses to finish. It drains upstream bodies after the native client closes
its side, including optional telemetry bodies. This avoids treating Turbo's
early exit after telemetry headers as a transfer failure. A timeout, upstream
error, truncated/oversized body or redirect still fails qualification.
This proves the external HTTPS/native transport through that relay, not a
direct-client performance comparison. Summed overlapping request durations
are separate from native-command wall time and are not pure bandwidth.
`commandMs` stops at native process exit; `observationMs` records the separate
response-drain wait.

Only `r2-native-public-summary.json` is uploaded. It allowlists tool/source pins,
task keys, statuses, execution counts, output digests and aggregate measurements.
It omits project selectors, capability/OIDC bodies, service identities, URLs,
local paths, raw Turbo summaries and command logs. Original summaries remain
on the disposable runner; only the baseline is submitted by the pinned action's
post hook. A qualification failure removes all owned raw summaries before that
hook runs, preserving the partial public summary with `passed=false`.
This is optional cache acceptance. Required application handoffs
continue using GitHub artifacts; there is no application or cost-saving claim.

Credential-free controls run locally and before any hosted authentication:

```sh
node --test qa/r2-native-oidc/run.test.mjs
actionlint .github/workflows/r2-native-oidc.yml
```

They cover wrong ref/opt-in, wrong issued authority, origin retargeting,
summary redaction and 307/308 rejection with zero requests received by a foreign
target. After the
locked fixture client is installed, they also run real Turbo producer/reader
and failure controls against a local in-memory protocol model with synthetic
credentials. They do not qualify live OIDC, R2 persistence or native hosted
cache reuse.
