# xcslot and LayerCache compatibility, November 25, 2026

xcslot makes local Xcode builds incremental across worktrees by mirroring them
into a stable checkout and keeping that slot's DerivedData. LayerCache serves
compiler objects when Xcode needs to run its build graph again. A serial local
fixture exercised both paths on the same Mac. [xcslot implementation](https://github.com/ertembiyik/xcslot/blob/f4969f69e6f40859b6a9bff29b758999d0e2edd5/docs/how-it-works.md) · [LayerCache adapter contract](https://github.com/layercompute/layercache/blob/1df80bdbe4a54644c53c1d527d04ca8631bff385/docs/xcode-cache.md)

## Local fixture

The fixture used a Release arm64 macOS CLI with two Swift files and a C static
library, generated with XcodeGen 2.47.0. The host ran macOS 27.2.1 (26B131),
Xcode 27.2 (27B52) and macOS SDK build 26B118. xcslot 0.1.5 ran from source
under Bun 1.4.0 with one slot and one permit. The LayerCache arm64 beta.13
archive matched the release asset SHA-256
`7c9415848993d8eb338573f629649469a8f78a1fc29be3b6d83ef14373a7b577`.
The temporary store had a 128 MiB artifact budget and an explicit 5 GiB
free-space reserve. Each build used at most two Xcode jobs.
[xcslot source](https://github.com/ertembiyik/xcslot/tree/f4969f69e6f40859b6a9bff29b758999d0e2edd5) · [LayerCache beta.13](https://github.com/layercompute/layercache/releases/tag/v0.1.0-beta.13)

Two linked worktrees started at the same commit. Direct `xcodebuild` used a
disposable DerivedData directory. The combined runs used
`layercache xcode run --data-dir … --socket … --project … --compatibility … --stats-file … -- xcslot xcodebuild …`,
with the adapter files outside the slot. Timings are single monotonic wall-clock
observations of the command, including any wrapper setup, sync and closure.

| Run | Time | SwiftCompile / CompileC log entries | LayerCache hits / lookups |
| --- | ---: | ---: | --- |
| Direct, empty DerivedData | 4.14 s | 2 / 1 | Adapter absent |
| First slot build, empty adapter store | 2.79 s | 2 / 1 | 0/4 action, 5 action writes |
| Second worktree, intact slot | 1.26 s | 0 / 0 | Zero action or CAS lookups |
| Same slot path, fresh DerivedData | 2.99 s | 2 / 1 | 5/5 action and 12/12 CAS |
| Changed Swift input in second worktree | 1.64 s | 2 / 0 | One action lookup missed |

The cold build's Xcode CAS path was inside the slot's DerivedData. The replay
followed removal of that entire directory and kept the same slot checkout and
closed LayerCache store. Xcode logged Swift and C replay hits and produced the
same executable SHA-256 as the combined cold build. The changed source printed
`43` instead of `42`. An invalid Swift input subsequently returned exit code 65
with a worktree diagnostic, zero uncached retries and a removed adapter socket.
Two `xcslot run` shell invocations both executed and advanced a slot-local
ignored counter from `1` to `2`. The focused upstream xcslot tests passed
(23).

These samples establish compatibility and invalidation for this fixture. App
speedup, parallel adapter use, snapshot transfer, iOS device execution and
cross-Mac replay remain unmeasured.

## Adoption boundary

- **Local agent worktrees.** Start with one slot for a frequently built project.
  Each slot has its own absolute path and full DerivedData. The adapter store
  allows one active owner, so the tested wrapper is for serial invocations with
  that store. Compare full elapsed time and peak allocated disk against direct
  Xcode builds, including waits, edits, branch switches and product handoff.
  Use a private slot root and keep credentials external to the mirror. [xcslot configuration](https://github.com/ertembiyik/xcslot/blob/f4969f69e6f40859b6a9bff29b758999d0e2edd5/docs/configuration.md)
- **CI workers.** If a worker already keeps a stable checkout and persistent
  DerivedData, a second slot creates another checkout and DerivedData tree.
  Measure setup, snapshot restore, build, packing and upload through the
  existing worker before changing that policy.
  [LayerCache snapshot contract](../xcode-cache.md)
- **Terminal work.** Use native package caches and declared deterministic task
  outputs for reusable work. `xcslot run` persists a working directory and can
  write files back into the caller's worktree. Execute side-effectful commands
  and current device assertions fresh. Retain the exact app and test products
  for required build-for-testing handoffs.
  [LayerCache task inputs](../ci-optimization/task-reuse.md) · [Retained artifacts](../retained-artifacts.md)
