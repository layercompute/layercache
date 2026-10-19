# Opt-in actual R2 persistence qualification

This runner uses real provider storage. It requires explicit
`--allow-provider-writes`, two absolute protected credential-file paths and an
absolute **new** `--evidence-file` path. It is never selected by ordinary CI.
Run from the repository root after reviewing the exact source:

```sh
go run ./qa/r2-cloud \
  --allow-provider-writes \
  --primary-credentials-file /protected/primary-s3.json \
  --secondary-credentials-file /protected/secondary-s3.json \
  --evidence-file /protected/new-r2-receipt.json
```

Each credential file must be a regular JSON file with no group/other permissions.
It supplies `provider`, `poolId`, `endpoint`, `bucket`, `region`, `accessKeyId`
and `secretAccessKey`. Existing private buckets and distinct bucket-scoped S3
credentials are mandatory. Credentials are read inside the process; they are
never passed as argument values, printed or included in evidence. Error output
contains bounded operation labels and provider error codes. Do not enable HTTP
or SDK debug logging or capture process memory.

Before any provider work, the runner reserves the new receipt and an adjacent
mode-0600 `.cleanup.jsonl` journal with exclusive creation. It fsyncs the journal
and records exact owned bucket/prefix, credential-file reference, container name
and local scratch path **before** creating or writing each resource. This
private recovery inventory is never copied into public evidence. It remains on
failure, including cleanup failures; a completion record distinguishes complete
cleanup from resources needing operator recovery. Existing output paths fail
before a provider retry can start.

The runner starts one uniquely named PostgreSQL 16 container, pinned by digest,
with a 256 MiB tmpfs, 512 MiB memory cap, one CPU and an ephemeral loopback port.
It never uses an existing database. It runs the actual Go HTTP server and
production cloud store with the repository-pinned MinIO S3 SDK and pgx versions.
Two new projects use different R2 buckets; a third project in the primary bucket
checks same-bucket project isolation. No buckets, existing projects, services,
worktrees or application workflows are changed.
Foreign-bucket controls accept explicit provider denials for buckets already
confirmed by their own credentials; DNS, timeout and provider 5xx errors fail
qualification instead of counting as denied access.

A 4 KiB direct SDK probe measures PUT, copy with replacement metadata, metadata
lookup and complete SHA256-verified GET separately. Opaque HTTP cache controls
cover cold publication, read-only consumption, restart persistence, project
isolation, current membership, quota, immutable conflict and missing/corrupt
bytes. Gradle's native duplicate acknowledgement retains the original winner;
it does not assert byte equality for equivalent native archives.

A deterministic 124,078,900-byte stream exercises the existing retained API.
There is no large fixture file. The producer reserves a complete immutable
declaration before upload, deliberately loses only the committed publication's
HTTP acknowledgement, then reconciles through authoritative inspection without
uploading again. R2 metadata must preserve full SHA256, size and committed state.
Read-only independent consumers, including a later attempt of the same run,
verify complete bytes after gateway restart. Fixed deadlines, foreign-run
denial, protected quota shrink and retained isolation from optional cache GC are
checked. Over-quota declarations fail before content upload; wrong-content
uploads never publish. A separate 10-second 4 KiB receipt proves real elapsed
expiry and denial without expiring the one-hour large receipt.

All writes belong to random qualification prefixes or project namespaces created
by this invocation. Cleanup stops gateways and stores, then deletes only those
exact object/multipart prefixes and removes the owned PostgreSQL container and
local scratch. A passed receipt is exported only after cleanup succeeds. Overall
runtime is bounded to 12 minutes; individual HTTP transfers to three minutes.
Provider cleanup receives its own 90-second budget after cancellation.

The sanitized receipt records exact source/recipe digests, SDK versions, complete
HTTP times, response-header times, explicit `Server-Timing` fields, full hashes
and byte counts. Large publication time combines streaming SDK staging, R2 copy,
PostgreSQL commit and local HTTP work. Read-header and complete verified-read
times have different boundaries. These measurements are not pure bandwidth,
application build savings, native-client qualification, public service
activation, live GitHub OIDC, billing or aggregate storage-plan acceptance.
GitHub artifacts remain the required application handoff until its own full
producer/consumer and consumer-only rerun qualification passes.
