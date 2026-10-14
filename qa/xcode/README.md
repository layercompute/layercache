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
returns that response. It marks references discovered by the compiler's own
Get requests and tracks successful fetches by CAS identity. Qualification requires
the compiler to Get its promised root and fetch every discovered descendant;
repeated root reads and a Load-only root do not satisfy that proof.
Successful Get adjacency is retained even when a child response precedes its
parent. The final reachable closure includes those transitive references and
requires every discovered object to have a successful post-pressure fetch.
Observation and closure are bounded to 100,000 objects and 262,144 references.
A missing or unfetched promised root or descendant fails qualification even
if Xcode later recompiles successfully. The helper sends no CAS read RPCs, so
its pressure writes cannot inflate compiler replay counters.

The quota is cold usage plus the largest cold CAS object and bounded slack.
Read-only SQL observations must show eviction and committed usage within that
quota. Evidence also records the query-to-first-root-fetch interval and pressure
duration. This controlled fixture must fetch within the fixed two-minute read
lease. It does not qualify Phone's larger graph or detached-query timing.

`go test -race ./qa/xcode/pressure-proxy` exercises the same interleaving through
real gRPC with a synthetic graph on Linux. Copying the QA package into the
pre-lease baseline makes that test fail when the promised root is evicted.
Only the macOS workflow can qualify the actual Apple compiler.
