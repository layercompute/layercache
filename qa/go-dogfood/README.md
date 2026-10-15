# Go compiler cache through standalone Actions v1

This opt-in qualification archives only fixture-owned `GOCACHE` and
`GOMODCACHE` directories through the locked stock `@actions/cache` 6.2.0
client and an account-free loopback LayerCache daemon. Go owns compiler keys
and executes every test with `-count=1`. No workflow, installed daemon,
operator configuration or host cache is changed.

Use Linux amd64, the repository's Go 1.25.13 and Node 24, and installed GNU tar
and zstd. Install the existing Action's locked dependencies if needed:

```sh
pnpm --dir action/cache install --frozen-lockfile
```

Build a candidate from the same source first, using ordinary bootstrap caches.
Record this one-time build cost separately from the private-cache experiment:

```sh
go build -trimpath -o /tmp/layercache-go-dogfood-candidate ./cmd/layercache
LAYERCACHE_BIN=/tmp/layercache-go-dogfood-candidate \
  GO_DOGFOOD_QA_RECEIPT=/tmp/layercache-go-dogfood.json \
  node qa/go-dogfood/run.mjs
```

`ACTIONS_CACHE_MODULE` may select an existing absolute
`@actions/cache/lib/cache.js`; its package version must be 6.2.0. The runner
requires tracked source to match HEAD and records the binary and driver
digests. The caller is responsible for building that binary from the recorded
source. It copies only tracked source files into separate temporary checkouts,
keeps all compiler/module caches under each checkout's `.fixture-cache`, and
disables dependency downloads and external `GOCACHEPROG`. The selected
`./internal/compatibility` package uses only the standard library, so this does
not qualify module dependency download savings.

The acceptance checks cold compilation, a stock archive save, a fresh relocated
restore, fresh passing tests, a same-path source change after a zero-compile
control, native recompilation under `GOAMD64=v2`, a miss for the same archive
key under an incompatible signed profile, and two fresh executions of an
intentional failing test. The normal fixture commands add no `-trimpath` or
other compiler flags. A relocated checkout may recompile repository packages
while reusing standard library outputs; the receipt records compiled package
names and complete subprocess wall times. Native `go test` result replay is
never used as evidence of fresh execution.

The output receipt path is invalidated before work begins; a new success receipt
is published atomically only after qualification and cleanup succeed. The receipt
contains no URLs, headers, credentials, absolute workspace paths or subprocess
logs. Each command has a 120-second timeout and an 8 MiB output
budget; owned process groups are stopped and temporary state removed on
completion or failure. `KEEP_GO_DOGFOOD_QA=1` retains a private mode-0700
directory for diagnosis; remove it after inspection. Actions archive runs do
not currently produce a LayerCache task-time report, so transfer bytes come
from the stock client's archive-size output and timings cover whole commands.

Local [measurement](../../docs/evidence/go-dogfood-20261015.json) at source
`ffaf5b66122e7103a1f21659aedf838687a9d581`: cold build plus fresh tests took
4.548 seconds; restoring 13,078,040 archive bytes plus the relocated build and
fresh tests took 0.801 seconds. Publication added 0.719 seconds. The consumer
recompiled four package/test units versus 123 cold compiler invocations;
standard library reuse accounts for the benefit. Setup took 0.027 seconds and
daemon startup 0.138 seconds; the one-time candidate build took another 1.292
seconds. First use was slower. All 20 qualification commands, including
input/failure controls and cleanup, took 11.957 seconds. An
[actual-driver interruption control](../../docs/evidence/go-dogfood-forced-cleanup-20261015.json)
also proved that a SIGTERM-ignoring child was stopped, failure remained visible,
the default private directory was removed and no passed receipt was emitted.
These are single-host loopback observations, not hosted CI, Phone, private Team or local-to-CI
qualification. Measure setup and real network transfer before adopting this
extra transport; existing GitHub Actions Go caching may be the simpler choice.
