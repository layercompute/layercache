# Real Nx native HTTP acceptance

Run `pnpm qa:nx-native` from the repository root. The runner installs the
fixture with its frozen lockfile and builds the current LayerCache CLI and a
QA credential helper. Set `LAYERCACHE_BIN` to an absolute path to test an
already-built candidate instead. Node 24, pnpm 10.34.5, Go, and a supported
compiled Nx native package are required. The runner rejects Nx's WASM fallback.

The fixture pins `nx@23.3.0`. Its lockfile also pins the matching platform
packages. Both tasks use Nx's standard `nx:run-commands` executor and native
task hashing. Source, task definitions, the dependency fixture, package and
lock files, and the two task environment inputs are declared in `nx.json`.
Execution counters live outside the workspace and are deliberately absent
from task inputs.

The runner starts isolated loopback Local and Team runtimes in a mode-0700
temporary directory. It uses `layercache run` for the producer and Local
replays. A separate producer and fresh consumers invoke the real Nx CLI
directly against the private Team endpoint with `NX_SELF_HOSTED_REMOTE_CACHE_SERVER` and
`NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN`. Credentials carry the project,
`nx` integration, compatibility profile, and read/write authority. The native
client supplies no custom compatibility headers.

Checks cover cold build/test execution, exact binary output and terminal log
restore after deleting Nx's cache and outputs, Local reuse with Team offline,
Local and Team daemon restart, a fresh read-only direct consumer with no
native or LayerCache cache, source/dependency/environment misses, and profile
isolation with the same Nx hash. Read-only misses execute and Nx declines the
forbidden upload. Failed tests return failure and execute again with both a
warm and empty native cache. `NX_CACHE_FAILURES` is absent throughout.

Set `NX_QA_OUTPUT` to an absolute file path to export a protected, sanitized
JSON report. It contains native hashes, task cache statuses, execution counts,
archive digests, output digests, and terminal output digests. The exported
report excludes credentials, raw logs, configurations, and workspace files.
Each run retains owner-only daemon logs, native CLI logs, configurations, and
`evidence.json` in the printed disposable runtime directory. That directory
contains credentials and stays local. Do not upload it.

Owned daemons and active command process groups are stopped on success, failure,
SIGINT and SIGTERM. Cleanup attempts every child, allows five seconds for graceful
shutdown, then waits up to two seconds after SIGKILL before reporting aggregated
errors. The helper regressions use real children which ignore termination, and
check that forced shutdown cannot leave a second child running. Exported success
evidence is written only after cleanup finishes.

On POSIX, cleanup waits for the whole owned process group after its leader
closes, and escalates surviving descendants. The Linux descendant regression
uses its own subreaper so orphaned zombies do not depend on the host's PID 1.
The signal and direct-child checks also run on macOS.

The existing Linux and macOS native-client CI jobs run this acceptance check
against the candidate binary they already compile. Each matrix job uploads
only `${{ runner.temp }}/nx-native-evidence.json`. This wires the candidate
gate into CI. The [beta.4 release workflow](https://github.com/ziahamza/layercache/actions/runs/38552629557)
passed this fixture against Linux amd64, Linux arm64 and macOS arm64 candidates
before publication. The [published installer gate](../../.github/workflows/beta-release-install.yml)
also runs the fixture against downloaded, provenance-verified beta.4 binaries on
all three native platforms. Only the QA credential helper is compiled there.

Nx Local-to-Team read-through and durable publication are not implemented in
this slice. The Local producer and the direct Team producer each execute
build/test once. The direct Team producer populates the private endpoint that
the fresh read-only consumer restores from.

LayerCache's byte budget bounds only its own artifact store. Nx has a separate
filesystem cache and database. This QA runner deletes that state in its own
disposable workspaces to prove remote restores; it does not establish a total
disk cap for an ordinary Nx workspace. Nx publication confirmation, Public
Build caching, and outage fallback guarantees remain outside this check.
