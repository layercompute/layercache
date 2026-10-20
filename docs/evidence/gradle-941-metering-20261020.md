# Gradle 9.4.1 native metering qualification

On October 20, 2026 UTC, the seven public-listener controls passed with both Gradle
9.3.1 and 9.4.1 on Linux x64 and OpenJDK 17.0.20.1. Each ran from a five-file
source export without `.git`, using separate generated workspaces and Gradle
User Homes. This qualifies completed task observations and exact local log
correlation. Application adoption and compute or billing savings remain open.

The parser now accepts exactly `9.3.1` and `9.4.1`. Its grammar, byte/path bounds,
malformed-input refusal and handling of conflicting or shared native keys remain
unchanged. Other versions preserve accepted task outcomes with unknown key,
source and producer-timing coverage. The observer runner requires the selected
version to match the actual runtime and every collected report.

## Actual native controls

All seven stages passed independently on each version:

| Control | Observed result on each version |
| --- | --- |
| Original init-script comparison | Four selected root, subproject and included-build task keys match the unchanged baseline within that runtime. |
| Root/multi/composite/buildSrc, parallel cold execution | One root report, 12 completed tasks, five exact keyed tasks; no duplicated work references. |
| Warm configuration-cache reuse | A fresh invocation identity, four selected tasks up-to-date, no fabricated cache restores. |
| Accepted filesystem-cache restores | Five native `FROM-CACHE` outcomes, including the four selected task keys. |
| Explicitly disabled build cache | Selected tasks execute, carry explicit ineligibility and have no projected native keys. |
| Deliberately failed task | One failed outcome, no projected native key or successful execution duration. |
| Equivalent tasks sharing a key | Both distinct task mappings retain the same native key; producer ambiguity remains a server decision. |

The baseline remains the exact credential-free init script from
`f49b92adf365b0a877f588f884d7e3b885b1fdfd`, SHA256
`b45b47190b38f9f439d2324a49fbc5bbfdb8bf3a8671b63900c11d64ef32eaf5`.
The active init script is unchanged, SHA256
`fd946abea4fb4c14f7f07223df9b9a01efa9a8a0d7b1d17269924a598444316e`.
No cross-version native-key equality is claimed.

Forty-four focused controls passed across action setup/post, parsing, report
projection and the source offline CLI. These include exact 9.4.1 projection
through renewed scoped action authority, matching acknowledgement validation,
successful task-operation duration, unknown unsupported versions, malformed
messages, privacy and safe-file controls. All seven offline CLI controls also
passed against the regenerated checked-in bundle. Type checking, actionlint,
shell checks and whitespace checks passed.

Linux CI retains the complete 9.3.1 native HTTP matrix and its seven observer
stages. It adds the seven-stage 9.4.1 observer gate. Cached distribution ZIPs are
checked against pinned official SHA256 values before every extraction. The
9.4.1 ZIP is 137,878,901 bytes, SHA256
`2ab2958f2a1e51120c326cad6f385153bb11ee93b3c216c5fccebfdfbb7ec6cb`.
All 639 installed distribution files matched that verified official archive.
Raw task paths, logs and local reports remain private; CI retains sanitized
stage summaries only.

## Upstream contracts and limits

The key and cache-disabled logging methods are byte-identical in the pinned
[9.3.1 source](https://github.com/gradle/gradle/blob/v9.3.1/platforms/core-execution/execution/src/main/java/org/gradle/internal/execution/steps/AbstractResolveCachingStateStep.java#L113)
and [9.4.1 source](https://github.com/gradle/gradle/blob/v9.4.1/platforms/core-execution/execution/src/main/java/org/gradle/internal/execution/steps/AbstractResolveCachingStateStep.java#L109).
The public
[completion registry](https://github.com/gradle/gradle/blob/v9.4.1/subprojects/build-events/src/main/java/org/gradle/build/event/BuildEventsListenerRegistry.java#L33)
still supports a direct registered BuildService provider with configuration
cache. Native cache results still set both `fromCache` and `upToDate`, so the
collector checks `fromCache` first.

The filesystem controls do not identify Local versus Team HTTP delivery. That
requires the existing signed transport/publication joins, separately qualified
with the unchanged 9.3.1 HTTP matrix. Event intervals measure task-operation
elapsed time, not CPU consumption or critical-path savings. Existing beta.8
clients and action pins do not gain 9.4.1 correlation retroactively; use the
new qualified action/source revision. No application build, hosted diagnostic
dispatch or live service change was performed for this qualification.
