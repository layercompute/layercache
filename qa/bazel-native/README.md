# Real Bazel HTTP cache qualification

Run `node qa/bazel-native/run.mjs` from the repository root on Linux amd64.
Go and Node.js are required. The runner downloads official Bazel 9.3.0 and
bazel-remote 2.6.3 releases and verifies their pinned SHA256 digests before
execution. `BAZEL_BIN` and `BAZEL_REMOTE_BIN` may select absolute paths to those
exact binaries; overrides receive the same digest verification. `LAYERCACHE_BIN`
selects an absolute candidate CLI path, or the runner builds the current CLI.

The custom Starlark fixture needs no downloaded language rules, compiler or
application dependencies. Its declared source, executable tool input and action
environment participate in Bazel's native keys. An intentionally failing native
test writes an execution counter outside the build inputs. That counter is
observational and does not qualify caching of arbitrary undeclared side effects.

The gateway is a disposable private Team service with explicit membership and
scoped read/write capabilities. Its dedicated loopback bazel-remote instance has
HTTP ActionResult validation enabled, gRPC disabled, uncompressed storage, a
1 MiB blob bound and explicit 1 GiB logical and hard limits. The pinned engine
parses these capacity flags as integer GiB. Its cache capacity is separate from
LayerCache staging, metadata and the client's output roots.

Acceptance covers acknowledged cold native publication, an independent empty
output root restoring exact bytes with a native remote-cache hit and zero PUT,
both services restarting, missing-CAS invalidation, source/environment/declared
tool misses, failed-test execution twice, read-only writes, foreign project,
profile and integration denial, member and signing-key revocation, wrong-digest
and oversize rejection, and incomplete uploads remaining unavailable. An
observing proxy records methods, paths, statuses, bytes and timing without
recording Authorization headers. Native cache flags carry disposable fixture
tokens; do not capture process arguments or upload the private runtime directory.

Each native command is limited to 120 seconds, one job, a 384 MiB JVM heap and
4 MiB of output. One embedded JDK installation is shared across independent
output roots. Ordinary generated scratch is capped at 1 GiB. Optional
`BAZEL_QA_PRESSURE=1` writes at most 1,050 generated 1 MiB CAS objects through the
gateway, with a 120-second deadline, 5 GiB free-space floor and 1.5 GiB owned
scratch cap. This exercises capacity admission and a subsequent native reader;
it is omitted from the ordinary small fixture run.

Set `BAZEL_QA_EVIDENCE` to an absolute new JSON path for the sanitized receipt.
Otherwise a unique receipt is written in the system temporary directory. The
receipt contains exact release/source/binary digests, native runner counts,
complete command times, HTTP byte/timing totals, controls and allocated storage.
All owned processes stop before the successful runtime directory is removed.
On failure, the bounded mode-0700 runtime remains for local diagnosis. The
runner removes no existing cache, worktree or operator configuration.

These are synthetic Linux protocol checks. They establish no application
migration, Apple build support, remote execution, deployment, CI savings or
monthly bill result. HTTP request timings include staging and engine work;
sums of overlapping request durations are not complete command wall time.
