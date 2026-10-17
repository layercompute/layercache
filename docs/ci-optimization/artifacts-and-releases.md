# Reliable handoffs and less repeated release validation

An optional acceleration cache and a required pipeline handoff have different
failure contracts. Decide which one you are building before selecting storage.

| Data | Suitable contract | Miss behavior |
| --- | --- | --- |
| Dependencies, task outputs, compiler results | Bounded evictable cache with native identity | Recompute the work |
| APK, IPA, web export or binary required by the next job | Retained immutable artifact plus producer manifest | Fail visibly, or run an explicitly supported producer fallback |
| General-validation proof for release preparation | Trusted current workflow/job state for the exact source/context | Run general validation fresh |
| Release package retained for later distribution | Explicit retention policy and exact package identity | Rebuild through the normal release path |

GitHub distinguishes workflow artifacts from caches and offers retained data
transfer between jobs. [Artifact contract](https://docs.github.com/en/actions/concepts/workflows-and-actions/workflow-artifacts)
An upload acknowledgement, cache hit or signed download URL does not promise
that bytes will remain available until a later job needs them.

## Publish once and verify the actual consumer

The producer builds the output once, calculates its full size and SHA-256, and
publishes it with a manifest. Retain the artifact under an immutable producer
identity. Fail the required publication step if it cannot establish that
contract. Do not silently convert a missing handoff into a successful build.

A useful manifest binds the immutable repository identity, source SHA,
workflow/run ID, run attempt, producer job, artifact identity, size, digest,
execution profile and package-specific identity. Display names help humans;
they do not replace those identities. A filename called `latest` is insufficient.

Download from the actual consuming job, verify the complete byte count and
digest, then inspect the package. Keep web entry files, native package IDs,
version/build values, signatures, entitlements or required runtime contents in
that stage's existing validation. Digest handling depends on the pinned client;
download-artifact v8 defaults to an error on a digest mismatch. Retain explicit
payload, completeness and producer checks rather than treating a verified ZIP
as verification of the contained package's identity.
[Pinned v8 digest policy](https://github.com/actions/download-artifact/blob/9000827ccba6bdab643e8b6fd33ac0654aef8333/action.yml)

Avoid a redundant producer download when the protocol and retained-artifact
contract do not require one. Prove the fresh consumer path and a consumer-only
rerun instead. This removes repeated network transfer while exercising the job
that actually depends on the bytes. Use immutable attempts so a retry cannot
silently read another producer's output.

Exercise missing, truncated, corrupt and expired data, a lost commit response,
read-only consumers and canceled producers. A streaming reader error cannot
become successful publication of partial bytes. An unknown commit response may
mean the server accepted the data; confirm authoritative state before deleting
possibly committed objects. Keep keys, signed URLs and credentials out of
public evidence.

The [retained-artifact contract](../retained-artifacts.md) specifies
admission, authoritative acknowledgement, deletion protection, expiry and
transfer phase evidence. Its source and gateway qualification are recorded
separately from actual application OIDC and fresh-consumer acceptance. Keep the
qualified required-transfer provider until those application gates pass.

Publish a new proof atomically after validation. Invalidate an old output
receipt before a supported new attempt starts, including initialization
failures. Required consumers must not confuse a previous `passed: true` file
with the outcome of the current failing attempt. Stop owned processes and clean
temporary state even when shutdown itself reports an error. The standalone
[Go probe](../../qa/go-dogfood/README.md) has real failure controls for these
receipt and cleanup cases.

## Measure packing and transfer separately

Already compressed APK/ZIP/archive payloads may spend CPU on compression with
little size reduction. The official upload action supports
`compression-level: 0`; compare it with the current level using the same bytes
and pinned client. Text logs may benefit from compression instead.
[Upload compression controls](https://github.com/actions/upload-artifact#altering-compressions-level-speed-v-size)

Record packing CPU/time, uploaded bytes, upload, download and full consumer
verification separately, plus complete job allocation. A retrieval-and-check
step is not a bandwidth benchmark. Preserve immutable provenance, digest/size
checks and retention when changing packing or transfer providers.

A failed consumer need not rebuild a valid producer solely for diagnostics.
`gh run rerun RUN_ID --failed` or `--job JOB_ID` can target failed work;
`--job` takes the database job ID, which differs from the job's web URL number.
Use this only after checking the exact producer, attempt binding, retained
deadline and current consumer policy. A changed input or unavailable artifact
needs the normal producer/fallback, and runtime assertions still execute fresh.
[GitHub CLI rerun contract](https://cli.github.com/manual/gh_run_rerun)

## Reuse trusted general validation

A manual release often repeats unit tests and analysis that a trusted workflow
already completed for exactly that commit. Resolve its state through the CI
provider, rather than trusting a local file or cache acknowledgement.

The reuse decision must bind all relevant facts:

1. Immutable repository identity, exact requested source and allowed workflow.
2. Trusted event/ref and producer authority. A fork or arbitrary writer's
   uploaded JSON does not establish a trusted main validation.
3. Latest eligible run and its current attempt, both completed successfully.
   Enumerate that attempt's required jobs and reject missing or skipped gates
   outside the documented scope policy.
4. Matching validation context, including the workflow/configuration and
   execution profile required by the release.
5. Fresh fallback when a lookup is unavailable, ambiguous, stale, incomplete,
   canceled, failing or for another commit. Preserve an explicit fresh option.

The [workflow-run API](https://docs.github.com/en/rest/actions/workflow-runs)
and [attempt-specific job API](https://docs.github.com/en/rest/actions/workflow-jobs)
provide authoritative metadata. Keep API reads bounded and paginate job lists.
Recheck current state where the release's trust policy requires it; a newer
rerun invalidates an earlier cached verdict. Do not replace exact-source trust
with ancestry or "some recent green build."

Skip only the equivalent general validation. Current package inspection,
signing/entitlement checks, protected publication, live-provider checks and
runtime acceptance retain the release's policy. A source match does not freeze
external services, credentials or device state. A retained signed package also
needs its own explicitly qualified reuse contract.

One anonymized observed preparation stage fell from 28m18s to 3m16s after a
two-second trusted proof lookup removed repeated general validation. Signing and
inspection still ran. This is a stage measurement with an exact-source reuse
decision, not an equal-coverage comparison of two whole publishing workflows.
See the [measurement boundaries](measurement.md) before reusing the percentage.

This trusted-CI optimization is separate from sharing local task results. Local
results may make ordinary CI task commands restore quickly; they do not create
a provider-level passing commit attestation. Keep required checks present.

## Recorded inference is an input

For model-driven E2E, replay an exact recorded planner response, then execute
the actions against the current application and evaluate current assertions.
The judge needs an explicitly fresh model path when measuring current behavior.
A saved plan is not a retained verdict that the current UI passes.

Bind provider/model revision, complete messages, tools and tool results,
multimodal bytes, observations, options, execution context, recording generation
and independent sample slot. A model alias, seed or temperature alone does not
establish a reproducible output. Report recorded token usage as historical,
not newly billed inference. Fresh evals bypass both replay reads and writes.

The [existing model adapter](../../native/LLM.md) uses the upstream AI SDK model
interface. Keep native request semantics and application assertions there;
avoid another device driver or a second inference executor. Evictable replay
needs strict-miss/read-through behavior. Frozen regression corpora require a
separate retained-data policy before promising durable samples.
