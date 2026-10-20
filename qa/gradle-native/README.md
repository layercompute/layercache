# Real Gradle HTTP cache acceptance

Run `GRADLE_BIN=/absolute/path/to/gradle-9.3.1/bin/gradle node qa/gradle-native/run.mjs`
from the repository root. Java 17 and Go are required. The runner rejects a
different Gradle version or Java language toolchain. `LAYERCACHE_BIN` may select
an already-built candidate by absolute path; otherwise it compiles the current
CLI. This fixture has no downloaded project dependencies. Gradle must have
network access to the disposable loopback HTTP endpoints; `--offline` disables
Gradle's HTTP build cache.

`pnpm qa:gradle-native` is the normal Linux CI gate. It runs the action's unit
controls, all seven public-listener controls below, then the native HTTP matrix.
Its existing `GRADLE_QA_OUTPUT` receipt includes the sanitized listener summary;
raw logs and private task identities stay in local scratch. Each ordinary
command is bounded to 120 seconds; the seven-command listener subprocess has a
separate ten-minute total limit.
The Linux CI gate also runs the seven listener controls with pinned Gradle
9.4.1. Distribution ZIPs are cached together and checked against their published
SHA256 on every run before extraction; the 9.3.1 HTTP matrix remains unchanged.

The fixture uses Gradle's standard `JavaCompile` task and an explicitly cacheable
verification task that launches the compiled class and checks its output. Source,
compiler release option, verification inputs, and runtime version/vendor/OS/arch
participate in native task identities. The profile also fingerprints the Java
version output. Execution counters stay outside the workspace and task inputs.
This is Java protocol qualification. It does not establish Phone Android task
correctness, Android build savings, a second actual JDK, or live device behavior.

Each producer and fresh consumer has a separate workspace and empty Gradle User
Home. The shipped init script configures the native `HttpBuildCache`; the runner
explicitly enables `--build-cache` and disables Gradle's own filesystem task
cache. Checks cover Local and direct private production, empty-consumer exact
class/verdict restoration, daemon restarts, source/declared-input/compiler-option
misses, incompatible profiles with identical native hashes, failed-task reruns,
read-only publication denial, revocation, quota rejection, outages, and explicit
`--no-build-cache`. Init-script controls cover an existing remote, no configured
endpoint, partial environment and unsafe remote HTTP.

A loopback recording proxy observes native requests and records only method,
native hash, status and transferred byte counts. A deliberate GET-miss control
forces a second native producer to execute and PUT existing keys. The retained
winner's complete archive digest stays unchanged even though Gradle adds new
origin metadata. This control is identified in the receipt. Archive downloads
for independent digest checks go directly to the service and are outside those
native-stage transfer counts.

Pinned Gradle 9.3.1 treats HTTP 413 as a cache warning and disables its remote
cache for the remaining build; task execution still succeeds. The runner checks
that behavior directly. Read-only publication and membership revocation also
disable remote caching after the denied operation. The fixture does not promise
that these failures preserve subsequent remote lookups in the same build.

The runtime directory is mode 0700. Raw logs, short-lived credentials,
configurations and Gradle state remain there for local inspection. Do not upload
that directory. Set `GRADLE_QA_OUTPUT` to an absolute new JSON path to export only
the sanitized receipt, mode 0600. It contains source revision, binary and init
script digests, native hashes, output/archive digests, task outcomes/execution
counts, per-request byte counts and command wall times. Each Gradle command is
bounded to 120 seconds and 8 MiB of logs. Daemons and command process groups are
stopped on success, failure and signals before success evidence is exported.

Whole command times include fresh Gradle initialization, configuration and
transfer. The qualification's total also includes CLI/helper compilation, server
startup and cleanup. They are fixture timings, not controlled Phone build or
billing comparisons. Gradle's separate filesystem state remains outside
LayerCache's byte budget. Local-to-private forwarding and Public caching remain
unimplemented in this slice.

## Native metering controls

The HTTP acceptance runner also collects public native completion events,
correlates each invocation's exact info log and submits its bounded projection
through a fresh synthetic member-local capability. The disposable token helper
can name only the fixture's project and uses its private test signing key. The
wire projection contains no raw task paths. Diagnostic archive downloads retain
unscoped fixture credentials so they cannot become accepted task hits.
Aggregate report assertions verify the eligible denominator, accepted hit count,
Local/Team source, original first-publication operation duration and restart
retention. A duplicate producer with a different archive cannot replace the
original producer duration. Producer resources and provider charges remain
unknown in this fixture.

Run the separate public-listener controls with:

```sh
GRADLE_BIN=/absolute/path/to/gradle-9.3.1/bin/gradle \
  node qa/gradle-native/metering.mjs

GRADLE_BIN=/absolute/path/to/gradle-9.4.1/bin/gradle \
  GRADLE_METERING_VERSION=9.4.1 node qa/gradle-native/metering.mjs
```

Its small generated text tasks cover root and multiple projects, an included
build, `buildSrc`, parallel execution, configuration-cache storage and reuse,
accepted cache restores, up-to-date/no-source/skipped/failed outcomes, explicit
cache disabling and equivalent tasks sharing one native key. Comparing the
original init script with the metered script checks unchanged native keys.
The immutable baseline is vendored in
`fixtures/pre-metering-init.gradle` from
`f49b92adf365b0a877f588f884d7e3b885b1fdfd:native/gradle/init.gradle`, with
SHA256 `b45b47190b38f9f439d2324a49fbc5bbfdb8bf3a8671b63900c11d64ef32eaf5`.
The runner verifies those exact bytes before comparison, so qualification works
in shallow checkouts and exported source trees without Git history.
These filesystem-cache controls qualify native outcomes and projection; the
HTTP runner separately qualifies private source and producer correlation.
Neither fixture establishes application or billing savings.
The observer controls accept only those exact versions. The requested version
must match the actual runtime and every collected report. Keys are compared
with the original init script within each version, without assuming compatible
keys across Gradle versions. See the
[9.4.1 qualification](../../docs/evidence/gradle-941-metering-20261020.md).
