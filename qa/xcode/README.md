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
the first successful action response containing a CAS root, writes unique
unrelated CAS blobs totaling more than the configured artifact quota, then
returns that response. Before and after pressure, it uses read-only SQLite and
blob inspection to verify the complete promised graph, exact sizes and content
hashes. This includes native Xcode's inline `value` node and encoded external
nodes, LLVM's `CASID` root and Clang's output-name-to-ID map. The helper sends no
CAS read RPCs, so inspection cannot inflate compiler replay counters.

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
