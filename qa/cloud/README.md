# Self-serve cloud browser QA

This suite launches the real `serve-cloud` CLI against isolated configuration and
storage. A local Node HTTP server acts as GitHub for authorization redirects,
PKCE token exchange, stable user identity, and repository administration checks.
It validates the configured OAuth client ID, exact callback, and requested scope;
the real cloud rejects altered state, a mismatched PKCE challenge, callbacks sent
to another browser, and callback replay after sign-out.
The fake provider exists only in this QA fixture; no production authentication
bypass is added.

```sh
mise exec -- pnpm install --frozen-lockfile
mise exec -- pnpm exec playwright install chromium firefox webkit
mise exec -- pnpm build
mise exec -- go build -o /tmp/layercache-cloud-qa ./cmd/layercache
LAYERCACHE_BIN=/tmp/layercache-cloud-qa DASHBOARD_BROWSER=chromium mise exec -- node qa/cloud/run.ts
LAYERCACHE_BIN=/tmp/layercache-cloud-qa DASHBOARD_BROWSER=firefox mise exec -- node qa/cloud/run.ts
LAYERCACHE_BIN=/tmp/layercache-cloud-qa DASHBOARD_BROWSER=webkit mise exec -- node qa/cloud/run.ts
```

`QA_OUTPUT` optionally selects a directory for screenshots and `results.json`.
The fixture's private temporary directory and browser output paths are printed.
Each run uses fresh data and two independent browser contexts. Processes stop
when the suite completes or is interrupted. Test assertions exit nonzero on
failure. `CHROMIUM_PATH` can select an explicit Chromium binary; other engines
use their pinned Playwright installations.

The flow covers real OAuth sign-in, beta creator admission denial at both UI and API, empty-workspace onboarding, team creation,
repository administration denial, project provisioning, an actual cache upload,
an actual `layercache connect --github-cli` run with isolated fake `gh`, CLI instructions, targeted invitations, reader/writer roles, session reload,
report periods, four viewport sizes, last-admin protection, member removal,
logout, and expired-session recovery.
OAuth failure checks use disposable browser contexts and verify that rejected
callbacks do not create sessions. A fresh sign-in after a tampered state also
verifies recovery without restarting the service.

The storage-pool fixture marks a private directory on the host filesystem and
sets its configured ceiling to that filesystem's reported size. This exercises
pool-aware provisioning and cache writes without requiring privileged mounts; it
does **not** prove a physically bounded production volume or disk exhaustion.
WebKit exercises Safari's engine, not the native Safari application on macOS/iOS.

## Lifecycle observations

The cloud runner also exercises the shipped portal bundle against synthetic
same-origin APIs in `lifecycle.ts`. This checks queued-fetch cancellation,
partial inventory, stale responses and simulated persisted-page restoration.
Simulated page-transition events do not establish browser BFCache eligibility.

The observer can deliberately deliver its console records late:

```sh
DASHBOARD_BROWSER=webkit QA_LIFECYCLE_OBSERVATION_DELAY_MS=1000 node qa/cloud/lifecycle.ts
```

The optional delay accepts integer milliseconds from 0 to 1000. It changes only
the fixture's observation delivery. HTTP arrival and rendered DOM do not order
Playwright console callbacks, so the fixture awaits the exact expected records
before reading document IDs or checking counts. Cancellation counts and the
zero-browser-error assertions remain strict.

## Two-user Turbo restore

The browser suite checks real CLI connection and a direct artifact upload. The
separate Turbo acceptance run proves that a cache-aware build crosses user and
Workspace boundaries. Install the pinned Turbo client, then run this against a
built CLI or a published installed binary:

```sh
pnpm --dir qa/fixtures/turbo install --frozen-lockfile
LAYERCACHE_BIN=/absolute/path/to/layercache \
TURBO_BIN="$PWD/qa/fixtures/turbo/node_modules/.bin/turbo" \
node qa/cloud/turbo-restore.ts
```

Run `node --test qa/cloud/turbo-restore-interruption.test.ts` with the same
environment to verify cancellation. It launches the real cloud and Local Cache,
holds the build command open, sends SIGINT and SIGTERM to the harness, and
requires every owned process to stop before the harness exits nonzero. CI runs
this regression before the restore acceptance test.

It signs in two emulated GitHub users to an isolated loopback cloud, creates a
team and repository project, invites the second user as a reader, and connects
two separate CLI configurations. The pinned Turbo client builds in Alice's
fresh Git clone, then Bob restores from another clone after Alice's Local Cache
daemon stops. The assertions require one actual task execution, native Turbo
`MISS` then `REMOTE HIT`, positive uploaded/downloaded byte counts, a Team Cache
source in the Layer Cache report, identical output SHA-256, and denial of Bob's
previous capability after removal. Evidence is written to `turbo-restore.json`
in `QA_OUTPUT` or a fresh temporary evidence directory. The CI performance job
runs this against the candidate binary and preserves that JSON.

The GitHub provider and cloud hostname are emulated. This test does not replace
an external HTTPS deployment, GitHub's real OAuth service, or an independent
customer machine on the public network.

## PostgreSQL contract

Portal PostgreSQL CI runs the store contract against a pinned, disposable
PostgreSQL service. Against an isolated local database named
`layercache_portal_test`, run:

```sh
LAYERCACHE_PORTAL_TEST_POSTGRES_URL='postgres://USER:PASSWORD@127.0.0.1:PORT/layercache_portal_test?sslmode=disable' mise exec -- go test -race ./internal/portal -run '^TestStore' -count=3
```

The fixture rejects non-loopback/non-test databases and creates/drops a random
schema per test. Coverage includes durable identity/membership, invitation
replay/revocation, concurrent last-administrator removal, global project capacity,
session limits, and live capability revocation. Three PostgreSQL race runs and
SQLite race tests passed locally on 2026-09-22.
