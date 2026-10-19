# External native Turbo reuse with R2 and GitHub OIDC

Qualification completed October 19, 2026 UTC. A default-branch publisher and two
fresh read-only GitHub-hosted Linux x64 runners used the
[operator R2 endpoint](r2-operator-hosting-20261019.md). Both readers restored the
publisher's two native task results with zero task executions, zero local-cache
hits and zero artifact uploads during baseline restoration. The second reader ran after an API-only restart.
This is a small synthetic fixture, not application or economic acceptance.

## Provenance and authority

| Role | Producing run | Source revision | Complete job interval, UTC |
| --- | --- | --- | --- |
| Publisher, actual default ref | [39339723265](https://github.com/ziahamza/layercache/actions/runs/39339723265) | `55f5d0efb1e2aaafbe7c1f4e0661f77ab6e58569` | 23:08:59 to 23:09:45 |
| Independent reader, actual non-default ref | [39340596383](https://github.com/ziahamza/layercache/actions/runs/39340596383) | `172cdceba9c384d8befb00c698a886c0c84eb302` | 23:17:05 to 23:17:42 |
| Fresh reader after API restart, same non-default ref | [39340977602](https://github.com/ziahamza/layercache/actions/runs/39340977602) | `172cdceba9c384d8befb00c698a886c0c84eb302` | 23:20:44 to 23:21:25 |

All three runs use workflow dispatch, attempt 1, and real GitHub Actions OIDC.
The production service issues write authority to the default-ref publisher and
read authority to the actual non-default-ref consumers. Both readers receive
HTTP 401 for an explicit artifact PUT, while their authenticated archive GETs
succeed. Decoding capability claims alone is not treated as signature proof.

The source revisions differ. The nine-file native fixture is pinned to
`170582ba6caccb5f5dcdf60171d8d65196075993`, with manifest SHA256
`5ad4043e643204cef74cf9254189e5576c7cb0f90ee0b3cbc0ef6c8c90485ca8`.
Node 24.13.0, Turbo 2.10.12 and pnpm 10.34.5 are fixed. The
[native action](../../action/turbo/action.yml) is also pinned to
`170582ba6caccb5f5dcdf60171d8d65196075993`. The compatibility identifies that
runtime/toolchain; an experiment sample label is a declared task input shared only
by this experiment. Neither the source commit nor paths enter compatibility.
Every command explicitly disables filesystem task caching.

## Native outcomes and controls

Both independent readers match these publisher keys and complete output bytes:

| Task | Native key | Output bytes | Output SHA256 |
| --- | --- | ---: | --- |
| Build | `267da277167fec52` | 128 | `4c71fe79519494762daee48b3d3a590993b3986c2e94e083adf3d7f2afce7460` |
| Test | `24d158f2466f2078` | 127 | `a2c255f525d8bd128942b6da1e38e1ebd78c0dde9d3aa5919b833f523fff0816` |

The publisher executes both tasks and receives HTTP 200 for each artifact PUT.
Each reader reports two remote HITs and verifies the SHA256 response digest of
both complete cached archives. The output hashes above describe the restored
JSON files, not the compressed archives.

Each accepted run also exercises these controls:

| Control | Expected and observed result |
| --- | --- |
| Changed source | Both native keys change; both tasks execute. |
| Changed declared environment | Both native keys change; both tasks execute. |
| Changed compatibility | Native keys stay the same; the other namespace misses and both tasks execute. |
| Failed test, twice | Same failed-task key, exit 7 and one execution each time; zero artifact PUTs. |

Each reader additionally requires the exact expected native keys' artifact GETs
to return HTTP 404 for every miss: two per successful control and one per failed
test. The original publisher summary predates these added miss counters and
has not been modified or backfilled.

Native optional event telemetry requires write authority on this server.
Each reader therefore records six completed `POST /v8/artifacts/events` 401s,
one per scenario, as `deniedOptionalEvents`. This exception is limited to that
exact method, route and reader role. Artifact or status-probe 401s, producer
telemetry denials, redirects, truncated/oversized bodies, upstream errors and
observation timeouts remain qualification failures. The explicit denied
artifact PUT is separate from scenario PUT counts. The action's reporting
post hook is also separate from native artifact publication.

At 23:20:13 to 23:20:14 UTC, only the new R2 API container restarted. Its image and
container identity stayed the same. The other 22 running containers, including
PostgreSQL, retained their process/start state. Loopback health and trusted
public HTTPS returned normally before the final fresh reader. This establishes
native restart persistence; offsite metadata recovery and high availability
remain separate gates.

## Measurement boundaries

| Phase | Publisher | First reader | Reader after API restart |
| --- | ---: | ---: | ---: |
| Baseline native command | 2.831 s | 1.116 s | 0.988 s |
| Separate response observation wait | <0.001 s | 0.171 s | 0.152 s |
| Complete qualification job, including setup and all controls | 46 s | 37 s | 41 s |
| Baseline request-body bytes uploaded / downloaded | 943 / 71 | 253 / 738 | 253 / 738 |
| Baseline summed request completion time | 4.605 s | 2.358 s | 1.967 s |

The streaming loopback relay measures requests to the public HTTPS origin.
Body counts include metadata and error responses; they exclude TLS overhead
and backend R2 traffic. Request durations overlap and cannot be added to native
command time or interpreted as bandwidth. Observation time starts after the
native process exits and waits up to five seconds for real response completion.
These tiny workloads and independent VMs establish no causal speedup, application
benefit, signed-build result or monthly/invoice saving.

The earlier [producer attempt](https://github.com/ziahamza/layercache/actions/runs/39337648180)
and [reader attempt](https://github.com/ziahamza/layercache/actions/runs/39339853544)
remain failed qualifications. Local real-client regressions reproduced delayed
telemetry completion and the read-only telemetry-policy mismatch. Their
receipts are preserved; the accepted publisher was not republished for either
successful reader. No server authority was broadened to satisfy the fixture.

The operator verified complete GitHub artifact ZIP digests, exact workflow,
source, ref and attempt, then matched native keys and full outputs independently.
Only the allowlisted summaries are public workflow artifacts. Operational
journals, protected credential files and provider resource identifiers remain
outside Git. The [fixture instructions](../../qa/r2-native-oidc/README.md) describe the
source checks and deliberate operator opt-in.

Required application handoffs still use GitHub artifacts. Actual workload
adoption, supported recovery, namespace migration, OAuth signup and managed
billing remain outstanding in [TODOs.md](../../TODOs.md).
