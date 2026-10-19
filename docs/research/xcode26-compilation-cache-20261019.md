# Xcode 26 compilation cache support

Checked 2026-10-19 against LayerCache
[`702d0f7`](https://github.com/ziahamza/layercache/tree/702d0f719d76e47799380d7c3923be3b11d549f3).
The native implementation and fixture are unchanged from the tested
[`90b07ca`](https://github.com/ziahamza/layercache/tree/90b07ca0e83117a638604bb7a7944ad89483ff3e).

## What already works

Apple introduced opt-in compilation caching in Xcode 26 for Swift and C-family
languages. It reuses compilation results when inputs match, including after
clean builds or branch switches. The public build setting is
`COMPILATION_CACHE_ENABLE_CACHING=YES`.
[Apple release notes](https://developer.apple.com/documentation/xcode-release-notes/xcode-26-release-notes),
[build settings](https://developer.apple.com/documentation/xcode/build-settings-reference).

LayerCache already integrates with that feature. The `xcode run` wrapper enables
caching, Apple's bundled plugin, the private Unix socket, detached key queries
and diagnostic remarks. Its local daemon implements the upstream LLVM CAS and
action-index protocols. It does not replace the compiler's dependency scanning
or construct separate compilation keys.
[Wrapper](https://github.com/ziahamza/layercache/blob/702d0f719d76e47799380d7c3923be3b11d549f3/internal/xcodecache/run.go#L122),
[protocol provenance](../../internal/xcodecache/proto/README.md).

| Capability | Qualified scope |
| --- | --- |
| Apple's native local compilation cache | Xcode feature for Swift and C-family languages; project compatibility still matters. |
| LayerCache Clang adapter | Clang, Objective-C, modules and PCH through a local socket and bounded artifact store. |
| Swift local CAS persistence | A separate native reuse path; preserving it does not establish external Swift replay. |
| Swift compilation replay through LayerCache | Unqualified. Actual source compilation misses remain in the mixed-language fixture. |
| Sharing between runners | Copy the closed LayerCache store with CI snapshot transport. There is no authenticated shared native TCP endpoint. |

The adapter verifies complete reachable output graphs before returning a hit,
temporarily protects those objects from eviction, and keeps the committed byte
quota in force. This prevents an action index from promising an already-evicted
descendant. That contract and its limits are documented in the
[adapter guide](../xcode-cache.md).

## What the hosted evidence proves

The [2026-10-17 hosted qualification](https://github.com/ziahamza/layercache/actions/runs/39072238914/job/117512286300)
passed on Xcode 26.6, build 17F113. Its mixed Swift/Objective-C command-line
fixture uses fresh DerivedData and deletes the native local CAS before each
consumer; only the closed LayerCache store survives.
[Fixture source](https://github.com/ziahamza/layercache/blob/90b07ca0e83117a638604bb7a7944ad89483ff3e/qa/xcode/probe.sh).

The warm consumer recorded 69 hits from 71 action lookups and 70 hits from 70
CAS reads, with zero cache errors, uncached fallback or retry. The separate
pressure consumer fetched both promised external objects after unrelated
writes forced eviction. All promised objects survived and committed usage
remained below quota.

The retained `warm.log` also records two Swift source compilation tasks with
`Replay cache miss`, one per architecture. Their action keys differ from the
cold invocation. Therefore the aggregate hit count proves protocol replay, not
complete Swift compilation reuse. The evidence does not establish the reason
for those changed keys, application-scale performance, signed-release savings
or cross-machine Swift compatibility.

## Extend the existing OSS integration

Swift already loads Apple's bundled `libToolchainCASPlugin.dylib` in this
fixture. Creating a new Swift plugin is not an established prerequisite. The
current upstream Swift Build code selects plugin/socket options for both
compiler families; Swift compilation caching also requires its integrated
driver, explicit modules and a supporting toolchain.
[CAS options](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBCore/Settings/CASOptions.swift#L185),
[Swift prerequisites](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBCore/SpecImplementations/Tools/SwiftCompiler.swift#L984).

An October 7 compiler-owner proposal describes moving the canonical plugin and
gRPC definitions into `swiftlang/toolchain-cas-plugin`. It separates the
toolchain plugin, local daemon and remote storage service. That is the upstream
integration to follow. The proposed repository returned 404 during this audit;
no released plugin or new license was verified. The current vendored protocol
remains pinned under Apache-2.0 with LLVM exception.
[Upstream proposal](https://forums.swift.org/t/compilation-caching-integration-with-toolchain-cas-plugin/89937).

The next Swift gate is a source-stable producer and empty consumer with
language-specific replay evidence, changed-input misses, corruption fallback,
bounded storage and complete-job timing. Compiler hits do not cover linking,
packaging, signing or fresh simulator assertions.
