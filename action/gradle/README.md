# Native Gradle HTTP cache action

This action connects Gradle directly to a private LayerCache project through
GitHub OIDC. It requires an endpoint that supports `integration: gradle` and
explicit project and complete toolchain compatibility identities. Pin the action
to a reviewed commit. Its init script comes from that same source revision.

```yaml
permissions:
  contents: read
  id-token: write

steps:
  - uses: actions/checkout@v7
  - id: gradle-cache
    uses: layercompute/layercache/action/gradle@7b5861df3db72882b4122dd66af5e87bb37a625e
    with:
      team-url: https://cache.example.com
      project: private-project
      compatibility: linux-x64-jdk17-gradle931-android36-ndk271-schema1
      native-info-log: ${{ runner.temp }}/gradle-native-info.log
      push: ${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}
  - name: Build with native Gradle keys
    env:
      CACHE_INIT_SCRIPT: ${{ steps.gradle-cache.outputs.init-script }}
    run: |
      set -o pipefail
      cache_args=()
      if [[ -n "$CACHE_INIT_SCRIPT" ]]; then
        cache_args+=(--init-script "$CACHE_INIT_SCRIPT")
      fi
      ./gradlew --build-cache --info --console=plain "${cache_args[@]}" build 2>&1 |
        tee "$RUNNER_TEMP/gradle-native-info.log"
```

The compatibility above illustrates the required input; it does not establish
that those toolchains are mutually compatible. Derive the actual profile from
your pinned Gradle wrapper, JDK, OS/architecture, Android/other toolchain and
output-affecting build configuration. Gradle still owns complete native task
identity and archive contents.

The action exports `LAYER_CACHE_GRADLE_URL`, `LAYER_CACHE_GRADLE_TOKEN` and
`LAYER_CACHE_GRADLE_PUSH`. The token is masked, Gradle-scoped, and expires after
`ttl-minutes`, from 1 to 60 with a default of 60. Rerun the action before expiry.
It never writes a credential into the init script or command arguments.

Publication defaults to disabled. `push: true` additionally requires the scoped
credential to allow writes, its ref to match the configured default branch, and
the current event to be `push`, `workflow_dispatch` or `schedule`. PRs and
`pull_request_target` use `push=0`. Inspecting a capability does not verify its
signature; the server enforces signature, profile, membership and write policy.

Unavailable OIDC authentication or exchange leaves `configured=false`, an empty
init-script output and cleared connection variables. Your build continues with
its ordinary native settings. Invalid inputs and runner file errors fail setup.
The action first clears inherited LayerCache Gradle connection variables, and
publishes the new connection as one rollback-protected runner-file append.

The copied script is credential-free, mode 0600, in an owned mode 0700 runner
directory. Post cleanup removes that directory. Gradle's runtime may retain its
own state; choose isolated private Gradle homes for qualification.

This action does not install the CLI, start a Local daemon, forward Local
archives, enable build caching, disable Gradle's filesystem cache or replace an
existing remote. Loading the init script when another remote is configured
fails explicitly. Required release handoffs still use retained artifacts.

## Native task reporting

The reviewed action pinned above enables optional task reporting when the server
advertises `nativeReports` version 1. Older HTTP-only revisions do not contain
this collector. It uses Gradle's public task completion events and a configuration-cache
compatible build service. One root listener covers subprojects, included builds
and `buildSrc`; it does not add tasks or change their native keys.

Accepted `FROM-CACHE` results are eligible cache hits. Up-to-date, no-source,
skipped, failed and executed results keep separate counts. A successful executed
task's eligibility remains unknown unless native output supplies its cache key
or explicitly says caching was disabled. HTTP downloads alone do not establish
accepted task hits. Gradle's completion API does not supply cache keys or cache
source.

For exact native-key correlation, provide `native-info-log` as an absolute path
to one completed Gradle 9.3.1 or 9.4.1 `--info` log. Keep the caller's existing command and
preserve its exit status, for example:

```yaml
# Keep one completed native info log per action instance:
with:
  native-info-log: ${{ runner.temp }}/gradle-native-info.log

# In the following build step, pass the init-script output as usual:
run: |
  set -o pipefail
  cache_args=()
  if [[ -n "$CACHE_INIT_SCRIPT" ]]; then
    cache_args+=(--init-script "$CACHE_INIT_SCRIPT")
  fi
  ./gradlew --build-cache --info --console=plain \
    "${cache_args[@]}" build 2>&1 |
    tee "$RUNNER_TEMP/gradle-native-info.log"
```

Use one invocation per action instance when supplying one info log. Multiple
invocations sharing a log, unsupported versions, missing completion, changed
files and ambiguous task-path mappings leave key-dependent evidence unknown.
Equivalent task paths can share a native key; the server separately requires a
unique matching producer publication before attaching original timing.

Private files are bounded and remain in the owned runner directory. The client
uploads only hashed task references, qualified native keys, outcomes and native
operation timestamps. It uploads no raw task paths, logs, commands, inputs or
credentials. The server scopes and hashes keys using the authenticated project
and profile. It derives producer timing and cache source from matching native
transport evidence, rather than accepting those claims in the report body.

Post reporting renews this action's own job authority, including for read-only
PRs. Authentication or submission failure leaves reporting unavailable and
preserves the build result; it does not rerun Gradle. The client confirms only a
bounded final receipt matching the exact invocation and task count. An unknown
acknowledgement is not retried automatically. Cleanup still removes the
owned runner directory. Native task elapsed time can include cache overhead and
overlap other tasks. It is distinct from CPU usage, complete build time and
provider charges.
