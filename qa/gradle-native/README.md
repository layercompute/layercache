# Real Gradle HTTP cache acceptance

Run `GRADLE_BIN=/absolute/path/to/gradle-9.3.1/bin/gradle node qa/gradle-native/run.mjs`
from the repository root. Java 17 and Go are required. The runner rejects a
different Gradle version or Java language toolchain. `LAYERCACHE_BIN` may select
an already-built candidate by absolute path; otherwise it compiles the current
CLI. This fixture has no downloaded project dependencies. Gradle must have
network access to the disposable loopback HTTP endpoints; `--offline` disables
Gradle's HTTP build cache.

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
