# Bazel and LayerCache

Checked on 2026-10-19 against primary sources and LayerCache source
`90b07ca0e83117a638604bb7a7944ad89483ff3e`. This is a product and integration
decision, with no new benchmark or application migration.

Bazel already supplies much of the build/test reuse that LayerCache wants to
make accessible across local machines and CI. For a well-modeled Bazel project,
LayerCache should preserve its native graph and offer a compatible backend.
There is no evidence that LayerCache is a better build engine or a faster Bazel
cache today. Bazel support was unimplemented at that baseline. The subsequent
[native HTTP integration](../bazel-cache.md) records the first source slice;
installation, application adoption and remote execution remain separate gates.

## What Bazel already caches

Bazel describes work as actions with declared inputs, outputs, commands and
environment. Its remote cache has an action cache mapping action hashes to
result metadata, and a content-addressable store holding output bytes. A hit
can reuse another developer's or CI machine's compatible output. Remote caching
does not execute missing actions remotely. That requires a separate remote
execution service. [Remote caching](https://bazel.build/remote/caching),
[remote execution](https://bazel.build/remote/rbe).

Its native layers cover different costs:

| Layer | Existing benefit |
| --- | --- |
| Incremental build state | Reuses valid outputs in the current output base. |
| Repository cache | Shares checksum-addressed external downloads across workspaces. |
| Disk action cache | Shares action results across branches and checkouts on one machine. |
| Remote action cache and CAS | Shares compatible build outputs across machines. |
| Test results | Reuses eligible test outcomes when their inputs remain valid. |

See [remote and disk caching](https://bazel.build/remote/caching),
[repository cache](https://bazel.build/reference/glossary#repository-cache) and
[test result policy](https://bazel.build/docs/user-manual).
The default test policy reruns after changed dependencies, previous failure,
requested repeated runs or the `external` tag. A cached passing test is a
previous observation. Tests of live services, current devices or other external
state need an explicit freshness policy; a commit identity cannot substitute
for that state.

Reuse depends on complete declarations and compatible execution context.
Bazel documents cache hazards from undeclared tools and inputs changing during
execution. A cache backend cannot repair those declarations.
[Known cache limitations](https://bazel.build/remote/caching#known-issues).

## Storage savings are already part of Bazel

Bazel can avoid downloading intermediate remote outputs through
`--remote_download_outputs=minimal` or `toplevel`. Local actions still need their
inputs, and final consumers may need actual binaries. These native options can
reduce local bytes and transfer without adding a filesystem layer.
[Build without the Bytes](https://blog.bazel.build/2023/10/06/bwob-in-bazel-7.html).

Native disk-cache size and age garbage collection arrived in Bazel 7.4.
Separate output bases still exist for separate workspace paths. A disk-cache
budget therefore does not bound every output tree, temporary directory or
emulator disk. Our storage product should measure those boundaries rather than
presenting LRU eviction or content hashes as inventions.
[Disk GC](https://bazel.build/remote/caching#garbage-collection),
[output layout](https://bazel.build/remote/output-directories).

## iOS support is real, and adoption changes the build

Apple rules compile Objective-C and Swift through Bazel targets and build
application bundles using the Xcode toolchain and SDKs. `rules_xcodeproj` can
generate an Xcode IDE project whose builds use Bazel. Build-affecting IDE and
command-line configurations must match
to share cached actions. This is a maintained build-graph integration rather
than adding a remote URL to an unchanged `xcodebuild` invocation.
[Apple tutorial](https://github.com/bazelbuild/rules_apple/blob/main/doc/tutorials/ios-app.md),
[Xcode project usage](https://github.com/MobileNativeFoundation/rules_xcodeproj/blob/main/docs/usage.md).

Standard Xcode-toolchain actions still require suitably equipped Mac workers.
A remote cache hit avoids an execution; it does not make an uncached Xcode
action executable on an ordinary Linux host. Custom cross-toolchains require
separate qualification, as discussed below.
[Bazel Apple platforms](https://bazel.build/versions/9.2.0/extending/platforms).

Apple rules recommend consistent Xcode selection and document actions where
large bundles, linking or signing make remote work or transfer unattractive.
Do not promise that every Apple step should be cached. Migrating a generated
mobile project also requires qualifying its generated sources, dependencies,
resources, scripts and signing configuration. That migration cost is separate
from adding a compatible cache backend.
[Apple configuration and remote performance](https://github.com/bazelbuild/rules_apple/blob/main/doc/common_info.md#optimizing-remote-cache-and-build-execution-performance).

## Where LayerCache can add value

Our useful scope is a common compute and storage service across existing
toolchains. Native clients still decide task identity and execution. Current
integrations include Turbo, Nx, Gradle and local Xcode Clang CAS with closed-store
CI snapshots. Our [Xcode adapter](../xcode-cache.md) has no qualified external
Swift compilation replay or shared native network endpoint. Swift already loads
Apple's bundled plugin; the [language-specific evidence](xcode26-compilation-cache-20261019.md)
does not establish why its source tasks miss. Bazel's Swift action caching uses a different
interface. Nx/Gradle Local-to-private forwarding is still missing. Self-serve
hosted cloud is not launched. The [README](../../README.md) and
[integration plan](../oss-integration-plan.md) distinguish delivered behavior
from planned collaboration.

Retained handoffs have a different contract from an evictable action cache.
They bind provenance, admit capacity before success and preserve immutable
bytes through a fixed deadline. They are useful when a later job must receive
an exact file. Recorded LLM replay is another separate mechanism, implemented
through AI SDK middleware today. Neither feature follows automatically from
using Bazel. See the [retained contract](../retained-artifacts.md) and
[LLM middleware](../../native/LLM.md). Managing abandoned generated data remains
[planned work](../../TODOs.md), not a claimed host-wide storage cap.

Managed Bazel providers already overlap this service scope. BuildBuddy documents
cache analytics, read-only credentials and cache partitions. EngFlow documents
authentication, cache permissions and metrics. Authentication, remote storage
and cache visibility alone cannot establish differentiation.
[BuildBuddy cache](https://www.buildbuddy.io/remote-cache/),
[EngFlow authentication](https://docs.engflow.com/re/config/authentication.html),
[EngFlow metrics](https://docs.engflow.com/re/monitoring/metrics.html).

## Recommended integration and acceptance

Reuse the Apache-2.0 `bazel-remote` executable behind an authorization gateway.
The research examined source `1605e32b5921d05e373407b98b3c021e40c277da`.
The first [HTTP integration](../bazel-cache.md) instead pins the released 2.6.3
executable, avoiding an import of the newer engine's Go 1.26 module into the
Go 1.25 CLI.
It already provides HTTP/gRPC caching, bounded LRU storage and compressed CAS.
Its Go library APIs have no stability guarantee. CAS instance names are ignored;
action-key mangling does not isolate CAS. Use authenticated project routing and
isolated stores. Preserve Personal, Team and trusted-publisher Public authority.
[Pinned implementation](https://github.com/buchgr/bazel-remote/blob/1605e32b5921d05e373407b98b3c021e40c277da/README.md),
[license](https://github.com/buchgr/bazel-remote/blob/1605e32b5921d05e373407b98b3c021e40c277da/LICENSE).

Qualify a real cold producer and independent read-only consumer, changed inputs,
toolchain misses, failed-test reruns, corrupt/missing CAS, revocation, project
isolation, quota pressure and restart behavior. Measure complete build/test time,
transferred bytes and total allocated storage, including native output trees.
Do not repackage `bazel-out` behind a second fingerprint. Add this integration
when an actual Bazel workload justifies it; migrating an existing application
solely to obtain remote caching needs its own measured case.

## Reduce Mac work before migrating an Expo app

Bazel can place compatible actions on Linux and Apple-dependent actions on Mac
workers. Execution platforms and execution groups express that split, including
different tools within one target. The rules must declare those actions and
their toolchains. These settings do not extract individual compiler commands
from an existing `xcodebuild` invocation. A cache backend supplies stored results;
an execution service schedules missing actions. Moving work to another Mac,
reusing a cached result, and avoiding the native job entirely have different
costs. [Execution groups](https://bazel.build/extending/exec-groups),
[remote execution](https://bazel.build/remote/rbe).

A Linux machine can drive a suitably configured build, but the default Apple
setup is insufficient. Local Xcode discovery emits a stub on non-Darwin hosts.
Remote builds need explicit remote Xcode metadata, registered execution platforms,
compatible toolchains and remote-safe helper tools. The standard Xcode Swift
toolchain selects macOS executors. Portable C++ source still needs a toolchain
that produces the required Apple target and ABI; an ordinary Linux compile does
not produce an interchangeable iOS object. Custom cross-toolchains need separate
qualification. [Xcode discovery](https://github.com/bazelbuild/apple_support/blob/d4595d8f66887617b084a57279544c9e6f2e9261/xcode/xcode_configure.bzl),
[remote Xcode selection](https://bazel.build/docs/bazel-and-apple#xcode-selection),
[Swift execution constraints](https://github.com/bazelbuild/rules_swift/blob/b7feaf9e7e0d0dabcdbe89b706d331de276f44b6/swift/toolchains/BUILD).

For a React Native application, platform-neutral lint, typechecking, JavaScript
tests, Metro bundling and qualified Node-based code generation are Linux
candidates. Generating Objective-C++ files is distinct from compiling them.
Standard Apple SDK-dependent compilation, linking and resource processing stay
on suitably equipped Mac workers. Fresh iOS simulator assertions also need an
Apple runtime, even after every compilation action hits cache. Current Apple
bundling rules and test execution groups constrain execution to macOS.
[React Native code generation](https://reactnative.dev/docs/the-new-architecture/using-codegen),
[Apple rule constraints](https://github.com/bazelbuild/rules_apple/blob/main/apple/internal/rule_factory.bzl).

When native inputs rarely change, our recommended first experiment is a complete
native fingerprint and reusable native shell. Linux can produce the new
JavaScript/assets, and a compatible shell can avoid compilation. Changed native
dependencies, configuration plugins or SDKs must rebuild. Qualify fingerprint
coverage of patched/generated native inputs and pin the JS/Hermes toolchain,
environment and assets. Expo documents repack
for QA and internal distribution, with production store signing and symbolication
limits. Simulator `.app` files need no device signing. Run Cloud specifically
documents Release simulator rebundling, which does not establish signed-device
support. Expo also exposes an experimental Linux `zsign` signing backend; its
existence is no evidence of application acceptance. Compatible OTA updates can
avoid making a new installable artifact, subject to release policy.
[Expo repack](https://docs.expo.dev/build-reference/repack/),
[Run Cloud rebundling](https://docs.run.cloud/ios/build-with-xcode#rebuild-only-javascript),
[Expo signing implementation](https://github.com/expo/eas-cli/blob/f949f3430e66113194343456af39920510dde27c/packages/build-tools/src/steps/functions/repack.ts),
[experimental signing schema](https://api.expo.dev/v2/workflows/schema),
[fingerprint inputs](https://docs.expo.dev/versions/v58.0.0/sdk/fingerprint/),
[Hermes compatibility](https://docs.expo.dev/guides/using-hermes/),
[OTA runtime compatibility](https://docs.expo.dev/eas-update/runtime-versions/).

Turbo already caches configured package scripts and their declared outputs.
Bazel can model compiler actions and dependency edges inside the native build,
giving finer reuse when a package task misses. A Turbo task wrapping
`xcodebuild` does not acquire that internal graph. Either system still needs
correct inputs and a separate policy for fresh runtime checks.
[Turbo task configuration](https://turborepo.dev/docs/reference/configuration#tasks).
We therefore recommend qualifying shell reuse before migrating an existing
mobile graph solely to reduce Mac minutes. Compare cold publication, warm consumers,
native changes, transfer/storage, signing and fresh runtime costs. This research
establishes neither lower Mac consumption nor a Bazel advantage over the current
workflow.
