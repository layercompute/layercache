# Retained private file action

This Node 24 action transfers one opaque file through the retained-artifact
protocol. It needs no LayerCache CLI, archiver or storage SDK. It uses GitHub
OIDC, the existing authenticated gateway and streaming HTTP requests. It never
extracts or executes the file.

Source implementation and disposable tests do not qualify a deployed service.
Keep the current required-transfer provider until the gateway pin, configured
policy and fresh hosted producer/consumer have passed acceptance. This action
fails on a required-transfer error. It contains no optional cache fallback.

## Identity and authority

Both jobs need `id-token: write` and `actions: read`. Checkouts also normally
need `contents: read`. The `github-token` input defaults to the ephemeral
`${{ github.token }}` and is sent only to the OIDC exchange for repository/run
eligibility checks. It is never a storage credential, receipt field or output.

The gateway verifies OIDC and GitHub run metadata. The action checks that the
returned authority matches its signed job claims and runner context. Decoding
the JWT locally does not verify it. Publication binds the signed
`check_run_id`, not the logical `GITHUB_JOB` label.

The interface permits readers in another job of the same repository, workflow,
source and run. The receipt identifies one exact producer attempt, which must
be the current attempt or an earlier attempt in that same run. This supports
a consumer-only rerun without publishing again. Future producer attempts are
denied. There is no search for a previous output or `latest` fallback. Eligible
same-repository pull requests remain isolated from other runs.

`sourceCommit` is the signed OIDC SHA, which must equal `GITHUB_SHA`. The
publishing action also runs `git rev-parse HEAD` in `GITHUB_WORKSPACE` and
requires that exact commit. Readers need no checkout when their independent
expected producer context is supplied. A PR job
that deliberately builds a head SHA while its signed identity uses a merge SHA
must resolve that policy difference before adopting this protocol. The receipt
binds an authorized producer's byte declaration; it does not attest the build
process, package safety or successful runtime assertions.

## Publish

Use `mode: publish`, `team-url`, `project`, `profile`, `name`, `path` and a fixed
`retain-until` RFC3339 timestamp. The deadline must be in the next 24 hours and
use at most microsecond precision. Choose a window covering the queued consumer
job. Save and reuse that exact deadline on retries. A changed deadline or payload
under the same producing job and output name conflicts instead of renewing or
replacing publication.

The client hashes and counts the regular source file before quota admission.
It inspects the immutable publication ID, reserves capacity if needed, then
streams one complete PUT. A lost acknowledgement causes an authoritative GET,
without a blind second upload. A staging publication fails visibly and can be
reconciled on a later invocation with the exact declaration.

An illustrative producer uses a caller-approved gateway and an immutable action
commit. Replace the commit placeholder only after source and deployment
qualification. Compute the deadline once in a preparation step and preserve it
with the declaration if that step is retried.

```yaml
permissions:
  contents: read
  actions: read
  id-token: write

steps:
  # Existing checkout, build and package inspection remain here.
  - name: Choose the retained handoff deadline once
    id: retention
    shell: bash
    run: |
      node -e 'console.log("until=" + new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString())' >> "$GITHUB_OUTPUT"
  - uses: ziahamza/layercache/action/retained@<reviewed-immutable-commit>
    id: retained
    with:
      mode: publish
      team-url: https://cache.example
      project: private-widget
      profile: android-debug-x86_64-full-toolchain-v1
      name: qa-apk
      path: out/app-debug.apk
      retain-until: ${{ steps.retention.outputs.until }}
```

Pass the `receipt` output through the trusted producing job's normal outputs.
It is bounded JSON containing the complete file digest/size, immutable producer
binding and server-approved deadline. It contains no bearer token or storage
URL. `published: true` appears only after the gateway returns a published
receipt or a lost response has been reconciled to that authoritative state.
Local upload admission, a background queue and an ordinary cache PUT cannot
produce this output.

## Read

Use `mode: read` and exactly one of `receipt` or `receipt-file`. Set `project`,
`profile` and `name`, plus all `expected-*` producer fields from the workflow's
trusted context and the trusted producing job's output. `expected-job-id` is
the signed producing `check_run_id` in that receipt. It is not a new caller
job label. Do not take the remaining expected context from an arbitrary
downloaded receipt.

```yaml
- uses: ziahamza/layercache/action/retained@<reviewed-immutable-commit>
  id: retained
  with:
    mode: read
    team-url: https://cache.example
    project: private-widget
    profile: android-debug-x86_64-full-toolchain-v1
    name: qa-apk
    path: out/app-debug.apk
    receipt: ${{ needs.build.outputs.retained-receipt }}
    expected-repository: acme/widget
    expected-repository-id: ${{ github.repository_id }}
    expected-source: ${{ github.sha }}
    expected-workflow-ref: ${{ github.workflow_ref }}
    expected-run-id: ${{ github.run_id }}
    expected-run-attempt: ${{ fromJSON(needs.build.outputs.retained-receipt).declaration.producer.runAttempt }}
    expected-job-id: ${{ fromJSON(needs.build.outputs.retained-receipt).declaration.producer.jobId }}
```

The reader checks the supplied receipt, its independent expected context and
current server state before downloading. It uses read-only authority and one
full streaming GET, rejects redirects and transformed content, and checks the
complete size and SHA256 before flushing and atomically renaming the staged
file. Corrupt, partial, foreign, expired or unavailable content fails. No prior
artifact or successful receipt remains at the designated output paths after
a failed read. Keep APK inspection, Kotlin tests, signing assertions and fresh
device checks in the caller.

The `receipt-output` and `timings-output` inputs optionally select caller-owned
files. The defaults are private files in `RUNNER_TEMP`. The artifact, receipt,
timings and GitHub output paths must differ. A receipt input file should also
be separate from these output paths. No environment credential is exported.

## Transfer evidence and limits

`timings-file` contains monotonic intervals and outcomes for setup, source
hashing, identity/exchange, inspect/reserve, headers, first body, body completion,
complete size/digest verification, disk flush and atomic local publication.
It records each actual request and consumed-byte count. The built-in fetch API
does not expose separate DNS, TCP or TLS timings. Sanitized numeric
`Server-Timing` metrics are retained when supplied; absent phases stay unknown.

Body hashing and disk writes overlap the stream. Their intervals are not
additive and do not measure pure network bandwidth. Upload counters mean bytes
consumed by the HTTP request body, not a verified wire-level byte count.
Complete step and job times, publisher overhead and existing package/runtime
checks must be measured separately before claiming CI savings.

Metadata responses are bounded to 64 KiB and 30 seconds. Body operations have
a 30-minute bound and fail after two minutes without progress. HTTPS is
required outside loopback, credentials stay in memory, and error bodies and
signed URLs are not logged. There is no per-chunk proxy RPC, client retry
wrapper, archive format, cross-run alias or Public publication path.

Run the isolated client qualification with:

```bash
node --test action/retained/retained.test.mjs
```

The tests use real HTTP and real files, including a 124,078,900-byte opaque
file. The disposable provider exercises client streaming, identity binding,
lost acknowledgements, restart reconciliation, read-only denial, corruption,
truncation, admission refusal, inactivity and stale-output cleanup. It models
the protocol, and does not establish PostgreSQL/S3 durability, real GitHub
authorization, a deployed retention guarantee or a transfer speedup.
