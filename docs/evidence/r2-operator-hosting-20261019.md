# Private R2 hosting rollout

The operator endpoint is live at `https://r2-cache.services.ziahamza.com`.
Acceptance completed October 19, 2026 at 21:50 UTC. The
[public summary](r2-operator-hosting-20261019.json) records source pins, resource
bounds and four acceptance phases. Private helper, local image and individual
receipt fingerprints stay in protected operator evidence.

Two explicitly provisioned private projects use separate R2 buckets, database
roles, databases and signing identities. Each has a 5 GiB committed quota and
1 GiB soft target. API and PostgreSQL remain on the existing cloud host; artifact
bytes are remote. A separate 12 GiB filesystem bounds local SQL, control state
and verification staging, with 5 GiB free reserve and an 8 GiB host reserve.
Protected credentials stay outside Git and clients receive scoped capabilities.

Runtime source `08d3df8cdff7ce859378109226c13a629ef52296` is byte-identical in
`cmd`, `internal`, `Dockerfile`, `go.mod` and `go.sum` to reviewed main
`d4e53ea8414126e2d9ee2017da90a68abab44212`, merged in
[PR #61](https://github.com/ziahamza/layercache/pull/61) with all fifteen checks
green. The deployed image ID is recorded in protected operator evidence. The
independently reviewed [host configuration](https://github.com/ziahamza/config/pull/29) keeps
the API, database, mount and ingress lifecycle explicit.

Real human GitHub authentication succeeded for both projects in every phase.
Twelve synthetic payloads cover Turbo, Gradle and Nx HTTP at 4 KiB and 8 MiB.
Complete size, SHA256 and response digest matched on all 48 verified reads.
Changed keys missed; anonymous access, foreign project/profile selectors and
read-only publication were denied. The initial phase made twelve successful
publications and six intentionally denied PUT attempts. Each later phase used
the same private canary plan and sent zero native PUTs:

1. Initial private publication and reads.
2. Fresh reads after restarting only the new API.
3. Fresh reads after restarting only the new PostgreSQL instance.
4. Fresh reads through the public origin with trusted TLS.

All probes originated on the service host. These are deployed protocol and
persistence checks, not real native build-client archives, an external runner
test or application speedup evidence. Readers were operator-minted with the
production signer. Subsequent [external native Turbo qualification](r2-native-oidc-20261019.md)
adds real GitHub Actions OIDC and fresh-runner native restores, including after
an API-only restart. The
[actual R2 source qualification](r2-private-source-20261019.md) records retained
publication and bounded transfer timings independently.

The rollout preserved all 21 existing service configurations and added only the
reviewed two-service include. Before reload, current ingress files matched the
active configuration. Only the reviewed new route was added; existing cache
endpoints, storage and Local daemons stayed intact. Ordinary operator Compose
validation passes, and the new service and mount are enabled for restart.

This endpoint provides operator-managed private caches. Named managed plans are
implemented and source-qualified; signup, billing and entitlement migration are
not activated here. Existing cache namespaces have not moved. Offsite SQL
recovery and a fresh external client remain separate gates. Required retained
artifacts are disabled on this endpoint, and required application transfers
continue using GitHub artifacts until their own acceptance passes.
