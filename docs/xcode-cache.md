# Xcode compilation cache adapter

`layercache xcode run` integrates with Xcode 26's native compilation cache. It enables Apple's bundled CAS plugin and serves LLVM's CAS and compilation action-index protocols on a private Unix socket for one Xcode build. The verified artifact store supplies project/toolchain namespaces and a bounded quota. Clang, Objective-C, module and PCH replay is qualified. External Swift compilation replay remains unqualified; the mixed-language fixture still compiles its Swift source on the warm consumer. Persisting Swift's local CAS is a separate reuse path and does not prove that external replay works. See the [support audit](research/xcode26-compilation-cache-20261019.md) for exact coverage and evidence.

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

A miss or corrupt object triggers compilation. A cache directory or metadata startup failure runs the compiler once with caching disabled and reports `uncachedFallback`. Insecure socket directories, invalid configuration and an already-owned data directory are rejected. Once compilation starts, its result is preserved even if a cache backend error or statistics write also fails; a cache error alone cannot establish the cause of a compiler failure. Statistics and close warnings do not replace the compiler result. `uncachedRetry` remains false for compatibility with existing receipts. `cacheErrors` and real CAS/action request and hit counts report cache behavior. An action with a missing or corrupt indirect CAS output is returned as a miss; size checks include serialized object and response envelopes before accepting an object. The adapter opens no TCP listener and requires no account or external service deployment. It currently uses a local store; CI snapshot transport supplies reuse between runners.

Before returning a cached action, this revision verifies its complete transitive
CAS graph and atomically leases the exact objects for a two-minute fetch window.
It recognizes LLVM's `CASID` root, Clang's output-name-to-ID map and the native
Xcode plugin's inline `value` node. Native nodes include inline data and external
references; encoded external nodes are followed transitively. The native parser
accepts the bounded message shapes recorded from the Xcode 26.6 fixture and
returns a miss for unsupported fields, malformed descriptors and unknown action
schemas. The alternate data-chunk branch accepts only its recorded single-byte
sentinel; new native formats need qualification before they can become hits.
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

`graphChecks`, `graphObjectsChecked` and `graphLookupNanos` measure complete graph
verification and lease admission. Divide the nanosecond total by `graphChecks` to
inspect average lookup cost. `graphLeaseMisses` records admission failures caused
by graph or lease bounds. CAS RPC hit counts remain separate.

Build the Darwin arm64 binary on Linux with `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/layercache`. The phone-app integration pins the reviewed source commit, cross-compiles on Linux and transfers only that binary to the Mac job: [phone-app #218](https://github.com/ziahamza/phone-app/pull/218).

## Qualification

[Hosted replay and pressure qualification](https://github.com/ziahamza/layercache/actions/runs/39072238914/job/117512286300) passed on 2026-10-17 at source `90b07ca0e83117a638604bb7a7944ad89483ff3e`, using Xcode 26.6 build 17F113. The mixed Swift/Objective-C command-line tool builds with fresh DerivedData. The native local CAS is deleted between invocations; only the closed Layer Cache store survives. The warm consumer recorded 69 of 71 action lookups as hits and replayed 70 CAS objects. Its two Swift source compilation tasks still reported replay misses. This proves LLVM protocol compatibility; it is not complete Swift replay or an app release speed benchmark.

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
