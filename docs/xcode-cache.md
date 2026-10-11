# Xcode compilation cache adapter

`layercache xcode run` serves LLVM's CAS and compilation action-index protocols on a private Unix socket for one Xcode build. It uses the existing verified artifact store with a project/toolchain namespace and bounded quota. Xcode's Clang, Objective-C, module and PCH requests can reuse objects from a previous build. Swift's native local CAS should be persisted alongside this store; this adapter does not provide a Swift CAS plugin dynamic library.

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

Build the Darwin arm64 binary on Linux with `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/layercache`. The phone-app integration pins the reviewed source commit, cross-compiles on Linux and transfers only that binary to the Mac job: [phone-app #218](https://github.com/ziahamza/phone-app/pull/218).

## Qualification

[Hosted replay](https://github.com/ziahamza/layercache/actions/runs/38152661755/job/114510218849) built a mixed Swift/Objective-C command-line tool twice using fresh DerivedData. The native local CAS was deleted between invocations; only the closed Layer Cache store survived. The consumer recorded 69 of 71 action lookups as hits and replayed 70 CAS objects. This proves LLVM protocol compatibility on Xcode 26.6; it is not an app release speed benchmark.

The dedicated workflow cross-compiles on Linux and repeats this replay with real Xcode. Its Linux runner defaults to GitHub Ubuntu until the repository has Ubicloud installed and `LAYERCACHE_UBICLOUD_ENABLED=true`; reruns use Ubuntu for recovery. Go protocol tests additionally exercise immutable action collisions, project/toolchain misses, file/inline identity, a fresh store consumer and compiler failure/uncached retry behavior.

The vendored protocol and license are documented in [the protocol README](../internal/xcodecache/proto/README.md).

The landing review reproduced startup storage failure, masked compiler exits from optional statistics, shared socket parents, two owners of one data directory, an evicted indirect output behind a surviving action index, and the encoded-message boundary against the original implementation. Each regression fails before its fix and passes afterward. The revised implementation passes `go test ./...`, focused race tests, vet, Darwin arm64 cross-compilation and `pnpm check` (49 passed, one installed-server-dependent skip). Fresh-runner application transport qualification remains a consumer launch gate.
