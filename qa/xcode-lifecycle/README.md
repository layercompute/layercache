# Closed compiler-store and scoped archive qualification

This opt-in fixture connects the actual Go executable, embedded Team server and
built native archive CLI. It runs on Linux or macOS without Xcode. The fixture
writes a small synthetic CAS graph and action through the existing gRPC protocol,
closes its producer, publishes the closed directory, restores it with a separate
read-only client and opens a new adapter against the restored directory. The new
adapter must return the original action, manifest and transitive object bytes.
The generic archive preserves the saved top-level directory. The restored
adapter opens that directory below the extraction parent, after checking its
existing `metadata.db`; it does not create an empty store at the parent path.

Build the candidate Go executable and native bundle through the repository's
normal build process first. The qualifier never rebuilds them or downloads tools.
It requires Node 24 or newer and absolute paths to both candidates:

```sh
LAYERCACHE_BIN=/absolute/path/to/layercache \
NATIVE_CACHE_CLI="$PWD/native/dist/cli.js" \
GOMAXPROCS=2 GOPROXY=off GOSUMDB=off \
go test ./qa/xcode-lifecycle \
  -run '^TestScopedClosedStoreRoundTrip$' -count=1 -timeout=4m -v
```

Without both candidate paths the ordinary Go suite skips this qualifier. A
configured run must pass, rather than skip. Windows is unsupported because this
adapter uses Unix domain sockets.

The fixture creates its own protected configuration, random bootstrap grants,
loopback Team service, short Unix socket paths and archive caches. It inherits no
cloud, Actions or native-cache credentials. It preserves the caller's existing
`HOME` unchanged because Go's macOS defaults require it; explicit configuration,
data and archive paths remain inside the fixture. Failure output includes only
fixed stderr classifications, never raw configuration or token output. Separate
reader and writer bootstrap files exchange authority through the real
`/v1/auth/snapshot/exchange` endpoint;
the qualifier does not mint capabilities itself or replace authentication with a
mock. An observing loopback proxy forwards authentication and archive requests to
that server. It changes response bytes only for the named integrity controls.

The checks cover:

- Authenticated readiness and an exact-instance stop acknowledgment with storage
  closed, ownership released and `historical: false` before archive publication.
- A separately launched producer reopening its closed graph, a Team service
  restart, a fresh archive cache and a new restored-store adapter.
- A confirmed Team publication and exact archive digest/size agreement. The
  normal read-only archive restore sends zero PUT requests. A separate negative
  control deliberately attempts a PUT with a read capability and must fail.
- Reader publication denied before upload; foreign project, profile and snapshot
  namespace rejected; a new generation inside the allowed namespace producing a
  clean miss; an exact-artifact capability refusing another artifact key.
- Corrupt and truncated HTTP archive bodies rejected before installing either
  output or a cached archive.
- Current grant revocation invalidating a previously exchanged capability and
  preventing another exchange, without restarting the server.
- Managed cancellation through an interrupt, with the final exact-instance
  historical receipt recording closed storage and released ownership.

Commands have a 20-second bound, captured output has a 1 MiB bound, and the fixture
has a three-minute overall context. Stores and archive caches have a 16 MiB
artifact budget. Cleanup stops only the child processes and temporary directories
created by this run. No live gateway, application repository, provider build or
hosted CI workflow is contacted.

Set `LAYER_CACHE_XCODE_LIFECYCLE_REPORT` to an absolute, **new** output file under
a private directory to retain a sanitized JSON receipt. The file is created with
mode `0600` and is never overwritten. It contains check names, candidate binary
and bundle SHA256 hashes, archive size and the qualification boundary. It contains
no bootstrap secrets, exchanged tokens, instance credentials or operational paths.

This is local source and protocol qualification. A bootstrap grant is shared
operator authority; it does not attest to Apple identity, branch or workload.
The fixture does not compile native code, establish Clang or Swift compiler hits,
qualify an Apple-managed build, deploy authorization, prove R2 persistence or
measure transfer performance or savings. Snapshot archives remain evictable cache
entries; required pipeline handoffs retain their separate retention contract.
Apple toolchain identity, real compiler replay and managed producer/consumer
acceptance still require the corresponding application owner's bounded builds.

The caller serializes the controlled snapshot: after the fresh live stop
acknowledgment and process exit, it starts no later owner until archive save
completes. A retained stopped receipt is historical evidence, not permission to
copy a currently quiescent store. The generic archive transport does not hold
the compiler-store ownership lock or establish input consistency against an
unrelated concurrent owner. Such concurrent export is outside this qualifier.
