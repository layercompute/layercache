# Actual private R2 qualification

Checked October 19, 2026 UTC using runtime and qualification source
`08d3df8cdff7ce859378109226c13a629ef52296`. The
[unaltered sanitized receipt](r2-private-source-20261019.json) records 45
measurements and the exact source digests. The helper binary SHA256 was
`bbba5f70ac08b3e5b18eacb68f661c151ace8de33f75e16ebdd17f28afd29e42`.

This used the production cloud store and HTTP handlers, MinIO SDK v7.0.95,
pgx v5.9.2, an isolated PostgreSQL 16 fixture and two actual private R2 buckets.
Each bucket had a separate scoped credential. Both foreign-bucket probes
returned explicit `403 AccessDenied`; both own-bucket probes succeeded. The
operator privately confirmed that the paired endpoints selected the same
account and jurisdiction. A later credential-free qualification guard enforces
that precondition for future runs. It changed no API runtime code or observed
provider operation, and the large transfer was not repeated for that guard.

## Correctness controls

Cold cache publication, exact read-only consumption and gateway restart passed.
Identical native keys stayed isolated across projects in one bucket and across
the two buckets. Current membership revocation, immutable conflicts, quota
rejection and missing/corrupt optional bytes behaved as required. Native Gradle
duplicate acknowledgements preserved the original winner; they do not establish
byte equality for equivalent native archives.

A deterministic 124,078,900-byte retained file passed admission, upload and
publication. The test deliberately dropped its HTTP acknowledgement after commit,
then inspected the authoritative published receipt and reconciled without another
upload. R2 copy preserved the full SHA256, size and committed metadata. The
unchanged provenance and deadline survived gateway restart, quota pressure and
optional cache GC. Independent read-only consumers, including a later attempt of
the same synthetic run, verified complete size and SHA256. Foreign-run access
and reader publication were denied. Over-quota admission failed before upload;
wrong declared bytes were rejected. A separate small publication reached its
real ten-second deadline, reported expired state and refused content with 410.
The large file's one-hour deadline was not allowed to elapse.

## Timing boundaries

| Operation | Observed duration |
| --- | ---: |
| Direct 4 KiB SDK PUT | 302 ms |
| Direct 4 KiB SDK copy with replacement metadata | 375 ms |
| Direct 4 KiB metadata lookup | 90 ms |
| Direct 4 KiB complete GET and SHA256 verification | 205 ms |
| 124,078,900-byte publication through lost acknowledgement | 14.163 s |
| Large committed-object metadata lookup | 67 ms |
| First large consumer, response headers / complete verified read | 315 ms / 4.212 s |
| Later-attempt consumer, headers / complete verified read | 231 ms / 3.345 s |

Publication includes streamed SDK staging, R2 copy, SQL commit and local HTTP
work. The API ran on loopback while R2 was remote. These are synthetic transport
observations, not pure bandwidth, application CI timings or a controlled speedup.
Capabilities were minted for the fixture; real GitHub OIDC, public TLS and
customer onboarding were not exercised.

Only new random qualification namespaces were touched. An exclusive protected,
fsynced journal recorded exact recovery ownership before writes. All owned
prefixes, the disposable database container and scratch were removed before a
passing receipt was exported. Provider identifiers, credentials and the private
recovery inventory are excluded from public evidence.

This establishes actual R2 persistence and protocol behavior. Hosted service
activation, recovery on its persistent volume, paid-plan lifecycle and
application adoption remain separate. GitHub artifacts remain the required
application handoff until real producer/consumer and consumer-only rerun
acceptance passes.
