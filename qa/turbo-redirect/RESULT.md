# Stock Turbo and a gateway 302 to presigned storage

Tested 9 November 2026 on Turbo 2.9.14 and 2.10.12, the versions in `qa/client-versions.json`, linux-x64 npm builds.

A gateway 302 works for `GET`. Do not send 3xx on `PUT`, because Turbo reports success and the artifact is lost. Do not redirect `HEAD` or signed artifacts.

The per-version results used local fake servers. The last section checks the implemented gateway on MinIO. Nothing here touched real R2 or a CI runner.

## Setup

`harness.mjs` runs a fake remote cache on `127.0.0.1` and a fake object store on `localhost`, a different host and port. The object store returns 400 for any request carrying `Authorization` and 403 for any request without `X-Amz-Signature`. It returns 403 on `HEAD`, as a GET-signed URL would. It logs every request.

```bash
S=$(mktemp -d)
for v in 2.9.14 2.10.12; do
  mkdir -p "$S/bin/$v"
  (cd "$S/bin/$v" && npm init -y && npm i --no-audit --no-fund --ignore-scripts "turbo@$v")
  ln -sf "$S/bin/$v/node_modules/@turbo/linux-64/bin/turbo" "$S/bin/turbo-$v"
done
node qa/turbo-redirect/harness.mjs "$S"
```

Each scenario clears local Turbo state and then runs this, with a real git workspace:

```bash
TURBO_API=http://127.0.0.1:$PORT TURBO_TEAM=harness TURBO_TOKEN=harness-bearer-token \
  TURBO_DAEMON=false TURBO_UI=false CI=1 NO_COLOR=1 \
  "$S/bin/turbo-$v" run build --summarize --output-logs=full -vv
```

`HEAD` scenarios use `--dry=json`. Each scenario's full stdout and stderr land in `$S/out/`, and `$S/out/results.jsonl` has the request log.

## Results per version

All 44 scenarios matched on exit code, whether the build ran, what was restored, cache status and the request sequence. Only timings and `timeSaved` on the proxied controls differed. Those two values come from real build durations.

| Question | 2.9.14 | 2.10.12 |
|---|---|---|
| User-Agent | `turbo 2.9.14 1.95.0-nightly linux x86_64` | `turbo 2.10.12 1.97.0-nightly linux x86_64` |
| Follows 301, 302, 303, 307, 308 on `GET` | yes | yes |
| Bearer token sent to the second host | no | no |
| 200 from target restores the outputs byte for byte | yes, `FULL TURBO` | yes, `FULL TURBO` |
| HTTPS to HTTPS 302 (local CA via `SSL_CERT_FILE`) | follows, restores | follows, restores |
| Query string on the target URL | unchanged, no `slug` added | unchanged, no `slug` added |
| 3xx on `PUT` | silent loss | silent loss |

Observed request traces, identical on both versions. `+A` means the request carried `Authorization`. "Target" is the object store.

| Scenario | Requests | Task result |
|---|---|---|
| 302, target 200 | `gw GET 302 +A`, `target GET 200` | HIT, no build, seed bytes restored |
| Proxied control | `gw GET 200 +A` | HIT, no build |
| Target 403 (expired URL, XML body) | `gw GET 302 +A`, `target GET 403`, `gw PUT 200 +A` | MISS, rebuild, re-upload, exit 0 |
| Target 404 | same shape as 403 | MISS, rebuild, re-upload, exit 0 |
| Target 500 | `gw GET 302`, `target 500`, `gw GET 302`, `target 500`, `gw PUT` | MISS, rebuild, re-upload, 2.2s added |
| Target hangs, `TURBO_REMOTE_CACHE_TIMEOUT=3` | `gw GET 302`, `target` hung, repeated once, `gw PUT` | MISS, rebuild, re-upload, 10.2s total |
| Target connection refused | `gw GET 302`, `gw PUT` | MISS, rebuild, re-upload, exit 0 |
| Target truncates body | `gw GET 302`, `target GET 200`, `gw PUT` | MISS, rebuild, re-upload, exit 0 |
| Target returns random bytes | `gw GET 302`, `target GET 200`, `gw PUT` | MISS, rebuild, re-upload, exit 0 |
| Target returns a valid tar.zst with altered content | `gw GET 302`, `target GET 200` | HIT, no build, altered file restored, exit 0 |
| Redirect loop | 11 `gw GET 302`, `gw PUT` | MISS, rebuild, re-upload, exit 0 |

Stock Turbo has no fallback to the proxied path. Every failure above ended in a rebuild with exit 0 and no warning in default output. Quiet runs printed the usual lines:

```text
a:build: cache miss, executing c851f66665a22cad
  Tasks:    1 successful, 1 total
 Cached:    0 cached, 1 total
```

It retries 500s and timeouts once, and each retry goes back through the gateway, so a retry gets a fresh URL. It does not retry 403 or 404.

### Metadata headers

Turbo reads `x-artifact-*` from the final response only.

| Scenario | Requests | Result |
|---|---|---|
| `x-artifact-duration` on the 302 only | `gw GET 302`, `target GET 200` | HIT, `timeSaved: 0` |
| `x-artifact-duration: 4242` on the target | `gw GET 302`, `target GET 200` | HIT, `timeSaved: 4242` |
| Signature on, tag on the 302 only | `gw GET 302`, `target GET 200`, `gw PUT` | MISS, rebuild, re-upload, exit 0, no warning |
| Signature on, tag on the target | `gw GET 302`, `target GET 200` | HIT, restored |
| Signature on, tag on the target, altered body | `gw GET 302`, `target GET 200`, `gw PUT` | MISS, rebuild |

I would not expect R2 to send `x-artifact-tag` or `x-artifact-duration` on a presigned `GET`, since custom metadata comes back as `x-amz-meta-*`. I did not test that against R2.

### HEAD

A normal `turbo run` sent no `HEAD` at all on either version. Only `--dry` does.

| Scenario | Requests | Result |
|---|---|---|
| `HEAD` 200 from gateway | `gw HEAD 200 +A` | HIT |
| `HEAD` 302, target accepts `HEAD` | `gw HEAD 302 +A`, `target HEAD 200` | HIT, `timeSaved: 0` |
| `HEAD` 302, target 403 (URL signed for `GET`) | `gw HEAD 302 +A`, `target HEAD 403` | MISS, debug line `failed to check http cache` |

### PUT

The source reading suggested Turbo rejects a 307. It does not. The client sends the body to the gateway, gets the redirect, cannot replay the streamed body, and treats the 3xx as success because `error_for_status` only fails on 4xx and 5xx. The debug log says `uploaded`.

| Scenario | Requests | Result |
|---|---|---|
| `PUT` 301, 302, 307 or 308 | `gw PUT 3xx +A`, target never called | exit 0, no warning, nothing stored |
| `PUT` 303 | `gw PUT 303 +A`, `target GET 200`, body dropped | exit 0, no warning, nothing stored |
| `PUT` 500 control | `gw PUT 500 +A` | exit 0, `WARNING failed to contact remote cache` |

### Preflight

`TURBO_PREFLIGHT=true` makes Turbo send `OPTIONS` and use the returned `Location`. It works for `GET` and `PUT`, and the bytes reach the target without `Authorization` when `Access-Control-Allow-Headers` omits it. Turbo appended `&slug=harness` to the target URL on both versions. The SigV4 canonical query covers every parameter, so that should invalidate a presigned URL unless `slug` is signed in. It also gives the gateway no commit step after an upload. I don't recommend it.

A token saved in Turbo's `config.json` behaved like `TURBO_TOKEN`. It went to the gateway and not to the target. I did not test a Vercel CLI auth file, which hosted CI does not use.

## Recommendation

Build the 302 for `GET`, behind its own kill switch, and try it on one project. The gain is real and the redirect itself is well behaved on both pins. Two costs need a decision first.

Reads through this path lose the gateway's verification. The altered-tar scenario restored as a clean `FULL TURBO` hit. Today `stageVerifiedArtifactRead` catches that, deletes the entry and answers 404. With a redirect a corrupt object stays in place. Going by `putTurbo`, the rebuild's `PUT` cannot repair it either, since cloud `Put` is first-writer-wins. I read that in the code and did not run it. If R2 returns a bad object, that hash rebuilds on every run until something deletes it.

Failures are invisible to the client's user, so the gateway has to detect them. A redirect followed by a `PUT` for the same key from the same run is the signal in every failure trace above. I would use that to stop redirecting for the project and to verify the object from the gateway.

## What would change in `internal/server`

- `getTurbo`: add a branch after the descriptor branch for `!head` requests that match a new gate. `directtransfer.Accepts` won't help, since Turbo sends `Accept: */*`.
- Gate it on a separate `DirectTransferPolicy` field, default off, plus a `User-Agent` of `turbo <version>` at or above the minimum pin. LayerCache's own clients must stay out, because `remote/turbo.go` and the TS clients reject cross-origin redirects.
- New `serveTurboRedirect`, next to `serveDirectTurboRead`: reuse `IssueDirectRead`, write `302`, `Location` and `Cache-Control: no-store`, and fall back to the proxied path on `ErrDirectTransferUnavailable`.
- Keep entries with `entry.Metadata.Tag != ""` proxied. The check belongs in `internal/cloud`, because `releaseReadLease` is unexported and the lease is taken before the entry is known.
- `putTurbo` and the `HEAD` path must never return 3xx. Add regression tests for both.
- Accounting, in `internal/measurement`: `closestTurboObservation` only matches `ResultHit`, so a redirect recorded as `ResultUnknown` becomes an unattributed hit. With `timeSaved: 0`, `positiveTurboMilliseconds` returns nil and the time-saved estimate disappears. Have the gateway record the producer duration on the redirect observation, and let reconciliation use it.

There is no completion call on this path. The read lease expires by its 15-minute TTL, or longer for large objects.

## Check against the implemented gateway

The gated redirect was then run with real Turbo binaries through the real gateway handler, on Postgres and MinIO, so SigV4 was enforced. The trace counts requests at the gateway.

| Step | 2.9.14 and 2.10.12 | Result |
|---|---|---|
| Seed | `GET 404`, `PUT 200` | build, upload |
| Second run | `GET 302` | `FULL TURBO`, no build |
| Object corrupted in storage | `GET 302`, `PUT 409` | rebuild, one `WARNING` about the 409 |
| Next run | `GET 404`, `PUT 200` | proxied read deleted the entry, rebuild, upload |
| Next run | `GET 200` | `FULL TURBO`, proxied |

## Not tested

- Real R2, including its SigV4 validation, HTTP/2 and response headers. MinIO stood in for it.
- Artifacts larger than a few hundred bytes. The test artifact was about 235 bytes, so large-body timeouts are unmeasured.
- Platforms other than linux-x64.
- A real CI run through `action/turbo`.
