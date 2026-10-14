# Nx native private cache

Layer Cache accepts Nx's native `GET/PUT /v1/cache/{hash}` protocol on Local
Cache and authenticated private project endpoints. Nx owns task hashing,
execution, archive packing and replay. Layer Cache stores the complete opaque
archive, including declared outputs and terminal logs, in its existing bounded
artifact store. This adapter is included in
[beta.4](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.4).

The acceptance fixture pins released `nx@23.3.0` and its matching compiled
native package. Its [native HTTP client](https://github.com/nrwl/nx/blob/2b305ee85b8db033e4ab998f31282cbff66ac2a8/packages/nx/src/native/cache/http_remote_cache.rs)
uses `NX_SELF_HOSTED_REMOTE_CACHE_SERVER` and
`NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN`. It sends no compatibility header.
See Nx's [self-hosted cache contract](https://nx.dev/docs/kb/self-hosted-caching).

## Local use

Start the configured Local Cache daemon, then run the existing Nx command:

```sh
layercache run --config /absolute/path/layercache.json -- pnpm nx run-many --targets=build,test
```

After checking the daemon, `run` supplies the native server and an expiring,
Nx-scoped credential. `--bypass nx`, persisted Nx bypass, and `--bypass all`
suppress that connection. A repository-scope mismatch also removes inherited
Layer Cache Nx credentials. An unavailable daemon keeps the command's existing
environment, matching the other integrations' current fallback policy.

Nx checks its own filesystem cache before making HTTP requests. Keep accurate
input and output declarations, and disable failure caching unless replaying
failed tasks is intentional. Installed Nx Cloud or storage plugins may select
another remote implementation ahead of the native HTTP client.

## Direct private endpoint

Configure the two native environment variables with an authenticated private
endpoint and a project capability. The credential must include a valid
compatibility profile. Nx hashes remain opaque; Layer Cache scopes every key
by authenticated project, `nx` integration and compatibility. Another project's
credential, another profile or a Turbo credential cannot restore that archive.
The shared private project engine serves owner-only Personal Cache projects.
Shared Team projects retain current membership checks. Personal CI additionally
requires the owner's signed stable actor identity.

GitHub Actions exchanges accept `integration: nx`. Main push, manual and
scheduled runs retain the existing writer policy. Other refs, PRs and unknown
events get read-only authority. Use the signed exchange response's token as
the native access token. Current membership still applies when it is used.

GET returns exact `application/octet-stream` bytes or 404. PUT returns 200 for
the first writer and 409 for every existing key, including an identical archive.
The root API reserves `quota` and `pins` for administration, so those names
cannot be native artifact keys. The pinned Nx fixture uses generated numeric
task hashes.
Read-only and capacity-limited writes return 403, which the native client
declines without replacing a successful task result. Invalid credentials return
plain-text 401. Private responses use `Cache-Control: private, no-store`.
The existing byte quota, staging limits, idle transfer deadlines and integrity
checks apply. Cloud archives are fully verified in bounded staging before any
success response or archive bytes are sent. Corrupt archives become misses and
can be rebuilt; insufficient verification staging also returns a miss without
deleting the cloud archive. Storage-pool admission rejects uploads with the
native client's 403 decline response, including capacity lost during streaming.
Existing cloud retention policy applies to cloud-backed project stores.

## Implemented limits and qualification

This slice supports a standalone Local endpoint and direct private endpoints.
It does not forward Local Nx writes to Team Cache or read through to a remote
private endpoint. Nx durable upload jobs, publication confirmation, reporting
and Public Cache reads or writes remain unimplemented. A direct endpoint outage
can fail Nx's native remote operation; this slice does not promise fail-open CI.

Nx also owns a separate filesystem cache and database. Layer Cache's byte quota
does not bound that state or total workspace disk use.

Run `pnpm qa:nx-native` for the pinned real-client acceptance. It proves cold
build/test execution, Local replay and restart with Nx's own cache removed,
fresh read-only direct Team restore with zero task executions, exact output and
log replay, source/dependency/environment/profile misses, and failed-test
reexecution. Go HTTP tests cover concurrent first writers, immutable duplicates,
quota rejection, project/integration isolation, membership revocation, corruption
recovery, Public Cache denial and OIDC reader/writer policy. See the
[runner instructions](../qa/nx-native/README.md) for retained evidence.
