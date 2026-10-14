# Native OSS integrations and three visibility tiers

Research snapshot: 2026-10-14. Repository baseline
`1d7ebdffafb81fa00e81721113264122bbb0aa65`. Recommendations below are proposed,
unless marked implemented. No provider migration or adapter implementation is
implied by this note.

## Keep visibility separate from storage location

Personal, team/organization and public are access boundaries. Local disk and a
remote service are storage locations. A personal cache can eventually span an
owner's machines without becoming team-readable. Both private tiers should
retain the native tool's complete cache payload, including declared outputs and
logs, by default. Complete task inputs remain the tool's responsibility; an
opaque archive cannot repair an incomplete fingerprint.

| Boundary | Implemented baseline | Required next behavior |
| --- | --- | --- |
| Personal | Owner-only local files/configuration and a single-user loopback daemon. No first-class owner-only remote collection. | Stable numeric owner identity, private project namespaces, owner-only authorization and explicit workflow delegation. A shared engine can serve this scope without sharing artifacts. |
| Team/organization | Project-scoped capabilities, membership roles, quotas and private Team Cache. | Keep full native payloads, authorized local writers and read-only PR credentials. Membership removal must affect continued access. |
| Public | Signed publications populated by the trusted collector, with verification/revocation and a separate Public Build contract. Hosted Public Builds remain unqualified. | Preserve collector-only population. A developer or team upload cannot promote itself into public scope. Qualify every new public protocol independently. |

These observations follow [capability validation](../internal/access/capability.go),
[runtime roles](../internal/config/config.go), [private artifact keys](../internal/artifact/store.go),
[Turbo resolution](../internal/server/server.go), and [public collection](../internal/server/public.go).
Currently the private transport uses role `team`; that is not a reason to expose
personal bytes to team members. A proposed `kind: personal|team` above existing
project IDs can add the missing owner boundary. Existing records should retain
team semantics. Invitations, arbitrary member mutations, repository membership
and stale membership rows must not bypass personal ownership. Any CI access to
personal scope needs an explicit owner delegation rule.

## What already plugs in

Turbo v8, Actions cache v1 and BuildKit integrations are implemented. Successful
Turbo publication confirmation and explicit read-only/read-write/fresh task
policies are in beta.3. Xcode LLVM replay is implemented with local snapshot
transport; streamed graph protection remains [#25](https://github.com/ziahamza/layercache/issues/25).
Bazel, Nx, Gradle HTTP and sccache remote adapters are not implemented. The
capability and upload-queue adapter allowlists currently name only Turbo,
Actions and BuildKit. New routes must extend those boundaries deliberately;
renaming a Turbo key is insufficient.

## Smallest next adapter: Nx's native HTTP client

Use the current OSS client and its published API. Do not create a task runner,
fingerprinter or archive packer. Nx provides bearer-authenticated
`GET/PUT /v1/cache/{hash}`, selected by `NX_SELF_HOSTED_REMOTE_CACHE_SERVER` and
`NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN`. [Official self-hosted API](https://nx.dev/docs/kb/self-hosted-caching).

I downloaded stable `nx@23.3.0`, matched its npm integrity, inspected its shipped
JS/native declarations, and loaded the compiled native client on Linux x64.
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

Build the following slice after storage fixes:

1. Add an `nx` integration and protocol-specific GET/PUT response handling over
   the existing bounded artifact store. Scope every key by authenticated project,
   integration, execution compatibility and opaque native hash. Take project and
   compatibility from the credential. The native client sends neither custom
   compatibility headers nor a SHA-256 task-key promise.
2. Return 409 for every existing Nx key, including an identical-byte duplicate,
   without overwriting the winner. Enforce length limits, atomic admission,
   digest verification, quotas, idle timeouts and expiry using existing storage.
   Return private cache-control headers so shared proxies cannot retain private
   context. HTTP cache-control does not disable Nx's own filesystem cache.
3. Wire loopback Local Cache to authenticated personal/team read-through and
   durable publication. Extend upload jobs with `nx`, preserving the repaired
   conflict/pin lifecycle. Keep Nx public reads and writes disabled until the
   trusted Public Build recipe and publication verification are implemented.
4. Add a pinned real Nx CLI fixture with a build and test, external execution
   counters and output digests. Prove producer execution, local reuse and a fresh
   read-only consumer with empty native/LayerCache caches. Include changed
   source/dependency/environment/profile misses, failed-test reruns, TTL/quota
   eviction, concurrent writers, identity isolation and daemon restart. Test
   actual outage behavior before promising a non-blocking direct CI endpoint.
5. Account for Nx's separate native DB/cache directory. The initial server quota
   bounds LayerCache bytes only. A later owned scratch-directory lifecycle can
   prevent a second unbounded local cache; do not claim a total disk cap before
   that exists. Add publication confirmation only after exact requested native
   hashes can be verified, as Turbo already does.

The older `@nx/s3-cache`, `@nx/gcs-cache`, `@nx/azure-cache` and shared-fs packages
were deprecated in May 2026. Current upstream requires immutable keys and
separated writer authority. Use the native API rather than those plugins.
[Upstream deprecation and protocol requirements](https://nx.dev/docs/reference/deprecated/self-hosted-cache-packages).

## Next integrations without new fingerprinting

| Integration | Reuse | Concrete next work |
| --- | --- | --- |
| Gradle | Native task hashing and `HttpBuildCache`; Gradle is [Apache-2.0](https://github.com/gradle/gradle/blob/master/LICENSE). | Add bounded GET/PUT opaque artifacts and a narrow Basic-auth bridge to scoped credentials. An init script can configure it without a custom Gradle plugin. Qualify audited Android tasks; cache outputs and task execution remain distinct from live runtime tests. |
| Bazel | [bazel-remote, Apache-2.0](https://github.com/buchgr/bazel-remote/blob/1605e32b5921d05e373407b98b3c021e40c277da/LICENSE), pinned executable first. | Put an authorization gateway above isolated per-project/profile instances with explicit byte budgets. Add a real Bazel fixture before moving a repository. |
| sccache | [Apache-2.0 compiler client](https://github.com/mozilla/sccache/blob/7bdb9bdcde435fd365e291715a6003f3ca28c2f9/LICENSE). | Add a separate bounded WebDAV namespace and use its bearer token, key prefix and READ_ONLY mode. Set `SCCACHE_WEBDAV_DISABLE_CREATE_DIR=true` when the gateway implements only object operations. |

Gradle's built-in remote protocol uses GET/PUT opaque keys, 404 misses and 2xx
success; 413 declines an oversize write. It supports Basic credentials, separate
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
