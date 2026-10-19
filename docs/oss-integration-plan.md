# Native OSS integrations and three visibility tiers

## Current implementation status, 2026-10-19

Owner-only Personal Cache projects merged in
[#28](https://github.com/ziahamza/layercache/pull/28). Permanent Turbo conflict
handling and pin retirement merged in
[#27](https://github.com/ziahamza/layercache/pull/27), and installed-release QA
corrections merged in [#29](https://github.com/ziahamza/layercache/pull/29).

Nx [#31](https://github.com/ziahamza/layercache/pull/31) merged using the
released native client for Local and direct Personal/Team HTTP endpoints.
Linux and macOS real-client CI passed. Local-to-private read-through, durable
Nx publication, publication confirmation and reporting remain unimplemented.

Complete Xcode graph retention merged in
[#30](https://github.com/ziahamza/layercache/pull/30) after the real macOS gate
restored an action with two distinct native output roots under eviction.
Beta.4 includes both integrations and the owner-only Personal model; see the
[release and installer qualification](../scripts/README.md). These features do
not activate the hosted self-serve service.

[TODOs.md](../TODOs.md) owns the current order. Reliable Phone handoffs,
independently reusable test groups and exact LLM result replay now precede
broader protocol adoption. Native Gradle's first hosted Android correctness
gate now passes; qualify net benefit, retained handoffs and Local-to-private
forwarding remain outstanding. The user has prioritized native Bazel support;
the first [HTTP integration](bazel-cache.md) uses an isolated pinned
`bazel-remote` engine without requiring application migration. Source, deployed
gateway and application qualification remain separate. sccache still needs a
measured workload. Keep native clients responsible for task hashing, execution
and packing.

The protocol research below began at repository baseline
`1d7ebdffafb81fa00e81721113264122bbb0aa65` on 2026-10-14. Its pinned-source
observations are retained; the status above and remaining-work sections describe
the later implementation state.

## Keep visibility separate from storage location

Personal, team/organization and public are access boundaries. Local disk and a
remote service are storage locations. A personal cache can span an owner's
machines without becoming team-readable. Both private tiers
retain the native tool's complete cache payload, including declared outputs and
logs, by default. Complete task inputs remain the tool's responsibility; an
opaque archive cannot repair an incomplete fingerprint.

| Boundary | Current implementation | Required next behavior |
| --- | --- | --- |
| Personal | Owner-only remote projects with stable numeric ownership, separate project identities and explicit owner-authorized CI, merged in #28. | Qualify useful cross-machine reuse. Team membership or stale membership records must never grant personal access. |
| Team/organization | Project-scoped capabilities, membership roles, quotas and private Team Cache. | Keep full native payloads, authorized local writers and read-only PR credentials. Membership removal must affect continued access. |
| Public | Signed publications populated by the trusted collector, with verification/revocation and a separate Public Build contract. Hosted Public Builds remain unqualified. | Preserve collector-only population. A developer or team upload cannot promote itself into public scope. Qualify every new public protocol independently. |

These observations follow [capability validation](../internal/access/capability.go),
[runtime roles](../internal/config/config.go), [private artifact keys](../internal/artifact/store.go),
[Turbo resolution](../internal/server/server.go), and [public collection](../internal/server/public.go).
The private transport uses role `team` for both private visibility tiers.
The implemented `kind: personal|team` collection and distinct project IDs enforce
the owner boundary; existing records retain team semantics. Invitations,
arbitrary member mutations, repository membership and stale membership rows
cannot bypass personal ownership. Personal CI additionally requires the owner's
signed stable actor identity. See the [tier contract](cache-tiers.md).

## What already plugs in

Turbo v8, Actions cache v1 and BuildKit integrations are implemented. Successful
Turbo publication confirmation and explicit read-only/read-write/fresh task
policies are in beta.3. Xcode LLVM replay is implemented with local snapshot
transport; complete streamed graph retention merged in #30 for
[#25](https://github.com/ziahamza/layercache/issues/25). Nx's native Local and
direct private endpoints merged in #31. The current
[Gradle HTTP integration](gradle-cache.md) adds Local standalone and direct private
endpoints through the native client. Beta.5 installation and the existing gateway
rollout have [separate qualification](evidence/gateway-beta5-rollout-20261016.json);
the [first hosted Android producer/full-reader gate](evidence/gradle-android-hosted-20261016.md)
now passes with fresh runtime assertions. Net economic acceptance remains open.
The source adds opt-in private Bazel HTTP routing to a dedicated per-project/profile
engine, with its own bounded volume. It is not in beta.5 or the existing gateway
deployment. Bazel Local-to-private forwarding, gRPC, remote execution and Public
publication remain unsupported. sccache remains future work. Neither Nx nor Gradle
currently forwards Local writes or misses to a private endpoint; those bridges
need their own durable publication and confirmation qualification.

## Native Nx and the remaining private-cache bridge

Use the pinned OSS client and its published API. Do not create a task runner,
fingerprinter or archive packer. Nx provides bearer-authenticated
`GET/PUT /v1/cache/{hash}`, selected by `NX_SELF_HOSTED_REMOTE_CACHE_SERVER` and
`NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN`. [Official self-hosted API](https://nx.dev/docs/kb/self-hosted-caching).

The original research downloaded `nx@23.3.0`, matched its npm integrity,
inspected its shipped JS/native declarations, and loaded the compiled native
client on Linux x64.
The [23.3.0 source](https://github.com/nrwl/nx/tree/2b305ee85b8db033e4ab998f31282cbff66ac2a8)
is [MIT licensed](https://github.com/nrwl/nx/blob/2b305ee85b8db033e4ab998f31282cbff66ac2a8/LICENSE).
Pin both Nx and its matching native package through a lockfile.

```text
package: nx@23.3.0
tarball: https://registry.npmjs.org/nx/-/nx-23.3.0.tgz
integrity: sha512-R7Kl+hURSGiG9uEjc19Cg3o+vGCDHqCVp6mioiBMEsOSol/LgzP+K8TVF06TnnSC8uzxMeB0ymu+LGMJGXnbbw==
```

The released native client sends `application/octet-stream`, a sized gzip tar
body, and a bearer token. GET accepts 200 or 404. PUT accepts 200 and treats
409/403 as a declined write. A 401 needs `text/plain`; unexpected status and
network failures become errors. The archive includes outputs, `terminalOutput`
and a four-byte big-endian exit `code`. It supplies no signature or server TTL
contract. Preserve raw bytes; do not add HTTP gzip transformation.
[Pinned HTTP client](https://github.com/nrwl/nx/blob/2b305ee85b8db033e4ab998f31282cbff66ac2a8/packages/nx/src/native/cache/http_remote_cache.rs),
[error mapping](https://github.com/nrwl/nx/blob/2b305ee85b8db033e4ab998f31282cbff66ac2a8/packages/nx/src/native/cache/errors.rs).

Normal task execution stores/replays successful cacheable tasks. Setting
`NX_CACHE_FAILURES=true` enables cached failures; leave that disabled in the
initial deterministic-verdict lane. Local DB hits precede remote access, and
installed Nx Cloud/storage plugins can take precedence over this HTTP client.
[Pinned orchestrator](https://github.com/nrwl/nx/blob/2b305ee85b8db033e4ab998f31282cbff66ac2a8/packages/nx/src/tasks-runner/task-orchestrator.ts),
[cache selection](https://github.com/nrwl/nx/blob/2b305ee85b8db033e4ab998f31282cbff66ac2a8/packages/nx/src/tasks-runner/cache.ts).

A disposable compiled-client probe verified 200 restore/log replay, 404 miss,
409 immutability, 403 read-only writes, rejected unauthorized/outage responses,
concurrent first-writer behavior and a miss after deletion. This exercised a
loopback fixture server, not LayerCache or application performance. Temporary
evidence is `/tmp/layercache-nx-contract-lkd1fnhi/evidence.json`.

The adapter in #31 adds scoped GET/PUT handling over the bounded artifact
store, immutable first-writer behavior, native status/error mapping and private
cache-control. Project and compatibility come from the credential. Its real
compiled-client fixture proves cold execution, Local/restart replay and a fresh
read-only direct consumer with empty native and LayerCache caches, exact outputs
and logs, changed-input/profile misses and failed-test reruns. Private project
and owner isolation remain enforced. This is protocol and fixture acceptance,
not evidence of Phone whole-job savings or a Local-to-private bridge.

Remaining work:

1. Wire Local Cache to authenticated Personal/Team read-through and durable Nx
   publication. Extend upload jobs with `nx`, preserving the repaired conflict,
   pin and interruption lifecycle. Make destination authority explicit. Keep Nx
   Public Cache reads and writes disabled until its trusted recipe and verified
   publication path are qualified.
2. Add exact requested-hash publication confirmation and reporting. A local
   task hit cannot establish that the result reached the private endpoint.
   Include transfer overhead and distinguish conflicts from completed uploads.
3. Extend the real-client fixture through the bridge: stop the producer, restore
   on an empty consumer, and cover owner-authorized CI, read-only PRs, outages,
   queue restart and quota pressure. Preserve input/profile misses, failed-test
   reruns and private-project isolation. Test outage behavior before promising
   a non-blocking direct CI endpoint.
4. Account for Nx's separate native DB/cache directory. The initial server quota
   bounds LayerCache bytes only. A later owned scratch-directory lifecycle can
   prevent a second unbounded local cache; do not claim a total disk cap before
   that exists.
5. Measure audited Phone jobs end to end, including restore, transfer,
   publication and unavoidable fresh work. Keep task hits separate from
   whole-job time saved before broadening adoption.

The older `@nx/s3-cache`, `@nx/gcs-cache`, `@nx/azure-cache` and shared-fs packages
were deprecated in May 2026. Current upstream requires immutable keys and
separated writer authority. Use the native API rather than those plugins.
[Upstream deprecation and protocol requirements](https://nx.dev/docs/reference/deprecated/self-hosted-cache-packages).

## Next integrations without new fingerprinting

| Integration | Reuse | Concrete next work |
| --- | --- | --- |
| Gradle, private native path | Native task hashing and `HttpBuildCache`; Gradle is [Apache-2.0](https://github.com/gradle/gradle/blob/v9.3.1/LICENSE). | [Native protocol and first hosted Android qualification](gradle-cache.md) pass with fresh runtime assertions. Measure net benefit and add Local-to-private forwarding; signed/ARM and Public remain separate. |
| Bazel, after protocol qualification | [bazel-remote, Apache-2.0](https://github.com/buchgr/bazel-remote/blob/1605e32b5921d05e373407b98b3c021e40c277da/LICENSE), pinned executable first. | Put an authorization gateway above isolated per-project/profile instances with explicit byte budgets. Add a real Bazel fixture before moving a repository. |
| sccache | [Apache-2.0 compiler client](https://github.com/mozilla/sccache/blob/7bdb9bdcde435fd365e291715a6003f3ca28c2f9/LICENSE). | Add a separate bounded WebDAV namespace and use its bearer token, key prefix and READ_ONLY mode. Set `SCCACHE_WEBDAV_DISABLE_CREATE_DIR=true` when the gateway implements only object operations. |

Gradle's built-in remote protocol uses GET/PUT opaque keys, 404 misses and 2xx
success; 413 declines an oversize write. The pinned 9.3.1 client warns and
disables its remote after that response, so it is not an invisible quota decline.
Its archives contain variable origin metadata; duplicate native keys retain the
first committed winner without requiring equal archive bytes.
It supports Basic credentials, separate
local/remote settings and remote push disabled by default. Keep its hashing and
task declarations. [Gradle protocol and configuration](https://docs.gradle.org/current/userguide/build_cache.html).

Bazel uses separate action-result and output-CAS objects through native HTTP or
gRPC. Read-only clients can disable remote uploads. [Bazel remote caching](https://bazel.build/remote/caching).
bazel-remote already supplies HTTP, REAPI/ByteStream, LRU and dependency checks.
Its CAS HTTP path ignores instance names, and instance mangling affects action
keys only. A shared instance label is therefore not a privacy boundary. Its Go
module APIs have no stability guarantee; a pinned sidecar is the safer initial
reuse. Keep CAS bytes isolated behind validated project routing and do not turn
S3 access into client authority. [Pinned bazel-remote reference](https://github.com/buchgr/bazel-remote/blob/1605e32b5921d05e373407b98b3c021e40c277da/README.md),
[REAPI services](https://github.com/bazelbuild/remote-apis/blob/main/build/bazel/remote/execution/v2/remote_execution.proto).

sccache's existing WebDAV backend supports both Basic and bearer authentication,
prefixes and read-only operation, including compatible object services.
[Pinned client configuration](https://github.com/mozilla/sccache/blob/7bdb9bdcde435fd365e291715a6003f3ca28c2f9/docs/Webdav.md).
Use client fingerprints; do not infer compatibility merely because two tools use
HTTP or happen to produce identical bytes.

## Where Omni Cache fits

Omni exposes useful native-protocol implementations and can be an internal
comparison fixture. Its current FSL-1.1-ALv2 license allows internal use but
restricts competing commercial services. It is not an unrestricted commercial
foundation for the planned LayerCache service. Keep upstream contributions and
internal comparison separate from incorporating its implementation into a
competing hosted product. [Pinned license](https://github.com/cirruslabs/omni-cache/blob/0f424763fc5fd3e5ac3399d4b526454ed39d91d8/LICENSE),
[protocol guide](https://github.com/cirruslabs/omni-cache/blob/0f424763fc5fd3e5ac3399d4b526454ed39d91d8/PROTOCOLS.md).

Useful upstream work includes native-client conformance fixtures, namespace
isolation tests, complete-graph restore pressure tests and truthful transfer
outcomes. Keep LayerCache's ownership, access control, quota accounting and
cross-machine confirmation above the tool clients and mature protocol engines.
