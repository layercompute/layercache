# Native compiler cache qualification

`probe.sh` builds a small Objective-C and Swift program with real Xcode. Each
consumer starts with fresh DerivedData and native CAS. The closed Layer Cache
store is the only retained compiler cache. The cold and warm phases require
actual compiler action and CAS hits, successful builds, and the executable's
expected output.

An optional third argument enables the pressure qualification:

```sh
bash qa/xcode/probe.sh /path/to/layercache /private/evidence /path/to/xcode-pressure-proxy
```

The QA proxy forwards all compiler RPCs to the private adapter socket. It holds
the first eligible successful action response containing CAS roots, writes unique
unrelated CAS blobs totaling more than the configured artifact quota, then
returns that response. Before and after pressure, it uses read-only SQLite and
blob inspection to verify the complete promised graph, exact sizes and content
hashes. This includes native Xcode's inline `value` node and encoded external
nodes, LLVM's `CASID` root and Clang's output-name-to-ID map. The helper sends no
CAS read RPCs, so inspection cannot inflate compiler replay counters.

The Mac probe passes `--min-native-roots 2` and requires at least two distinct
external roots in the selected native action. Earlier single-root action hits
continue normally without consuming the pressure attempt. Duplicate descriptors
for one CAS ID do not meet that threshold. If the compiler produces no eligible
action, the run fails with no pressure evidence. The helper defaults to one root
for its synthetic protocol tests, including a single-root LLVM transitive graph.

Qualification requires the compiler to read at least one promised CAS object
after pressure, with no failed promised reads. A native inline root's external
outputs can be consumed using `Load`; a Get of an external manifest is optional.
Every consumed promised read must fall within the fixed two-minute window.
Optional outputs may remain unfetched, but every promised object must still
exist with the exact verified bytes after eviction. JSON distinguishes
`availablePromisedObjects` from `fetchedPromisedObjects` and reports unfetched
IDs. Repeated root reads cannot hide an evicted descendant. Get adjacency is
retained even when a child response precedes its parent. Observation and closure
are bounded to 100,000 objects and 262,144 references, individual blobs to the
protocol message limit and inspected bytes to the artifact quota.

The quota is cold usage plus the largest cold CAS object and bounded slack.
Read-only SQL observations must show eviction and committed usage within that
quota. Evidence also records the query-to-first/last-promised-read intervals and pressure
duration. This controlled fixture must fetch within the fixed two-minute read
lease. It does not qualify Phone's larger graph or detached-query timing.

`go test -race ./qa/xcode/pressure-proxy` exercises the same interleaving through
real gRPC with synthetic LLVM and native inline graphs on Linux. The native
pressure regression fails against the adapter which recognizes only `CASID`.
An independently pinned root with an unleased, unconsumed leaf proves quota
eviction of that leaf fails the second inspection. A healthy graph can pass
with an unused first output and a real read from another promised object.
Only the macOS workflow can qualify the actual Apple compiler.

## Standalone Xcode 27 snapshot gate

`standalone-snapshot-probe.py` qualifies direct `xcodebuild` use with the
standalone lifecycle and the existing native archive client. The normal
`Xcode compiler cache` workflow adds one bounded `xcode-27` job alongside the
unchanged Xcode 26 pressure fixture. The selected release is checked against
actual Xcode build `27A266a`, macOS build `26A434` and iPhoneSimulator SDK build
`24A430`. The job records compiler version and executable SHA256, host and target
facts, minimum iOS 16.4 and Release `-O2`. A runner label alone cannot pass it.
The [published runner image](https://github.com/actions/runner-images/blob/main/images/macos/xcode-27-arm64-Readme.md)
was checked on 2026-10-20; image changes require reviewing the expected profile.

```sh
python3 qa/xcode/standalone-snapshot-probe.py \
  /absolute/path/to/layercache /absolute/path/to/native/dist/cli.js \
  /new/private/evidence-directory
```

The small Objective-C simulator library uses a precompiled header. It needs no
simulator boot, Apple signing or Swift code. `xcodebuild` is invoked directly
with the plugin settings and the ready standalone socket. Only after stop
acknowledges that exact instance's closed store, removes its socket and the
process exits can the scoped writer archive it. The fixture publishes one
immutable generation through a real isolated Team server, using generated,
short-lived fixture grants. It moves the closed producer to a private diagnostic
path, restores through a fresh reader archive cache into a new destination,
then starts another adapter
and builds with fresh DerivedData and native CAS.

Passing requires Clang compilation and PCH hit diagnostics, adapter action/CAS
hits and verified graph reads. An actual relevant source edit must produce a
Clang miss and a newly compiled arm64 function returning 43 instead of 42.
Object markers and disassembly verify the outputs; generic protocol counters
alone cannot pass. The read-only consumer must issue zero archive PUTs. Total
save and restore command times include authentication, packing or verification,
transfer and extraction. The proxy hashes streamed archive bytes in memory and
reports only complete-body, digest-header and published-PUT match booleans.
Failures expose only the native CLI's fixed allowlisted error classification.
The separate request timings are not a network
bandwidth measurement or a whole-application speedup.

Server configuration, grants, credentials, source and caches stay under the
private `.private` directory. The workflow uploads an explicit allowlist of
compiler logs, sanitized closure/statistics/transfer results and toolchain
facts. It never uploads the private subtree or command transcripts containing
credentials. Failure preserves the available compiler evidence and fails the
job. Linux evidence-parser controls run in the existing build job and establish
no compiler acceptance themselves.

This gate does not qualify external Swift replay, signed device builds or the
Apple-owned Xcode Cloud lifecycle. Application adoption still needs its exact
compiler fingerprint, supported workflow settings, scoped trusted publication
and fresh producer/consumer acceptance. Apple's own compilation cache stays
separate and enabled under its owning workflow policy.

On failure only, the optional `--shape-helper` executable inspects that preserved
closed producer store, before any consumer writes could affect its data. No
adapter reopens the diagnostic store. The helper opens SQLite in read-only
immutable mode, selects only the fixture's exact project/profile and uses the
production native-node and action-reference parsers. It reports acceptance,
reference counts, blob sizes, descriptor flag classes and at most 16 field
number/wire-type/length entries per node. Numeric row ordinals link accepted
references to declared native nodes, keeping ordinary raw output rejection
separate. Nested chunk shapes include only field/wire/length and a
`isSingleByteOne` boolean. Truncated lists are explicit. It reads at most 256 matching rows and
64 MiB, and emits at most 256 KiB. Raw SDK bytes, paths, cache IDs and digests
never enter this shape report. The failed compiler verdict stays red. These
shapes help diagnose format coverage; the older aggregate-hit fixture does not
establish that every compiler or PCH format previously replayed successfully.


The QA-only `plugin-semantics.py` probe runs immediately after confirmed producer
stop and before the first consumer deletes the producer's native CAS. A private
0600 manifest binds numeric fixture ordinals to the exact declared native
digests and bounded wire-reference counts. It remains under `.private` and is
excluded from uploads. `native-cas-locations.json` reports only fixed candidate
existence/ownership flags and whether the producer log selected the existing
plugin store. Missing or ambiguous locations remain unavailable; the probe does
not create an empty store and call that a restored object.

Go inspection of the closed LayerCache store stays immutable and read-only.
Apple's `llcas_cas_create` may update metadata in this fixture's separate producer
native CAS. No SDK files or native CAS directories are copied or published as
QA artifacts. The consumer still removes that native CAS and starts fresh.
`plugin-semantics.json` contains only fixed reason labels, ordinals, counts and
comparison booleans. An available API does not establish that every object was
found; each object has its own availability result. Missing symbols, an unknown
ABI, missing objects or a timeout establish no semantic success. The parent
requires `workerStopped:true` before any consumer cleanup or build. An
unconfirmed native worker preserves its producer CAS and fails qualification.
The production
wire parser and strict Clang/PCH replay requirements remain unchanged.

The independently expressed ABI declarations follow LLVM's experimental
[plugin types](https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/llvm/include/llvm-c/CAS/PluginAPI_types.h)
and [functions](https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/llvm/include/llvm-c/CAS/PluginAPI_functions.h),
licensed under [Apache 2.0 with LLVM exceptions](https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/LICENSE.TXT).
[Tuist's upstream loader](https://github.com/tuist/tuist/blob/a4c4bfec2eede5d2ac343aa1134460111eefce56/cas-plugin/src/upstream.rs)
provides an OSS example of using that interface under its
[MIT license](https://github.com/tuist/tuist/blob/a4c4bfec2eede5d2ac343aa1134460111eefce56/LICENSE).
The probe reads semantic sizes and reference counts rather than guessing an
encoding from opaque SDK bytes. Its fake-C ABI, privacy and timeout controls run
in the existing Linux build job; only the Mac job can qualify Apple's library.

A further bounded semantic comparison can load each declared native object's
children and privately compare its data size and SHA256 with the corresponding
inline literal chunk. The private manifest contains those hashes in wire order;
public output contains only counts and match/leaf booleans. A missing child,
non-leaf child, count mismatch or incomplete manifest is explicit. The comparison
is limited to 256 children per target, 1,024 overall, 64 KiB per child and a shared
64 MiB byte budget. Canonical one-based index-sequence predicates are separate
from the parser verdict. Equal vector byte lengths alone do not establish an
index format or semantic mapping.

The older `wireReferenceCountMatches` compares outer wire references with the
compiler API's semantic child count. A difference can reflect inline children;
it does not establish that a remote object is missing. Inspect the separate
complete inline mapping and leaf results before interpreting graph coverage.
These fixture observations do not widen the production parser by themselves.
