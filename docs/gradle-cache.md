# Gradle native private cache

LayerCache accepts Gradle's native HTTP build-cache protocol at
`/v1/gradle-cache/{hash}` on standalone Local Cache and authenticated private
project endpoints. Gradle owns task hashing, execution, archive packing and
restore. LayerCache retains the opaque archive in its existing bounded artifact
store, under the authenticated project, compatibility profile and `gradle`
integration.

Native Gradle support is included in immutable beta.5 at source
`53ec27443a406a508b7e8faeab3f46292e0ba07b`. The
[published installer qualification](evidence/beta5-installed-20261016.json) and
[existing gateway/host rollout](evidence/gateway-beta5-rollout-20261016.json)
passed independently. The [first hosted Android producer/full-reader gate](evidence/gradle-android-hosted-20261016.md)
also passes with fresh runtime acceptance. Net-benefit measurement,
Local-to-private forwarding and signed/ARM qualification remain separate work
with the Phone CI owner retaining application workflows.

## Local use

Use the credential-free [init script](../native/gradle/init.gradle) with a
published beta.5 CLI and its compatible Local daemon:

```sh
LAYER_CACHE_GRADLE_PUSH=1 layercache run \
  --config /absolute/path/gradle-project.json -- \
  ./gradlew --init-script /absolute/path/layercache/native/gradle/init.gradle \
  --build-cache build
```

After verifying the daemon and repository scope, `run` supplies the Local URL
and an expiring Gradle-scoped capability through the child environment. The
script reads `LAYER_CACHE_GRADLE_URL` and `LAYER_CACHE_GRADLE_TOKEN`; it contains
no credential. Remote publication requires `LAYER_CACHE_GRADLE_PUSH=1`.
`--bypass gradle`, persisted bypass, `--bypass all`, a scope mismatch or an
unavailable daemon removes the LayerCache Gradle connection. Ordinary Gradle
execution still follows its own caching flags and configuration.

The script does not enable build caching or replace an existing HTTP remote.
Use `--build-cache` explicitly or retain the project's existing native setting.
Use `--no-build-cache --rerun-tasks` when fresh task execution is required.
Gradle's `--offline` mode disables its HTTP build cache, including loopback.
Unset both connection variables to leave existing settings untouched; a partial
connection is an error. Explicitly setting `LAYER_CACHE_GRADLE_LOCAL_ENABLED=0`
disables Gradle's separate directory cache, which is useful for proving HTTP
restores. Otherwise its existing setting remains.

## Direct Personal or Team endpoint

Set the same URL and token variables through an external secret facility. The
URL ends in `/v1/gradle-cache/`; credentials never belong in URL userinfo, query
parameters, settings files or command arguments. HTTPS is required outside
loopback. The init script uses Gradle's native Basic credentials with the fixed
username `layercache` and the project capability as its password.

The username grants no authority. LayerCache verifies the capability's signature,
project, compatibility profile, integration, expiry and current membership.
Choose an explicit profile for the actual Gradle/JDK/platform/Android toolchain
and other compatibility assumptions that the native task declarations require.
Gradle sends no separate profile header. Personal projects retain owner-only
access; Team projects retain current roles. OIDC exchanges accept
`integration: gradle`, with the existing trusted main-event writer policy and
read-only authority for PRs, other refs and unknown events. Public Gradle access
is disabled.

For GitHub jobs, the [native Gradle action](../action/gradle/README.md) obtains an
ephemeral Gradle-scoped OIDC capability without installing the CLI. Supply an
explicit endpoint, project and full compatibility profile, then pass its
credential-free `init-script` output to Gradle. Publication is opt-in and remains
subject to the server's default-branch event and membership policy. Authentication
unavailability clears the connection and permits ordinary execution; invalid
inputs and runner-file errors fail setup. The action does not change Gradle's
cache flags, replace another remote or establish Android build savings.

Gradle has one HTTP remote slot. A Local endpoint does not forward Gradle writes
or read misses to Personal or Team Cache in this slice. A Local acknowledgement
therefore establishes only Local admission. Use the direct shared private
endpoint for the qualified cross-machine producer/consumer path. Automatic
Local-to-private forwarding and destination publication confirmation remain
open in [TODOs.md](../TODOs.md).

## Storage and native behavior

GET returns complete verified archive bytes or 404. PUT streams through bounded
staging and acknowledges admission only after commit. Existing physical storage
limits, retention, read leases and transfer deadlines apply. Cloud-backed reads
verify complete size and digest in bounded staging before sending HTTP 200.
Corrupt entries become misses. Insufficient read-verification capacity must not
delete a retained cloud archive.

The first retained archive wins for a native key. Duplicate writes return native
success while preserving that winner. Byte equality is not required: Gradle's
[origin metadata](https://github.com/gradle/gradle/blob/v9.3.1/platforms/core-execution/build-cache-base/src/main/java/org/gradle/caching/internal/origin/OriginMetadataFactory.java)
contains invocation identity, time and duration. A duplicate acknowledgement
does not say the caller's exact archive replaced the retained bytes, and eviction
or expiry can remove the winner later.

Authentication failure returns 401, a reader's PUT returns 403, and capacity
declines return 413. The pinned
[Gradle 9.3.1 client](https://github.com/gradle/gradle/blob/v9.3.1/platforms/core-execution/build-cache-http/src/main/java/org/gradle/caching/http/internal/HttpBuildCacheService.java)
and its [controller](https://github.com/gradle/gradle/blob/v9.3.1/platforms/core-execution/build-cache/src/main/java/org/gradle/caching/internal/controller/service/BaseRemoteBuildCacheServiceHandle.java)
determine whether errors disable the remote or fail the build. Do not assume
that a quota decline is invisible to Gradle or that every outage is harmless.
The acceptance runner records the actual pinned-client behavior.

Gradle's own directory cache, dependency cache, daemon and configuration-cache
state are outside LayerCache's byte budget. LayerCache does not write secrets
into the init script; Gradle still owns its runtime and persisted state. The
initial native qualification disables configuration caching and uses isolated
private Gradle homes.

## Native metering

See [native measurement](native-metering.md) for completed task/compiler evidence,
offline report export and import, hit-rate coverage, and the limits of elapsed,
compute and cost estimates. Native cache transport alone is insufficient.

## Qualification and scope

Reviewed runtime source `3b9ded989140abadedb476cfc29fc100163c9597` passes
[22 real-client commands](evidence/gradle-native-private-20261015.json) with
Gradle 9.3.1 and JDK 17.0.20.1. Total qualification time is 134.472s, including
25.590s of setup. The cold Local command takes 5.382s and its empty consumer
4.789s; direct private production takes 5.510s and its empty reader 5.016s.
Both consumers restore exact compiled and verification outputs with zero task
actions. Fresh Gradle initialization dominates this small Java fixture; these
times are not Android savings. The receipt records 7,233 upload bytes and
10,402 download bytes from native requests; independent archive verification
downloads are outside those counts.

The [real-client runner](../qa/gradle-native/README.md) uses pinned Gradle 9.3.1
and fresh compatible consumers. Its receipt distinguishes executed tasks from
native `FROM-CACHE` outcomes, verifies exact outputs, and records complete
command times and HTTP transfer counts. Input/profile misses and failed-task
reruns remain required. Go controls cover authorization, project routing,
revocation, concurrent duplicates, interrupted uploads, integrity and quota
pressure. Source qualification is separate from CLI installation, application
adoption and gateway deployment.

The [managed portal's Personal controls](evidence/gradle-personal-policy-20261015.json)
also exercise the Gradle route: Personal
and Team projects for the same repository retain different bytes across restart;
only the Personal owner's signed CI identity can obtain authority; PR access is
read-only; and owner changes revoke previously issued credentials. These controls
use the real portal, project gateway and artifact engine. The Java native-client
fixture separately qualifies the archive protocol.

[Real cloud upload controls](evidence/gradle-cloud-upload-integrity-20261015.json)
use PostgreSQL 16 and the production S3 SDK against a disposable multipart/copy
endpoint. They reproduced a malformed-upload publication bug: the SDK accepts
an exact `io.ErrUnexpectedEOF` as a valid final part. The reader now wraps and
retains terminal errors before storage consumes them. The tested small truncated, overlong and
interrupted streams return HTTP 400 without committed bytes or metadata;
capacity refusals retain HTTP 413. A full-buffer regression preserves the error
even when `io.ReadFull` discards a simultaneous error. The global cloud error
sanitizer is unchanged. These controls pass with race detection.

This reuses the Apache-2.0 Gradle
[native HTTP implementation](https://github.com/gradle/gradle/blob/v9.3.1/platforms/core-execution/build-cache-http/src/main/java/org/gradle/caching/http/internal/DefaultHttpBuildCacheServiceFactory.java).
No custom task hasher, build executor or Gradle plugin is added. Phone keeps its
existing ccache and live Contacts/runtime assertions. A restored task output is
not a retained verdict that a current app or live provider still passes.

The Linux native-client CI job downloads the pinned Gradle 9.3.1 distribution,
verifies its published SHA-256, and runs the same acceptance against the
candidate binary. It retains only the sanitized receipt. The published beta.5
binary independently passed the same 22-command acceptance on Linux x64, with
matching binary/source/init digests. Gradle on Linux arm64 and macOS remains
unqualified; installer acceptance for other native clients does not cover it.

The Linux x64 release job also runs all 22 real-client commands against the
binary it packages. Publication requires a successful receipt for that exact
source, binary and init script. The receipt is retained for 30 days and includes
only the sanitized protocol and timing evidence described above. A release
candidate passing this gate does not qualify an installed published release,
Linux arm64 or macOS Gradle use, an existing gateway upgrade, or Android reuse.
The CLI package contains the binary; consumers obtain the credential-free init
script from the same pinned source separately.

## Reference Android build

The [Android qualification receipt](evidence/gradle-android-private-20261016.json)
records one Linux x64 Debug producer and a relocated read-only consumer with
separate empty Gradle homes, filesystem build caching disabled and unchanged
native keys. Full Gradle command time fell from 880.213s to 524.971s, 40.36% in
this one local pair. It restored 318 tasks and downloaded 148,112,872 bytes with
zero uploads. The 2,063 HTTP hits include artifact transforms and repeated keys.

The fixture verified 290 compiled-output files and 13 selected APK entries,
including all DEX entries, manifests/resources and build provenance. It also
required changed-source execution, repeated compiler failures without publication,
reader-write denial and an incompatible-profile miss. Setup/prebuild is outside
the command times; host load and OS file caches were uncontrolled. The profiler's
CPU counters cover a shared host cgroup, so no isolated CPU or billing saving is
claimed. Device/runtime acceptance and signed releases remain separate.

The copied warm ccache seed delivered zero hits. Its 206 PCH refusals and 94
cacheable misses are separate from Gradle reuse; see the
[compiler investigation](research/android-compiler-cache-20261016.md). Both NDKs
selected during the build are recorded. The initial profile named only the root
NDK and stayed fixed within this disposable pair. It is an experimental namespace,
not a production compatibility generator. The later hosted adoption uses its
actual selected-model profile; its [receipt](evidence/gradle-android-hosted-20261016.md)
keeps native restores, compiler warmth and complete job times distinct.
Local-to-private forwarding and net economic acceptance remain pending.
