# Upstream techniques worth measuring

Checked on 2026-10-16. These are verified capabilities of upstream tools,
with proposed experiments for a JavaScript and mobile monorepo. A documented
capability is not a measured saving in your pipeline. Keep the command,
toolchain, cold control and whole-job result beside each adoption decision.

## Run independent steps on the same runner

GitHub Actions added native concurrent steps on June 25, 2026. `parallel`
starts a group concurrently and joins it before continuing. Unlike splitting
checks into jobs, this can share one checkout and dependency installation.
Separate step logs remain available. [GitHub announcement](https://github.blog/changelog/2026-06-25-actions-steps-can-now-be-run-in-parallel/)

This workflow fragment assumes the checkout, pinned tools and dependencies are
ready. The script names are synthetic examples.

```yaml
steps:
  # Existing checkout, toolchain setup and dependency installation go here.
  - parallel:
      - name: Check styles
        run: pnpm run lint:styles
      - name: Check formatting
        run: pnpm run check:format
  - name: Run the next dependent check
    run: pnpm run check:contracts
```

For work that spans later steps, use `background: true`, an `id`, and an
explicit `wait`. Outputs and environment changes become available after the
join. A failed background step fails its wait unless that step explicitly
allows errors. There is a ten-step concurrent background limit. `wait`,
`wait-all` and `cancel` cannot have `if`. Composite actions cannot declare
background steps or parallel groups internally, although an entire composite
action can run in the background. [Current syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepsbackground)

Start with two independent commands. Account for worker pools inside each
command, RAM, shared output directories, package-manager locks and CPU count.
Two steps that each use every core may finish later together. Wait for a
required download before using it, and use a readiness check before testing
against a background server. Joining a server process does not establish that
it is ready to accept requests.

The runner implementation includes the execution engine in
[this upstream change](https://github.com/actions/runner/commit/a3df03d35a925d4e832af530442be1eea7412df4).
This is source evidence, not a minimum supported runner-version claim. Check
your hosted or self-hosted runner and workflow parser before adopting the
syntax. The inspected actionlint parser rejects these new keys. Keep required
lint checks intact; use an existing task scheduler until parser support is
available, or qualify a small, explicit compatibility layer separately.
[Inspected parser](https://github.com/rhysd/actionlint/blob/011a6d15e749bb3f2d771eed9c7aa0e7e3e10ee7/parse.go#L1122)

Adoption gate: observe overlapping real steps, unchanged command coverage,
failure propagation and cancellation on the target runner. Compare the entire
job against the sequential version. A third-party percentage is not your
baseline.

## Five complementary experiments

### Give the native task graph smaller reusable units

Split independent deterministic test groups into tasks through the existing
runner. Preserve setup, all test files, typechecking and failure behavior.
Declare every file output that a later task needs. For source-based typechecks,
Turborepo documents transit tasks that propagate dependency changes without
serializing all checkers. Use ordinary build dependencies when the checker
needs generated declarations. [Task configuration and transit tasks](https://turborepo.dev/docs/crafting-your-repository/configuring-tasks#dependent-tasks-that-can-be-run-in-parallel)

Keep `$TURBO_DEFAULT$` when narrowing default inputs. Exclude a generated
evidence directory only after proving it cannot affect the result. Hash
behavior-changing environment values with `env` or `globalEnv`; passthrough
values are available at runtime without affecting the hash. [Input defaults](https://turborepo.dev/docs/crafting-your-repository/configuring-tasks#restoring-defaults-with-turbo_default),
[environment identity](https://turborepo.dev/docs/crafting-your-repository/using-environment-variables)

Proposed control: restore in an empty checkout, change one test, change a
dependency interface, change a compiler option, and rerun a deliberately
failing test twice. Generated evidence alone should leave unrelated task
hashes unchanged. Keep live-provider tests and deployment effects fresh.

### Make dependency image layers depend on dependency inputs

`pnpm fetch` fills the store from a lockfile and configuration before the
application source is copied into a Docker image. An offline installation can
then use that store after source arrives. Include workspace configuration and
patches before fetching. Local `file:` dependencies are skipped by fetch and
need their source at installation. [pnpm fetch](https://pnpm.io/cli/fetch)

This is a candidate for maintained runner or container images, rather than an
automatic improvement on ephemeral runners. An archive restore may cost more
than registry downloads. Measure fetch, extraction, linking, lifecycle scripts
and save separately. Keep the project's pinned pnpm version and frozen
lockfile. A newer bootstrap action or package-manager major needs its own
compatibility review.

Proposed control: compare fresh install, warm registry/store, restored archive
and prebuilt image with the same dependencies. Change a patch and lockfile to
prove invalidation. Publish the whole-job result, including image pulls.

### Measure Gradle configuration separately from task execution

Gradle's build cache reuses task outputs keyed by declared inputs. Cacheable
task implementations opt in; copying a large home directory is not the same
contract. Use native `FROM-CACHE` outcomes to distinguish restored work from
`UP-TO-DATE` work in an already populated checkout. [Build cache](https://docs.gradle.org/current/userguide/build_cache.html)

The configuration cache instead retains the configured task graph. It has
different inputs and plugin requirements. Enable and qualify it separately
from the output cache on the project's pinned Gradle and plugin versions.
Current documentation may describe behavior newer than those pins.
[Configuration cache](https://docs.gradle.org/current/userguide/configuration_cache.html)

Proposed control: use independent Gradle homes and deleted outputs for the
consumer. Measure configuration, dependency resolution and task execution.
Change source, compiler flags and build logic independently. Keep failed-task
reruns, read-only clients, outage behavior and storage pressure in the proof.
Do not upload a global developer home containing unrelated projects or
credentials.

### Avoid rebuilding a compatible native development runtime

Expo's fingerprint workflow can find a compatible development build before
starting a new native build. Its native identity includes dependencies,
configuration and native project files. A JavaScript-only change can use an
existing development runtime. [Development-build workflow](https://docs.expo.dev/tutorial/cicd/development-builds/#skip-unnecessary-builds-with-fingerprints)

Expo also exposes build-provider lookup/upload hooks for local `expo run`
commands. Those hooks do not intercept `eas build`, and iOS physical-device
builds are excluded. Cached artifacts retain their embedded build numbers.
[Provider contract and limits](https://docs.expo.dev/guides/cache-builds-remotely/)

Proposed control: bind reuse to platform, simulator or device, build profile
and exact toolchain, then install and launch with current JavaScript and
assets. A native match does not establish the current application's runtime
behavior. A development client, bundled simulator Release app and signed
device release each need their own qualification.

On native misses, Xcode's compilation cache can reuse Swift and C-family
compilations. Enable diagnostic remarks and measure actual compiler reuse
before adding another transport. [Xcode 26 release notes](https://developer.apple.com/documentation/Xcode-Release-Notes/xcode-26-release-notes),
[build settings](https://developer.apple.com/documentation/xcode/build-settings-reference)
Linking, bundling, signing and runtime checks still have separate costs.

### Shard browser tests only after measuring setup and contention

Playwright supports native sharding and mergeable blob reports. File-level
shards can be uneven; `fullyParallel` permits finer distribution when tests
are independently runnable. Use separate test accounts, ports and artifact
paths where needed. [Sharding and report merging](https://playwright.dev/docs/test-sharding)

Playwright recommends conservative worker counts in CI. It also advises
against browser-binary caching by default because restore time can match
download time, and Linux system dependencies still need installation.
Installing browsers while an independent build runs is another candidate for
native step overlap. [CI worker and browser-cache guidance](https://playwright.dev/docs/ci#caching-browsers)

Proposed control: compare one job with bounded workers against two shards,
including duplicate setup and report merging. Keep every test and current
assertion. A cached build, authentication fixture or recorded agent action
does not constitute a fresh E2E pass.

## Reuse upstream code through its supported interfaces

These source snapshots record what was inspected, not versions to upgrade to
without testing. Upstream licenses permit building on these projects; retain
the applicable license notices when redistributing code. This note adds no
vendored code or runtime dependency.

| Upstream | Inspected source and license | Small existing interface to use |
| --- | --- | --- |
| Actions runner | [MIT at `796cc18`](https://github.com/actions/runner/blob/796cc18ffaaafd5cea525d6231f5a8c93c11c603/LICENSE) | Native workflow steps |
| actionlint | [MIT at `011a6d1`](https://github.com/rhysd/actionlint/blob/011a6d15e749bb3f2d771eed9c7aa0e7e3e10ee7/LICENSE.txt) | Existing workflow parser, with its current limitations |
| Turborepo | [MIT at `fc95b6e`](https://github.com/vercel/turborepo/blob/fc95b6e5cee978fe741220e4c863534a25f90723/LICENSE) | Task graph, complete task hashes and native cache protocol |
| pnpm | [MIT at `1d90999`](https://github.com/pnpm/pnpm/blob/1d909993c4edc08b0136755bd9a43a8afb2882ea/LICENSE) | Lockfile, fetch and offline install |
| Gradle | [Apache-2.0 at `7cb22ac`](https://github.com/gradle/gradle/blob/7cb22acddadd13ae6527d6f16d1762c26a9fbf4e/LICENSE) | Native task output cache and HTTP client |
| Expo | [MIT at `aaf1537`](https://github.com/expo/expo/blob/aaf153746ff4eba1f8399eff9b08e804114bf1c2/LICENSE) | Fingerprint and build-provider hooks |
| Playwright | [Apache-2.0 at `034f230`](https://github.com/microsoft/playwright/blob/034f230cc68b286055186f4d9783188536f2f3cf/LICENSE) | Test runner, sharding and reports |

Keep scheduling and invalidation in these tools. Add storage, access policy
and bounded transfer where needed, then prove that avoided execution exceeds
lookup, verification, setup and transport costs.
