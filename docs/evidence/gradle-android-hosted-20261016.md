# Hosted Android Gradle qualification, 2026-10-16

The first trusted main-push producer and independent read-only full consumer
passed at application source `0fd189000d1166a91ca39a2a94f3de6e1e2c8cc9`,
merged by [Phone #317](https://github.com/ziahamza/phone-app/pull/317).
Main Check is green. This is hosted correctness evidence; net performance and
billing acceptance remain open.

| Evidence | Trusted producer | Independent full reader |
| --- | --- | --- |
| Hosted run | [38920244226](https://github.com/ziahamza/phone-app/actions/runs/38920244226) | [38920494987](https://github.com/ziahamza/phone-app/actions/runs/38920494987) |
| Event | Actual main push | Manual read-only full QA |
| Source | Exact merge source above | Same exact source |
| Complete measured job | 797s | 927s build job |
| Inner compilation/seed | 244.371s JVM seed | 616.427s full compilation |
| Selected-model discovery | 247.267s initial cold discovery | 40.426s |
| Compiler cache | Full C++/JNI graph excluded | 0/94 cacheable hits, 94 misses, 211 uncacheable calls |
| Gradle outcomes | 312 keyed paths, 186 unique native keys, 126 cache outcomes | 297 restored task paths with matching producer path/native key |
| Runtime | No APK/runtime work in the narrow producer | Full APK inspection and eight fresh fictional Contacts emulator steps pass |

Both builds use the accepted direct action
`ziahamza/layercache/action/gradle@53ec27443a406a508b7e8faeab3f46292e0ba07b`
and matching selected-model compatibility
`phone-android-gradle-v1-2cec266270d78a60c0fcc172d6c1a325072781ff15b263618cd4fa9e1a632470`.
The init script SHA-256 is
`b45b47190b38f9f439d2324a49fbc5bbfdb8bf3a8671b63900c11d64ef32eaf5`.
The action requires no installed CLI. App filesystem task caching is disabled;
each measured compile log contains three HTTP configuration lines and zero
local-directory configuration lines. Kotlin tests pass on both sides. The
producer's separate Kotlin test graph also excludes C++/JNI/signing/APK tasks.
Measured jobs report zero OOM events and no cache failure messages.

The full reader's receipt binds the same merge source, x86_64, successful APK
signature inspection, `compileOnly=false`, `runtimeAcceptance=true`, all eight
fresh steps and successful cleanup. This does not qualify signed Release/ARM.

## Attribution limits

All 126 producer cache outcomes have an executed same-key counterpart within
its own task graph. They are not evidence of 126 older-cache hits or unique
publications. Matching reader paths/keys establish compatible reuse without
proving that this producer uniquely supplied each stored archive. Server-side
PUT/GET counts and transferred bytes were not observed.

The earlier warm PR reader
[38915865730](https://github.com/ziahamza/phone-app/actions/runs/38915865730)
took 864s for its complete build job and 496s for inner compilation, with 94/94
compiler hits and zero Gradle task restores. Its source differs. The first
same-source reader above has a cold compiler cache, so comparing those durations
does not isolate native HTTP savings. Include the added 797s producer job and
discovery/setup/publication costs in the economic evaluation. The Phone owner
retains normal reader `38925086191`; its final result is not part of this receipt.
No combined hot-path speedup or monthly bill reduction is established here.

The earlier different-checkout-root local experiment's zero compiler hits and
the hosted stable-root warm compiler result describe different conditions.
No compiler safety flags changed; portable reuse across different roots remains
a separate qualification target.

## Required transfer observation

The first reader reports a required artifact of 124,078,900 bytes and 229s for
retrieval plus provenance verification. That is a complete step duration. The
custom client resolves metadata/authorization, receives and verifies a ZIP,
unpacks bounded entries, compares the producer manifest and separately verifies
the payload. Actual wire bytes and individual phases were not measured; do not
derive bandwidth or a new-backend saving from the reported size and duration.

The [retained-artifact contract](../retained-artifacts.md) owns the proposed
service semantics and phase-receipt acceptance. Existing retained-transfer
fallback stays in place until source, deployment and fresh consumer proof pass.
The historical Actions lookup miss in issue #34 remains unexplained.

## Evidence custody

The application owner retained the detailed report, input files and logs outside
the public repository. Read-only reconciliation validated all twelve report
input SHA-256 values and all three compile-log digests, recomputed the 297
matching restore path/key pairs and the producer's same-graph key counts, and
checked exact source/event/result against GitHub run metadata. This sanitized
record contains no archive contents, signed URLs, credentials or private traces.
It is not a public reproducible application benchmark or a new signed service
attestation. LayerCache made no application edits, dispatches or service changes
for this reconciliation.
