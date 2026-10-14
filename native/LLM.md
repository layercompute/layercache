# Recorded AI SDK model responses

`@layercache/native/llm` exports `createLLMReuseMiddleware` for AI SDK 7's
`wrapLanguageModel`. This first slice stores completed non-streaming provider
results through the existing private artifact client. It does not host an LLM
HTTP gateway or route providers. Qualification uses pinned `ai@7.0.107` and its
provider v4 interface, with actual `generateText` and `ToolLoopAgent` calls to
an OSS fake model. No paid inference or Phone deployment is part of this change.

Install AI SDK separately. It is an optional peer dependency; Expo consumers do
not need it. The LLM bundle does not include AI SDK.

```ts
import { wrapLanguageModel } from 'ai';
import { createLLMReuseMiddleware } from '@layercache/native/llm';

const planner = wrapLanguageModel({
  model: rawProviderModel,
  middleware: createLLMReuseMiddleware({
    project: 'github.com/your-team/your-app',
    scope: 'team', // 'personal' also supported; Public is unsupported
    compatibility: 'your-configured-execution-profile-v1',
    mode: 'read-through',
    profile: {
      endpointIdentity: 'your-provider-endpoint-and-routing-v1',
      accountIdentity: 'your-stable-authorized-provider-account',
      providerRevision: 'your-provider-package-and-adapter-pin',
      defaultsRevision: 'your-provider-defaults-and-policy-v1',
      modelRevision: 'your-model-snapshot-or-alias-generation',
    },
    context: {
      appArtifact: 'exact-app-artifact-digest',
      harness: 'exact-harness-and-tool-implementation-revision',
      configuration: 'exact-input-bearing-environment-and-data-profile',
    },
    corpus: { id: 'planner-regression', generation: '2026-10-14-1' },
    sampleSlot: '0',
    ttlMs: 24 * 60 * 60 * 1000,
    cache: { maxBytes: 64 * 1024 * 1024 },
    maxEntryBytes: 4 * 1024 * 1024,
    onEvent: event => console.log(event.outcome, event.currentProviderCalls),
  }),
});
```

For TesterArmy `e2e@0.20.0` and `@e2e-dev/mobile@0.12.0`, configure the planner
model and **explicitly configure the judge as `rawProviderModel`**. That e2e
release otherwise defaults its judge to the planner. Replayed tool calls still
execute tools against the current device. Different tool observations change
the next model request. This adapter does not store an E2E passing verdict or
replace upstream action-cache validation. Real e2e/agent-device adoption and
live-device qualification remain separate work. [Pinned e2e configuration](https://github.com/tester-army/e2e/blob/fc9064415d427060fad1d406c4c491dda248ad4f/packages/e2e/src/types.ts).

## Identity and private access

Every final normalized `doGenerate` option participates, including ordered
messages, instructions, tool schemas/calls/results, sampling, reasoning, output
format, provider options, headers and inline media bytes. AbortSignal is the
only excluded transport option; cancellation is enforced independently. A
versioned tagged canonical encoding preserves object-key equality, array order,
Unicode, binary bytes, undefined and Date values. SHA-256 identifies the exact
request together with project, private visibility, execution compatibility,
provider/model IDs, complete declared profile/context, corpus generation,
sample slot and expiry policy. Prompts are not trimmed or selectively hashed.
Only the digest and supported result envelope are stored, rather than request
debug bodies or raw authentication headers.

Declare everything that provider construction hides, including endpoint,
account/principal, routing/fallback, defaults and revisions. The middleware
cannot introspect those values or discover missing app/tool context. A model
name, temperature zero or seed does not prove deterministic fresh inference.
Each independent sample needs a separate slot; changes to the frozen recording
need a new generation. Put this middleware last in a middleware array, nearest
the provider, after all known request transformations.

Personal and Team are visibility choices. Local is the storage location. The
same declared visibility/project/context is used across local storage and a
fresh remote consumer. Credentials and the existing service's project/owner or
membership policy establish authority; setting `scope: 'personal'` does not
grant owner-only access to a Team project. This adapter has no Public lookup or
publication path. The legacy transport result `source: 'local' | 'team'` means
local disk or the authenticated remote artifact endpoint, not a visibility tier.

By default the middleware accepts paired `TURBO_API`/`TURBO_TOKEN` or
`LAYER_CACHE_URL`/`LAYER_CACHE_TOKEN` from the existing setup/login flow. Explicit
`cache.endpoint` plus `cache.token` can be supplied instead. Never put tokens
in profile, corpus, context or application configuration. The required
`compatibility` must match the daemon and issued capability. For example,
configure a dedicated project/execution profile with ordinary LayerCache setup,
then run the caller through that config. An existing Phone CI profile does not
automatically authorize a different compatibility value. With no credentials,
recordings stay on local disk. Remote publication uses the existing native
artifact integration; it has no dedicated LLM server accounting label or durable
publication queue in this slice.

## Modes and immutable samples

| Mode | Reads | Provider call | Writes |
| --- | --- | --- | --- |
| `replay` | Exact valid recording required | Never | Never |
| `record` | No lookup | Fresh | Immutable admission required |
| `read-through` | Exact valid recording when available | On miss or unsupported input | Best effort immutable admission |
| `fresh` | Never | Fresh | Never |

Record refuses a different retained sample and requires a new corpus generation
for deliberate replacement. First-writer consistency lasts while the artifact
is retained; this evictable cache does not preserve an eternal winner.
A concurrent read-through loser fetches and validates the committed winner.
It returns the winner's content and preserves the losing fresh call's usage as
current cost. A losing response never replaces the local winner. If that winner
is expired or unavailable, read-through returns its fresh response explicitly
marked `unrecorded`; it does not claim canonical frozen-sample replay.

The envelope stores absolute `recordedAt` and `expiresAt`. Hits do not renew
these fields. An expired replay fails without a provider call. Read-through can
run fresh after expiry. If the old artifact is still retained, a conflicting
fresh response remains unrecorded. After actual eviction, read-through may
record a new response under the same request key with a new absolute deadline.
NativeCache's disk-recency eviction is separate from freshness. Frozen eval
consumers must use strict `replay`; missing/expired recordings fail with zero
provider calls. Deliberate new corpus captures should use a new generation.
This slice is an evictable response cache, not durable frozen-corpus storage.

On ordinary replay, the returned SDK `usage` contains zero current tokens.
Original usage is preserved in `providerMetadata.layercache.historicalUsage`
and the hit event. Metadata also records mode, outcome, source, key, absolute
recording times and `currentProviderCalls`. Fresh/current usage remains current;
no avoided-dollar estimate is invented. Observer exceptions are isolated from
the model result. Use `fresh` for live model-quality, latency, availability,
randomness and cost measurements. Disable other harness caches separately.

## Supported values and failure behavior

Known AI SDK v4 generation options and plain JSON data are supported. Inline
Uint8Array/ArrayBuffer identity, binary generated file data and response Date
metadata use a validated codec. Mutable media URLs and provider file references,
provider-executed tools with hidden observations, URL objects, functions,
unsupported classes, cycles, invalid Unicode/non-finite
numbers and unknown top-level request options are unsupported. Ordinary private
JSON fields named `password` or `secret` are not silently redacted. Explicit
credential-bearing headers/provider options are declined. Provider request
bodies, response bodies and response headers are omitted from recordings;
semantic provider metadata remains supported, with known credential fields
rejected. Keep authentication in provider construction and transport credentials.

Only completed `stop` and `tool-calls` provider generations are admitted.
Declared client function names and JSON tool arguments are checked; empty
arguments follow AI SDK's empty-object convention. Terminal structured JSON
output is parsed and checked against its normalized schema. Schema admission
uses pinned MIT [Ajv 8.17.1](https://github.com/ajv-validator/ajv/tree/9050ba1359fb87cd7c143f3c79513ea7624ea443)
with strict synchronous Draft 7 validation. Asynchronous schemas, unknown
schema keywords, formats, dialects or unresolved external references decline
admission. Pinned BSD-3-Clause `secure-json-parse@4.1.0` rejects the prototype
properties forbidden by the SDK parser rather than accepting a response that
the SDK will reject. No custom schema engine is
implemented. Ajv and its bundled dependencies are confined to the LLM bundle
and listed in the packaged third-party notices.

Provider failures, truncation, preliminary/error provider tool output, malformed
or schema-invalid normalized results, oversize output and aborted provider
generations are not recorded. The middleware runs before the SDK's original
custom/Zod validators, refinements, repair callbacks, tool execution and app
assertions. Those later operations can still reject a recorded provider result;
recording does not prove they succeeded. Their behavior/revision belongs in the
declared harness context, and callers requiring current custom validation or
live acceptance use `fresh` and an unwrapped judge. This first slice cannot
inspect or cache the success of those higher-level operations. Record mode reports unsupported admission
as an error; read-through returns a truthful bypass/unrecorded result. Replay
and record reject streaming before calling the provider. Read-through and fresh
streams delegate unchanged and never record. Streaming replay is future work.

Input identity and result-envelope limits default to 8 MiB and 4 MiB. The
existing NativeCache bounds archives, validates digests and extraction, and
applies a 64 MiB default local archive budget here. The default LLM directory
is `~/.cache/layercache/llm-v1`, separate from native build archives;
`LAYER_CACHE_LLM_DIR` or `cache.cacheDir` can choose its location. Temporary response files are
owner-only and removed after the operation. HTTP deadlines use NativeCache's
configured timeout; cancellation interrupts waits/uploads and is checked
before local publication. An aborted provider generation is never admitted. A remote service can already have committed
bytes when a network cancellation arrives, so cancellation cannot revoke an
already accepted immutable artifact. Cache failures do not become model hits.

Run `pnpm test` for the actual SDK and bounded HTTP fixtures. Existing native CI
also supplies `LAYERCACHE_SETUP_BINARY` to test a concurrent recording and empty
SDK consumer against the real Go artifact endpoint/store. These tests prove
transport and middleware behavior, not paid-provider compatibility, live Phone
E2E acceptance or measured application savings.
