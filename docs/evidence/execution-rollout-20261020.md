# Execution inventory rollout

On October 20, 2026 UTC, the operator deployed and installed reviewed source
[`24a7ed4`](https://github.com/ziahamza/layercache/commit/24a7ed4fe15a89916a627c4a90f28935bcd3baab).
[PR #73](https://github.com/ziahamza/layercache/pull/73) added the inventory;
[PR #74](https://github.com/ziahamza/layercache/pull/74) corrected Darwin
concurrent identity creation and browser navigation/session handling. The final
merge tree matches the reviewed source tree. All fifteen final PR checks and
review statuses passed. The normal post-merge macOS job passed full Go tests,
vet, the binary build and twenty repetitions of the existing 24-worker identity
test in the [normal main run](https://github.com/ziahamza/layercache/actions/runs/39441031441).
See [source qualification](execution-observability-20261020.md) for the
earlier failures, corrections and offline controls.

## Installed clients and live dashboard

The installed CLI authenticated for five configured projects and ran a no-op
command for each.
Saved project configurations remained unchanged and their unused Local artifact
directories remained absent. The hosted dashboard showed one persistent
installation shared across all five projects, with one workspace filesystem
counted once. Offline comparison confirmed that both identities stayed stable
across the two deployments. These are location and resource observations. Cache
activity remained unconfirmed, and the captured inventory contained zero CI jobs.

Fresh authenticated Chromium, Firefox and WebKit runs each passed all 44
original scenarios, for 132 of 132 checks and zero page exceptions. The served
asset matched the reviewed build in each run. Across the three engines,
45 cache-metric projections and 45 execution-panel comparisons matched their
captured API responses. The latter used 225 per-project inventory responses
across repeated renders, not 225 distinct projects or artifacts.

All five projects were checked at desktop and mobile widths. Narrow 320-pixel
containment was checked on the final selected project. Reporting periods,
cross-project identity union, unknown effective CPU/memory limits, filesystem
accounting, refresh/reload, credential-safe connection examples and cancel-only
prune controls passed. Actual project caches were Team scoped; Personal and
Public explanations were checked without creating projects in those scopes.
All final sessions were signed out and revoked. No password, cache-pruning or
membership mutation occurred during this acceptance.

## Compatibility and rollout limits

Forty-five existing native artifact reads passed across five projects and three
current/compatibility origins. Signed Actions archive reads and wrong-key,
wrong-profile and foreign-project denials also passed. These operator-protected
read fixtures made no publications or deletions. They do not establish a new
independent CI producer/consumer pair or retained-artifact acceptance. Runtime
verification preserved protected authentication/configuration files and other
containers; the authenticated browser checks were a separate acceptance step.

Resource observations add no dimensions to artifact keys. Native compatibility
keeps relevant target, toolchain and declared input context. Different authorized
references can share one identical payload within a Local store or hosted
project namespace. Automatic portable logical sharing and a bounded local hot
cache shared across worktrees remain [TODOs](../../TODOs.md). Separate projects
do not yet share a global hosted blob pool.

Application clients with older immutable action pins still need observation
adoption. No extra native jobs were dispatched to populate inventory. Native
Gradle, Nx, Bazel and Xcode usage is not fully metered, and no provider cost
ledger or monthly savings is established. Required pipeline handoffs remain on
GitHub artifacts until the application's retained-artifact acceptance passes.
