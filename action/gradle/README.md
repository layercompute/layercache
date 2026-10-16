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
    uses: ziahamza/layercache/action/gradle@REVIEWED_COMMIT
    with:
      team-url: https://cache.example.com
      project: private-project
      compatibility: linux-x64-jdk17-gradle931-android36-ndk271-schema1
      push: ${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}
  - name: Build with native Gradle keys
    env:
      CACHE_INIT_SCRIPT: ${{ steps.gradle-cache.outputs.init-script }}
    run: |
      cache_args=()
      if [[ -n "$CACHE_INIT_SCRIPT" ]]; then
        cache_args+=(--init-script "$CACHE_INIT_SCRIPT")
      fi
      ./gradlew --build-cache "${cache_args[@]}" build
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
