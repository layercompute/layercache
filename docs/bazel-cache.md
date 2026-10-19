# Native Bazel HTTP cache

This source adds an opt-in Bazel HTTP endpoint backed by the Apache-2.0
`bazel-remote` engine. Bazel owns action keys, dependency declarations,
execution, output packing and test-result policy. The engine owns AC protobuf
validation, referenced CAS checks, compression and LRU storage. LayerCache
supplies project/profile routing and current private access policy.

This is a source integration, separate from the published beta.5 CLI, an
installed gateway, application adoption or remote execution. No existing
application needs to migrate to Bazel to keep using LayerCache.

## Dedicated engine and storage

Use one engine and data directory for each project and compatibility profile.
Do not share a `bazel-remote` instance between private projects by giving them
different instance names. Its CAS does not use those names as an access
boundary. LayerCache rejects duplicate engine origins in one project gateway;
the operator must also keep distinct origins backed by distinct stores.

The initial engine pin is released `bazel-remote` 2.6.3, source
`985cc081b1d5299e9d651ab01a605362f6bb170c`. Its Linux amd64 binary SHA-256 is
`65427624c288e29b17c7b0e189cf1d03ae1d8745354d9effc432f75c4361159a`.
The executable keeps its independent dependency/toolchain lifecycle; LayerCache
does not import its unstable Go library interfaces.
[Release](https://github.com/buchgr/bazel-remote/releases/tag/v2.6.3),
[engine contract](https://github.com/buchgr/bazel-remote/blob/985cc081b1d5299e9d651ab01a605362f6bb170c/README.md),
[license](https://github.com/buchgr/bazel-remote/blob/985cc081b1d5299e9d651ab01a605362f6bb170c/LICENSE).

For example, run an independently supervised engine with a dedicated volume:

```sh
/absolute/path/bazel-remote \
  --dir /absolute/path/project-bazel-cache \
  --http_address 127.0.0.1:8091 --grpc_address none \
  --max_size 1 --max_size_hard_limit 2 \
  --max_blob_size 67108864 \
  --http_read_timeout 60s --http_write_timeout 60s
```

Leave HTTP action validation enabled. The example has a 1 GiB logical LRU
target and a 2 GiB hard write-admission limit covering files queued for
eviction. The pinned engine accepts integer GiB limits; equal hard and logical
limits can fence writes before LRU eviction proceeds. Larger stores can use
closer relative headroom, such as 10/11 GiB. Neither limit includes native
output trees, filesystem metadata or another
volume's files. LayerCache's `maxBytes` controls its existing artifact store
and upload staging, not this engine's committed bytes. Budget the two stores
and client outputs together. Engine eviction is allowed; this is not retained
artifact storage.

## Explicit project binding

Add this object to an existing protected LayerCache configuration. Its project
and compatibility must match that configuration exactly:

```json
{
  "bazelRemote": {
    "projectId": "github.com/example/project",
    "compatibilityId": "project-linux-x86_64-bazel9-toolchain-v1",
    "url": "http://127.0.0.1:8091",
    "maxBlobBytes": 67108864,
    "requestTimeout": 60000000000
  }
}
```

`requestTimeout` is a Go duration encoded as nanoseconds, here 60 seconds.
The backend URL must be a bare HTTP origin on a literal loopback address with
an explicit port. Credentials, paths, queries and redirects are unsupported.
Copying this binding into another project or profile fails configuration
validation. Start the candidate daemon with `layercache serve --config FILE`.
No engine is started or configured implicitly, and an omitted binding leaves
Bazel disabled.

Use the normal authenticated project gateway outside the cache host, with
HTTPS outside loopback. Keep the unauthenticated engine listener private to
the trusted cache host. Loopback does not isolate it from other users or
untrusted builds running on that same host.

## Native client and authority

The client cache URL ends in `/v1/bazel-cache`. Native requests use
`GET`, `HEAD` or `PUT` on `ac/<sha256>` and `cas/<sha256>`. Digests must be
64 lowercase hexadecimal characters. This first slice supplies HTTP caching,
not Bazel gRPC, remote execution or a new task runner.

Configure the native credential helper approved for your environment and
supply a short-lived Bearer capability scoped to integration `bazel`, the
project and its exact compatibility profile. Keep credentials out of repository
files, URLs and persistent command arguments. For example:

```sh
bazel build //... \
  --remote_cache="$LAYER_CACHE_BAZEL_URL" \
  --credential_helper="$LAYER_CACHE_BAZEL_CREDENTIAL_HELPER" \
  --remote_upload_local_results=false
```

The credential helper is an external native Bazel helper, not a new LayerCache
CLI command. A standalone operator can use the protected runtime credential;
private readers and writers should receive scoped capabilities. GitHub OIDC
exchange accepts `integration: bazel` and retains the existing trusted
default-branch writer policy. PRs and unknown events remain readers. Every
request checks current membership. Personal ownership and Team membership
remain separate. Public Bazel reads and writes are disabled.

The client flag disables uploads but grants no authority. The server also
rejects reader PUTs. A Local endpoint does not forward misses or writes to
another private endpoint, and a successful PUT establishes only admission to
this evictable engine. It does not establish durable Team publication or a
required future handoff.
[Bazel remote caching](https://bazel.build/remote/caching),
[credential helper option](https://bazel.build/reference/command-line-reference#flag--credential_helper).

## Transfer and failure behavior

LayerCache stages a complete bounded PUT before forwarding it. Oversized,
truncated or interrupted request bodies cannot produce a proxy success before
verification. Staging is temporary and counted against the existing local
store's reservation and physical-space policy. This adds a bounded local I/O
pass; include it in real-workload measurements. The proxy forwards no client
credentials, cookies or project selectors to the engine and does not follow
redirects. Request and response transfers have bounded deadlines and size.

The engine verifies CAS upload digests and validates action results against
their referenced CAS. Missing CAS becomes a cache miss and Bazel rebuilds.
Client fallback after an unavailable backend is Bazel's policy; do not claim
that every cache error is invisible to a build. Preserving an earlier passing
test result also does not establish a fresh device or external-service check.

## Qualification

The [real-client fixture](../qa/bazel-native/README.md) uses pinned Bazel 9.3.0
and the released engine with independent output roots. Its evidence must
separate cold publication, empty read-only restore, restart, source/tool/env
misses, failed tests and protocol controls. Fixtures establish correctness,
not application-scale speed, Mac-minute or monthly cost savings.

Outstanding product gates remain in [TODOs.md](../TODOs.md). A native Bazel
cache backend does not qualify an application's migration from Xcode, Gradle
or Turbo; see the [build-system comparison](research/bazel-and-layercache-20261019.md).
