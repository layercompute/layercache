# Spend less time rebuilding native applications

Start with the largest native stage, then identify which layer repeated the
work. A dependency snapshot, compiler cache, task-output cache and complete
application binary avoid different work. Track each independently.

| Reusable work | Native authority | Work that still happens |
| --- | --- | --- |
| Downloaded dependencies | Package manager and lockfile | Installation, generation and compilation as needed |
| Compiler outputs | ccache, LLVM/Xcode or another qualified compiler cache | Build graph, linking, packaging and runtime checks |
| Gradle task outputs | Gradle task inputs and build-cache key | Configuration and every missed or non-cacheable task |
| Complete development build | Expo fingerprint plus build profile | Install, launch and current JavaScript/runtime assertions |
| Release artifact handed between stages | Exact artifact manifest and retained bytes | Inspection, signing and publication required by that stage |

## Keep the native executor and its keys

Enable and inspect the tool's existing cache before adding storage around it.
For Android, separate Gradle's dependency cache, configuration cache, task
build cache and C/C++ compiler cache. Each has a different lifecycle. A restored
Gradle home does not prove that a task avoided execution.

Start an isolated measurement with the project's pinned wrapper:

```sh
./gradlew --build-cache --info :app:assembleDebug
```

The module and variant must match the project. Record native outcomes such as
`FROM-CACHE`, `UP-TO-DATE` and executed task actions separately. A fresh checkout
with existing outputs is not an empty consumer. Gradle's
[build-cache contract](https://docs.gradle.org/current/userguide/build_cache.html)
defines reusable task outputs. Its
[configuration-cache adoption guide](https://docs.gradle.org/current/userguide/configuration_cache_adoption.html)
describes a separate optimization; qualify the real task graph before adopting it.

For C/C++ work, retain the build system's compiler-cache integration and inspect
its own statistics. Keep actual compiler, target architecture, build flags and
path assumptions correct. The [ccache manual](https://ccache.dev/manual/latest.html)
documents its hashing and path-handling behavior. Avoid broad ignore settings
merely to increase the displayed hit rate.

Bound Gradle workers, JVM memory and native compiler parallelism together. Keep
room for the emulator and operating system when they share a runner. Moving
compilation into its own job can make the budget easier to control, but count
artifact transfer and duplicate setup in the result.

## Reuse Go compilation while executing tests fresh

Go's native build cache can reuse compilation without replaying a successful
test result. Preserve the project's normal test coverage and disable result
replay when current test execution is required:

```sh
go test -count=1 ./...
```

Keep race checks and vet when they are part of the lane's contract. The
[Go cache contract](https://pkg.go.dev/cmd/go@go1.25.13#hdr-Build_and_test_caching)
describes the distinction. Use the pinned `actions/setup-go` cache integration
before building another archive client. Its native key includes Go version,
OS/architecture and dependency-file hashes. Add the actual runner image and
effective build-settings profile when qualifying stricter compatibility;
disable reuse if that profile cannot be established. Go still owns invalidation
for source, compiler and flag changes.

LayerCache's [routine hosted lanes](../self-ci.md#native-go-compilation-reuse)
use GitHub's cache transport and execute tests with `-count=1`. The
[opt-in standalone probe](../../qa/go-dogfood/README.md) separately exercises
LayerCache archive storage over loopback. Its first use was slower, and it does
not qualify a replacement for the routine hosted transport. Measure complete
setup, restore, fresh execution and save costs before changing that choice.

## Use a remote task cache without replacing Gradle

LayerCache's Gradle adapter accepts native HTTP archives. Gradle still owns
hashing, execution, packing and restoration. The adapter uses the existing
bounded artifact engine rather than another task executor.

The source-qualified slice supports standalone Local and direct authenticated
Personal or Team endpoints. It does not yet forward Local Gradle writes or
misses automatically to a private remote. A Local acknowledgement therefore
confirms Local admission only. Cross-machine qualification uses the direct
shared private endpoint.

An isolated native-client qualification ran 22 Gradle 9.3.1/JDK 17 commands. Its
empty consumers restored exact compiled and verification outputs with zero task
actions. Cold and restored Local commands took 5.382s and 4.789s; direct private
commands took 5.510s and 5.016s. Fresh process setup dominates this small Java
fixture. These numbers prove the protocol path, not Android application savings.

The qualification also checks source, compiler and profile misses, repeated
failed-task execution, duplicate writes, read-only access, revocation, quota and
outage behavior. Installation of a released adapter, deployment of a gateway and
adoption in an application workflow are separate milestones. Source support
alone does not establish any of them.

## Persist Xcode compilation state deliberately

Use the selected Xcode version's native compilation cache and diagnostic
remarks. Apple's [build-settings reference](https://developer.apple.com/documentation/Xcode/build-settings-reference)
defines `COMPILATION_CACHE_ENABLE_CACHING` and
`COMPILATION_CACHE_ENABLE_DIAGNOSTIC_REMARKS`. Qualify the selected toolchain;
settings and cache formats can change between Xcode releases.

Separate the cache from DerivedData, signed products, provisioning profiles and
keychains. Give snapshots an exact Xcode/compiler/SDK and target profile. Close
the cache owner before saving its directory. Bound snapshot generations and
bytes so that every run does not append another full archive forever.

LayerCache's Xcode adapter serves LLVM CAS and action lookups through a private
Unix socket. It currently uses local storage; runner-to-runner reuse needs a
separately qualified snapshot transport. It does not provide a Swift CAS plugin.
Preserving Swift's own native cache and proving Swift replay remain separate.

A mixed Swift/Objective-C fixture on Xcode 26.6 restored 69 of 71 action lookups
and 70 CAS objects after discarding DerivedData and the compiler's separate local
CAS. This demonstrates the adapter protocol for that fixture. It does not prove
that every Swift compilation reused the adapter or that a full application
release became proportionally faster.

CAS storage must preserve the complete graph behind a returned action hit.
Keeping only the action index or root can produce a reported hit whose child
objects have already been evicted. Qualify real compiler reads during eviction,
strict byte limits and concurrent builds. Reject an incomplete graph as a miss
and let the compiler execute.

In two historical application observations, all 1,725 cacheable compiler tasks
hit, but Macs remained occupied for 16m44s and 18m28s. Setup, dependencies,
linking, packaging and runtime acceptance can dominate after compilation is
cached. A perfect compiler hit rate is not a whole-job performance result.

## Reuse a compatible development build before scheduling a Mac

A complete native build can avoid the compilation job entirely when the native
inputs are unchanged. Use the framework's fingerprint plus explicit platform,
architecture, toolchain and build-profile context. A commit SHA alone prevents
useful reuse across JavaScript-only changes; a lockfile alone misses native
configuration and source changes.

Expo's [build-cache provider contract](https://docs.expo.dev/guides/cache-builds-remotely/)
offers native resolve/upload hooks for local `expo run:android` and
`expo run:ios`. Its documented scope excludes EAS Build and physical iOS device
targets. Treat those as separate integrations.

A shared Debug development client can load current JavaScript from Metro after
installation. A bundled Release QA app carries embedded JavaScript and needs a
key that includes that bundle's actual inputs, or a separately qualified
rebundling step. Never use a native-only fingerprint to serve stale Release
JavaScript.

For CI, perform an authenticated lookup on Linux before allocating a Mac when
the artifact and tools allow it:

1. Calculate the exact native/profile identity.
2. Resolve a retained, authorized artifact and verify its size, digest and
   required contents.
3. Schedule native compilation on a miss, unsupported identity or degraded
   lookup. Keep a clearly requested fresh mode.
4. Install and launch the selected output, then run current assertions on the
   required device or simulator.

One historical complete-artifact qualification restored 2,263,669 bytes in
2.198s on Linux and skipped its otherwise required macOS build job. The earlier
attempt failed restore staging because the storage host lacked configured free
space. Preserve capacity limits and repair the capacity problem; do not weaken
verification merely to turn a miss into a hit. This was one qualified artifact
path, not universal native-build compatibility.

## Keep Linux preparation separate from Mac execution

Inventory the commands in the Mac job. Source preparation, dependency metadata,
deterministic JavaScript checks, provider API calls and archive inspection may
run on Linux when their actual tools support it. Keep Xcode compilation,
Apple-specific signing tools and simulator acceptance on a compatible Mac.

Move a step only after proving its output contract on the new platform. Package
inspection on Linux must preserve strict archive, Mach-O, entitlement and
manifest checks that the consuming stage requires. A provider API that accepts a
build request is different from an API that exposes dependency-cache hooks on
its worker. Use documented provider hooks and count remote-worker time as well
as any waiting controller job.

Pin toolchains and install only what the job uses. Cross-compile a portable Go
helper on Linux when supported, then retain that exact binary for the Mac stage.
Use retained artifacts for required handoffs. Evictable caches are suitable for
optional acceleration and need a rebuild fallback.

## Acceptance before broad rollout

Compare a trusted producer and independent empty consumer on a compatible
profile. Retain native hit/miss and execution counts, exact restored output
digests, transfer bytes, setup time and complete command/job duration. Include
the upload/save cost in the producer measurement.

Change source, native configuration, SDK/compiler, architecture, dependency
lockfiles and relevant environment separately and verify misses. Run failing
tasks twice, test an explicit fresh bypass and preserve compiler failures.
Exercise eviction, outages, cancellation and read-only consumers. A cache must
not turn an unsuccessful build into a successful result.

Run current install/launch assertions against restored products. Keep optional
audio, long-running device suites or expensive matrix variants explicitly
selected, while the normal lane retains its stated required coverage. Record
the selected coverage in evidence; a smaller suite does not prove the omitted
behavior.

Standalone OSS use needs no LayerCache account for Local Cache or one
user-managed remote host. Managed Team collaboration, multiple enrolled hosts
and hosted storage require signup and a scoped token. Personal, Team and Public
visibility remain separate from storage hosting. Only LayerCache-controlled
Public Builds may populate Public Cache. Private artifacts keep the native
client's complete declared context.
