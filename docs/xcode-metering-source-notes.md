# Xcode metering source notes

Checked 2026-10-20. These sources explain the units behind native compiler
reporting. An upstream source revision does not identify the exact source of a
shipped Apple binary. The selected Xcode build and observed log format still
need qualification. The existing [native fixture](../qa/xcode/README.md)
qualifies Clang and PCH replay separately from adapter protocol hits; external
Swift replay remains unqualified.

## Terminal replay diagnostics

The Apple-origin Swift Build implementation at
[`5987c0274baf641def06752bd4c1e9c2cce04cbd`](https://github.com/swiftlang/swift-build/tree/5987c0274baf641def06752bd4c1e9c2cce04cbd)
has these distinct outputs:

| Task output | Interpretation |
| --- | --- |
| `note: replayed cache hit: KEY` | Clang outputs were found, materialized and replayed. |
| `note: cache miss: KEY` | Clang replay could not find the compilation or all required materialized outputs. |
| `note: replay cache hit` | A Swift driver job replayed all its cached compilations. |
| `note: replay cache miss` | A Swift driver job could not replay all its cached compilations. |

Clang increments its compiler and task counters after these outcomes. It also
prints a separate bare result line, so counting both representations doubles
the result. Swift does the same. One Swift job can query multiple keys before
reporting one outcome. A job count therefore does not establish the number of
source files or unique compilation keys.
[Clang replay](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/TaskActions/ClangCompileTaskAction.swift#L370-L421),
[Swift replay](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/TaskActions/SwiftDriverJobTaskAction.swift#L606-L688).

Intermediate lookup diagnostics cannot establish compiler replay. Detached
Clang queries emit a bare note with a lookup result. Swift queries emit their
own query result, and the replay implementation may report a local lookup
failure before its terminal job result. Neither belongs in a compiler hit-rate
denominator alongside the terminal result.
[Clang queries](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/TaskActions/ClangCachingKeyQueryTaskAction.swift#L61-L78),
[Swift queries](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/TaskActions/SwiftCachingKeyQueryTaskAction.swift#L56-L65),
[Swift local lookup](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/TaskActions/SwiftDriverJobTaskAction.swift#L628-L677).

Task headers alone do not measure hits or misses. Swift Build creates real
dynamic driver jobs and separate per-file virtual batch subtasks that can both
use `SwiftCompile` rule names. Planning, module emission and compilation also
have separate task rules. Treating every visible header as one cacheable job
would manufacture a denominator.
[Dynamic job rules](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/DynamicTaskSpecs/SwiftDriverJobDynamicTaskSpec.swift#L182-L199),
[virtual batch subtasks](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBCore/SpecImplementations/Tools/SwiftCompiler.swift#L3747-L3763).

`PrecompileModule` dispatches to an action using the same Clang replay helper.
[Rule and action selection](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/DynamicTaskSpecs/PrecompileClangModuleDynamicTaskSpec.swift#L133-L169),
[replay invocation](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/TaskActions/PrecompileClangModuleTaskAction.swift#L195-L205).
The Swift driver also creates `SwiftGeneratePch`,
`SwiftExplicitDependencyGeneratePcm` and
`SwiftExplicitDependencyCompileModuleFromInterface` rules that use the existing
Swift driver replay action. The `swift` reporting scope denotes that driver,
including Clang module jobs, rather than exclusively Swift-language source.
[Job categorization and PCH classification](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBCore/LibSwiftDriver/PlannedBuild.swift#L639-L679),
[rule construction](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/DynamicTaskSpecs/SwiftDriverJobDynamicTaskSpec.swift#L182-L205),
[shared action dispatch](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBTaskExecution/DynamicTaskSpecs/SwiftDriverJobDynamicTaskSpec.swift#L249-L261),
[Clang module labels](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBCore/LibSwiftDriver/LibSwiftDriver.swift#L319-L330).

## Separate compiler formats and skipped diagnostics

The LLVM fork at `33957bbc59f4eed39bfee4627ef05e47ef31d801` defines a different
Clang compiler diagnostic family beginning `compile job cache`, with hit, miss
and skipped outcomes. It also defines timing remarks for dependency scanning
and cache backend operations. These are a separate format from Swift Build's
task replay notes.
[Diagnostic definitions](https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/clang/include/clang/Basic/DiagnosticCASKinds.td#L49-L78).

A skipped diagnostic can mean intentionally disabled replay, or a decision
after compilation to withhold nonreproducible or unsupported outputs. A lookup
miss and a later skipped diagnostic can describe the same job. Skipped
observations are therefore not another mutually exclusive miss category.
[Disabled replay](https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/clang/lib/Frontend/CompileJobCache.cpp#L411-L440),
[publication skipped after compilation](https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/clang/lib/Frontend/CompileJobCache.cpp#L536-L555).

The inspected Swift Build terminal replay implementations define no skipped
counter or skipped terminal note. Reports using only that grammar leave the
skipped count unknown. Absence of a recognized diagnostic does not mean zero
uncacheable jobs. Swift frontend per-input lookup remarks are another format;
their success can precede output replay failure, so they cannot substitute for
the terminal driver result.
[Swift frontend diagnostics](https://github.com/swiftlang/swift/blob/f2f10454946c63da4164e182ea7f2a9895e430ce/include/swift/AST/DiagnosticsFrontend.def#L529-L542),
[lookup before replay](https://github.com/swiftlang/swift/blob/f2f10454946c63da4164e182ea7f2a9895e430ce/lib/Frontend/CachingUtils.cpp#L352-L416).

## Timing and adapter requests

Swift Build's structured metrics distinguish process CPU time in microseconds
through `utime` and `stime`, process wall duration through `wcDuration`, and
process start time through `wcStartTime`. Its compiler counters aggregate
across a build operation, with separate counters grouped by task rule. These
are useful future reporting inputs, but the first report client accepts only
explicit caller-supplied command timings.
[Process metric units](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBProtocol/BuildOperationMessages.swift#L691-L705),
[counter definitions](https://github.com/swiftlang/swift-build/blob/5987c0274baf641def06752bd4c1e9c2cce04cbd/Sources/SWBProtocol/BuildOperationMessages.swift#L493-L511).

Apple recommends `-showBuildTimingSummary` to find expensive tasks. Tasks can
run in parallel, so aggregate task time is not complete build elapsed time.
Parallel task counts also do not directly measure CPU or memory utilization.
Cache lookup and materialization timings do not measure the compilation that
a hit avoided. Hit rate alone cannot supply CPU-hours, critical-path time saved
or provider charges.
[Apple timing guidance](https://developer.apple.com/documentation/xcode/improving-the-speed-of-incremental-builds),
[Apple build parallelization](https://developer.apple.com/videos/play/wwdc2022/110364/).

LayerCache's adapter counters describe CAS read requests, action-index queries,
writes and validation stages. One compiler task can issue several requests or
query the same key more than once. A successful action query promises a
verified graph; it does not prove the compiler consumed it. Graph inspection
does not inflate the CAS read counters. Keep these protocol counts separate
from native compiler outcomes.
[Counter contract](../internal/xcodecache/stats.go),
[action lookup](../internal/xcodecache/service.go),
[inspection regression control](../internal/xcodecache/service_test.go).

## Unsupported input policy

Report native counts only for a supported, bounded log format and scope.
Missing diagnostics, truncated input, conflicting results, unrecognized task
association and unsupported formats retain explicit coverage and reason
labels. Unknown values remain null. Do not reinterpret adapter RPC hits,
archive restores, query-only notes, task headers or build success as compiler
hits. Do not copy source paths, compilation keys or raw logs into telemetry.
Parser controls qualify interpretation only; they do not qualify a compiler,
application build, remote cache restore or savings claim.
