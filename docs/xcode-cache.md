# Xcode compilation cache adapter


[Native metering](native-metering.md) explains supported compiler-log export,
separate protocol counters, private report import and remaining timing/resource
coverage. Installed client and application adoption gates remain separate.
`layercache xcode run` integrates with Xcode's native compilation cache. It
enables Apple's bundled CAS plugin and serves LLVM's CAS and compilation
action-index protocols on a private Unix socket for one build.
`layercache xcode serve` exposes the same adapter when the workflow owns
`xcodebuild`.
The verified artifact store supplies project/toolchain namespaces and a bounded
quota. Clang, Objective-C and PCH replay is qualified within the fixtures below.
External Swift compilation replay remains unqualified; the mixed-language
fixture still compiles its Swift source on the warm consumer. Persisting Swift's
local CAS is a separate reuse path and does not prove external replay. See the
[support audit](research/xcode26-compilation-cache-20261019.md) for the earlier
toolchain's coverage and evidence.

```sh
layercache xcode run \
  --data-dir "$HOME/Library/Caches/project-xcode" \
  --socket /tmp/project-xcode/cache.sock \
  --project github.com/owner/project \
  --compatibility exact-xcode-sdk-compiler-identity \
  --stats-file /tmp/project-xcode/stats.json \
  -- xcodebuild -workspace App.xcworkspace -scheme App \
     -configuration Release build
```

Create the socket parent directory privately and use a unique socket for each invocation. The wrapper enables Xcode compilation caching, its plugin integration, remote service path, detached key queries and diagnostic remarks. It acquires the runtime data-directory lock before opening the store, stops RPC handlers and closes SQLite before releasing ownership, including after compilation fails. A second owner with a different socket is rejected without running store reconciliation. Existing socket parent directories must belong to the current user and have mode 0700. Persist the closed `--data-dir` with the CI cache mechanism. Keep signed products, provisioning profiles, keychains and DerivedData out of that snapshot. Include the exact Xcode, compiler and SDK identity in both the compatibility argument and snapshot key.

## Standalone lifecycle for managed builds

When the build provider owns `xcodebuild`, start the adapter independently:

```sh
layercache xcode serve \
  --data-dir "$CACHE_STORE" --socket "$CONTROL_ROOT/cas.sock" \
  --ready-file "$CONTROL_ROOT/ready.json" \
  --project "$CACHE_PROJECT" --compatibility "$EXACT_TOOLCHAIN_PROFILE" \
  --max-size 5368709120 --stats-file "$CONTROL_ROOT/stats.json"
```

This is a foreground process. A provider hook can background it and retain its
process handle, but must wait for successful readiness before enabling the
compiler's remote plugin. The readiness file and control socket belong outside
the cache directory being snapshotted. Their parent is a private directory owned
by the current user. Use a distinct socket and readiness file for each active
adapter. The adapter publishes readiness only after ownership, recovery and RPC
startup succeed.

```sh
layercache xcode status --ready-file "$CONTROL_ROOT/ready.json"
# The provider runs its own build and waits for its compiler work to finish.
layercache xcode stop --ready-file "$CONTROL_ROOT/ready.json"
```

Control requests use the instance identity in the protected readiness file.
They never signal a PID. A successful live stop confirms RPC handlers stopped,
SQLite closed and store ownership released. A missing process, unreachable
socket or timed-out shutdown does not establish a safe snapshot boundary.
Status can return a retained stopped receipt with `historical:true`; that
receipt records an earlier instance and cannot acknowledge a new stop. Keep
statistics outside the store and separate from readiness and socket files.
Keep an unconfirmed directory out of publication. Serialize the snapshot with
any later owner of that directory. Cancellation and a later
restart retain the exclusive store ownership and existing recovery checks.

The standalone process does not inject settings into Apple's build. The owning
workflow must set `COMPILATION_CACHE_ENABLE_CACHING=YES`,
`COMPILATION_CACHE_ENABLE_PLUGIN=YES`,
`COMPILATION_CACHE_REMOTE_SERVICE_PATH` to the ready adapter's socket,
`COMPILATION_CACHE_ENABLE_DETACHED_KEY_QUERIES=YES` and
`COMPILATION_CACHE_ENABLE_DIAGNOSTIC_REMARKS=YES` through its supported build
configuration. Enabling settings or receiving readiness does not establish a
compiler hit. Preserve the provider's own build/test lifecycle and fresh runtime
assertions.

## Scoped private snapshot transfer

Reuse the [native archive CLI](../native/README.md#local-native-artifacts-and-fresh-worktrees)
with Node 24 or later. The credential file holds one operator-provisioned shared
bootstrap, not an Apple or GitHub identity. Its descriptor, provisioning and
revocation are documented in [snapshot authentication](snapshot-auth.md).
Keep the bootstrap file private and outside the snapshot.

```sh
node "$LAYER_CACHE_CHECKOUT/native/dist/cli.js" restore \
  --endpoint "$CACHE_ORIGIN" --credential-file "$READER_CREDENTIAL_FILE" \
  --project "$CACHE_PROJECT" --compatibility "$EXACT_TOOLCHAIN_PROFILE" \
  --key "$SNAPSHOT_GENERATION" --path "$NEW_RESTORE_PARENT" \
  --cache-dir "$TRANSFER_CACHE" --max-bytes 5368709120

# After a successful, confirmed stop, only the trusted publisher may save.
node "$LAYER_CACHE_CHECKOUT/native/dist/cli.js" save \
  --endpoint "$CACHE_ORIGIN" --credential-file "$WRITER_CREDENTIAL_FILE" \
  --project "$CACHE_PROJECT" --compatibility "$EXACT_TOOLCHAIN_PROFILE" \
  --key "$NEW_SNAPSHOT_GENERATION" --path "$CLOSED_CACHE_STORE" \
  --cache-dir "$TRANSFER_CACHE" --max-bytes 5368709120
```

Restore needs a new destination and preserves the saved top-level directory
inside it. Point the next adapter at that restored store. A miss returns
`hit:false`; ordinary compilation remains the fallback. Changed snapshots need
distinct immutable generation keys within the grant's namespace. Consumers name
a published generation explicitly; this interface does not discover or rotate
a latest generation.

Scoped commands exchange their bootstrap before consulting their local archive
cache. The backend issues a short-lived exact-artifact capability and rechecks
the current grant at each scoped archive request. A scoped save rejects a
conflicting winner or a degraded/local-queue acknowledgment. These are optional
cache snapshots, with the existing cache eviction policy. Their acknowledgments
do not grant retained-artifact TTL or future availability.

The archive client still needs local staging and extraction space. The Go store
budget and archive budget are separate; neither limits caller-owned restored
directories or all physical disk allocation. Preserve a filesystem reserve,
measure packing/transfer/unpacking and complete job time, and avoid duplicating
the entire DerivedData tree. This transport does not snapshot, checkpoint or
qualify Apple's own built-in CAS directory while another process owns it.

The example budgets are local limits, not a hosted storage entitlement. Choose
them against the backend's artifact limit, hard quota and garbage-collection
target. An unpinned snapshot larger than that target can be accepted and later
evicted during maintenance. Measure the closed store, expanded archive and
transferred bytes separately, and leave room for metadata and other generations.
An evicted generation must remain an ordinary cache miss with compilation as
fallback.

## Compiler behavior and native qualification

A miss or corrupt object triggers compilation. A cache directory or metadata startup failure runs the compiler once with caching disabled and reports `uncachedFallback`. Insecure socket directories, invalid configuration and an already-owned data directory are rejected. Once compilation starts, its result is preserved even if a cache backend error or statistics write also fails; a cache error alone cannot establish the cause of a compiler failure. Statistics and close warnings do not replace the compiler result. `uncachedRetry` remains false for compatibility with existing receipts. `cacheErrors` and real CAS/action request and hit counts report cache behavior. An action with a missing or corrupt indirect CAS output is returned as a miss; size checks include serialized object and response envelopes before accepting an object. The adapter opens no TCP listener and requires no account or external service deployment. It currently uses a local store; CI snapshot transport supplies reuse between runners.

Before returning a cached action, this revision verifies its complete transitive
CAS graph and atomically leases the exact objects for a two-minute fetch window.
It recognizes LLVM's `CASID` root, Clang's output-name-to-ID map and the native
Xcode plugin's inline `value` node. Native nodes include inline data and external
references; encoded external nodes are followed transitively. The existing
reference grammar for nodes without a marker is unchanged. A separate marker
form requires a leading data chunk with nested field 2 containing canonical
varints that form a permutation of `1..N`, followed by exactly `N` literal data
chunks with nested field 1, where `N` is 1 through 256. These literals contain
terminal native leaves. This form permits no other chunks, reference unions or
CAS envelope references. Duplicate, zero-based, out-of-range or noncanonical
indices, missing or extra literals, unknown fields and malformed descriptors
return an action miss. Other native formats still require qualification.
Eviction during verification produces an action miss. During the window, Local
Cache deletion, LRU and impact collection preserve all leased objects, including
descendants shared with another action. The committed byte quota remains in force;
a new object which cannot fit alongside leased objects is rejected. Its compiler
result remains subject to the existing cache-error policy above.

Ordinary `Load` and `Get` calls do not extend the window. A new successful action
query renews the same immutable graph's window. Abandoned consumers become
evictable after expiry, and an exclusively owned adapter restart clears interrupted
read leases. Opening another store does not clear live leases or durable upload
pins. Verification is bounded to 100,000 objects, the committed byte quota and a
ten-second lookup budget. Across the store, at most 4,096 graph owners and 262,144
owner/object memberships are retained. A graph which cannot be admitted returns
an action miss. These changes are newer than the published beta.3 artifact.

The Xcode 27 qualification uses Apple's native CAS API to check every declared
native object before publication or consumer startup. It requires complete
ordinal coverage, zero root data, matching child counts and indexed content,
and terminal children. Public reports contain only bounded structure, counts
and comparison booleans; native digests and literal hashes stay in the private
manifest. The semantic probe permits at most 256 targets, 256 children per
target, 1,024 children overall, 64 KiB per child and a shared 64 MiB root/child
byte budget. Its manifest and report are each bounded to 128 KiB. Closed-store
shape inspection reads at most 256 rows and 64 MiB and emits at most 256 KiB.
Exceeding an inline comparison bound aborts manifest generation and fails
qualification closed. Optional root-only counts are deferred and cannot qualify
replay. See the [QA contract](../qa/xcode/README.md#standalone-xcode-27-snapshot-gate)
for the diagnostic boundary.

`graphChecks`, `graphObjectsChecked` and `graphLookupNanos` measure complete graph
verification and lease admission. Divide the nanosecond total by `graphChecks` to
inspect average lookup cost. `graphLeaseMisses` records admission failures caused
by graph or lease bounds. CAS RPC hit counts remain separate.

Build the Darwin arm64 binary on Linux with `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/layercache`. The phone-app integration pins the reviewed source commit, cross-compiles on Linux and transfers only that binary to the Mac job: [phone-app #218](https://github.com/ziahamza/phone-app/pull/218).

## Qualification

[Hosted replay and pressure qualification](https://github.com/ziahamza/layercache/actions/runs/39072238914/job/117512286300) passed on 2026-10-17 at source `90b07ca0e83117a638604bb7a7944ad89483ff3e`, using Xcode 26.6 build 17F113. The mixed Swift/Objective-C command-line tool builds with fresh DerivedData. The native local CAS is deleted between invocations; only the closed Layer Cache store survives. The warm consumer recorded 69 of 71 action lookups as hits and replayed 70 CAS objects. Its two Swift source compilation tasks still reported replay misses. This proves LLVM protocol compatibility; it is not complete Swift replay or an app release speed benchmark.

The Xcode 27 standalone gate passed at PR #76 source
`fc8b449659de2889dc88dc2ec7dbd5e5bdf6a4a9` in the
[candidate job](https://github.com/ziahamza/layercache/actions/runs/39467518317/job/118783279071)
and repeated after merge at `86d2383d7be6f2831851444faf4e11f3ee67a8d0` in the
[post-merge job](https://github.com/ziahamza/layercache/actions/runs/39468895209/job/118788090537).
Both used Xcode 27.0 build `27A266a`, iPhoneSimulator 27.0 SDK build `24A430`
and macOS 27.0.1 build `26A434`, compiling arm64 Release with `-O2`.
The small Objective-C simulator library used the same VM and source path, with
fresh adapter/compiler processes, native CAS and DerivedData for the consumer.
Its closed store crossed the existing authenticated HTTP archive transport to
a separate reader cache and restored store. Both jobs recorded 10/10 action
hits and 20/20 CAS hits, main-source and PCH hit diagnostics, and a changed-source
compilation miss producing 43 instead of 42, checked by object bytes and
disassembly. The native API also verified the complete indexed leaf mapping
before transfer. This qualifies Clang/Objective-C and PCH replay for that exact
fixture and toolchain. Phase timings are observations from this small fixture
and establish no application savings. Fresh-worker restoration, managed Xcode
Cloud acceptance, signed builds and external Swift replay remain unqualified.

Scoped reader and trusted-publisher grants are now active on the healthy
backend running the merged source. Thirteen live gateway controls passed.
An independent read-only audit checked SQL and R2 metadata and verified SHA256
across a complete 4,443-byte archive. Six live revocation controls also passed;
the original grant-file identity and bytes were restored without a service
restart or bootstrap rotation. These results qualify deployed authorization,
revocation and archive integrity within the configured scope. A managed Xcode
Cloud seed is not yet available; fresh-worker producer/consumer acceptance,
external Swift replay and application savings remain unqualified.

The dedicated workflow cross-compiles on Linux and repeats this replay with real Xcode. Its Linux runner defaults to GitHub Ubuntu until the repository has Ubicloud installed and `LAYERCACHE_UBICLOUD_ENABLED=true`; reruns use Ubuntu for recovery. Go protocol tests additionally exercise immutable action collisions, project/toolchain misses, file/inline identity, a fresh store consumer and compiler failure/uncached retry behavior.

The graph-retention regression uses real gRPC clients and concurrent CAS writes
under a 1,024-byte quota. It proves the promised root and leaf remain readable,
oversubscribed writes are rejected, and committed usage stays bounded. Store
tests cover shared descendants, expiry without read renewal, atomic admission,
interrupted queries, namespace caps, collector races and exclusive restart cleanup.
These protocol proofs alone do not qualify real Xcode behavior under storage
pressure. The hosted native pressure gate described below also passed in the
2026-10-17 qualification. Application-scale adoption remains separate.

The mandatory pressure phase uses a QA-only proxy in front of the candidate's
Unix socket. It holds a real compiler action hit, writes unrelated CAS objects
until quota eviction occurs, then forwards the response. Before and after those
writes, bounded read-only SQLite and blob inspection verifies every promised
object's metadata, bytes, hash and complete reachable graph. It sends no helper
CAS read RPCs, so the inspection does not inflate compiler hit counts.
The Mac fixture selects a native action with at least two distinct external
roots and requires both to survive; earlier single-root hits pass through
normally. A producer with only single-root actions cannot qualify this gate.

The compiler must actually read at least one promised object after pressure,
produce the correct executable, and report no failed promised reads, retry or
uncached fallback. Native inline roots can restore their outputs using `Load`.
Every consumed promised object must be read within the fixed two-minute window.
Optional outputs may remain unfetched; they must still pass the complete
post-pressure availability check. JSON reports `availablePromisedObjects`
separately from `fetchedPromisedObjects` and the unfetched IDs. Repeated root
reads cannot hide an evicted descendant because the second inspection verifies
that descendant directly. Actual Get adjacency is retained even when a child
response precedes its parent. JSON also records committed usage, evicted bytes,
pressure duration and query-to-first/last-promised-read time.

The native-format protocol regression fails against the revision which only
recognized `CASID`. A separate root-only retention fixture proves an evicted,
unconsumed native descendant fails the post-pressure inspection. The hosted
pressure consumer fetched both promised external objects after forced eviction,
with zero cache errors, fallback or retry and committed usage below quota.
Full application restore timing, including detached action queries, still
requires the application's own evidence.

The vendored protocol and license are documented in [the protocol README](../internal/xcodecache/proto/README.md).

The landing review reproduced startup storage failure, masked compiler exits from optional statistics, shared socket parents, two owners of one data directory, an evicted indirect output behind a surviving action index, and the encoded-message boundary against the original implementation. Each regression fails before its fix and passes afterward. The revised implementation passes `go test ./...`, focused race tests, vet, Darwin arm64 cross-compilation and `pnpm check` (49 passed, one installed-server-dependent skip). Fresh-runner application transport qualification remains a consumer launch gate.
