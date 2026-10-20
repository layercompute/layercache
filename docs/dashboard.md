# Customer dashboard

The dashboard shows storage, effective cache reuse, transfer activity and costs
for configured projects. The CLI works without a cloud account. The authenticated
[cloud portal](self-serve-cloud.md) uses the same cache views for Personal and
Team projects. Source qualification, CLI release and hosted deployment are
separate outcomes; these additions need a CLI built from the updated source.

## Hosted project pages

The hosted dashboard opens one project at a time. Its Overview starts with
current cached bytes, effective hit rate, estimated build time saved and reported
measurement runs. It remembers the last authorized project in that browser.
Project pages have direct URLs:

```text
/projects/<encoded-project-id>/overview
/projects/<encoded-project-id>/activity?period=7d
/projects/<encoded-project-id>/infrastructure
/projects/<encoded-project-id>/storage
/projects/<encoded-project-id>/settings
/teams/<encoded-team-id>/settings
```

IDs containing slashes are encoded as one URL segment. Reload, back and forward
preserve the selected page. Changing the period fetches only that project's
snapshot and retains navigation, focus, scroll position and the previous report
while the request is pending. A failed update labels the previous report;
session expiry clears private content.

Overview shows reported integration hit rates. Unmetered clients remain unknown
under a compact disclosure, with the full breakdown on Activity. Common artifact
references are opaque, project-scoped hashes, rather than task paths or commands.
Hits follow native work units. BuildKit vertices, Turbo tasks and Actions archive
outcomes do not establish equivalent amounts of avoided work. Reported runs are
measurement invocations, not GitHub workflow counts.

Infrastructure groups authenticated GitHub Actions jobs by repository, run and
attempt. Individual jobs and resource samples expand on demand. Persistent
client installations appear separately from transient CI jobs. The default list
shows fresh clients seen in at least two distinct reported executions. One-off,
stale and unqualified clients stay in a collapsed disclosure. Counts are lower
bounds from retained primary samples in the selected period; start/finish phases
of one execution count once. An installation identity does not establish unique
physical hardware. Unknown compute providers and resource limits remain unknown.

Storage shows a deduplicated artifact-byte breakdown from explicit integration
metadata. A blob referenced by several integrations belongs to Shared and is
counted once. Turbo-protocol archives include Xcode and Expo snapshots; their
purpose cannot be inferred from this transport identity. Registry storage and
separate Bazel engine stores are excluded. Personal/Team/Public visibility and
storage protocol are different dimensions.

Administrators can lower or restore the operating capacity within the project's
assigned allocation. The mutation checks current native access, the previous
limit, committed bytes, retained reservations/staging, metadata and the soft
cleanup target. It persists across restart and never evicts entries. Raising the
assigned allocation needs an operator or storage-plan change. Pruning is a
separate, explicitly confirmed action. An uncertain capacity response requires
a fresh capacity read before retrying.

Savings estimates retain original-timing coverage and confidence. Gross task
durations can overlap; partial native groups cannot claim their entire producer
duration as work avoided. Critical-path estimates require usable graph evidence
and can be negative after cache overhead. CPU-hours and money saved remain
unmeasured until producer resources and prices can be joined to the reused work.
Current runner specifications are not a substitute for that evidence.

Account costs, team membership and project onboarding are on Account & team.
CLI connection instructions are on project Settings. The existing local CLI
dashboard retains its current layout and explicit configuration selection.

Build the CLI normally, then run:

```bash
layercache dashboard --config /path/to/project.json
```

To run a Local cache backend and its dashboard together:

```bash
layercache dashboard --config /path/to/project.json --serve
```

The process owns only the backends it starts and closes them on exit. It refuses
to replace a runtime that is already running. Use the ordinary dashboard command
to inspect an existing daemon. `--serve` does not create a cloud account or expose
an unauthenticated service.

If a remote is configured but has no initial Team capability, `--serve` obtains
that capability in memory before constructing its backends. If first login is
unavailable, startup returns a login/retry instruction before opening any
backend. Configurations without a remote still work offline without an account.

To inspect only the remote backend configured by that project:

```bash
layercache dashboard --config /path/to/project.json \
  --backend https://cache.example.com
```

The URL must match the configuration's existing Team URL. This mode takes one
configuration, does not require a Local daemon and cannot be combined with
`--serve`. Changing the configured remote requires restarting this mode.

Open the complete loopback URL printed by the command. The CLI stays running
until Ctrl-C. A dashboard launched on a remote machine needs a loopback tunnel
to that machine, preserving its dashboard port. Use `--listen 127.0.0.1:7438`
to choose a stable tunnel port. The dashboard intentionally accepts only its
exact loopback host and port; it cannot be exposed through a generic reverse proxy.

To see several projects, repeat `--config`:

```bash
layercache dashboard \
  --config /path/to/first-project.json \
  --config /path/to/second-project.json
```

Configurations must have the `local` role and distinct project identities. The
limit is 32 projects. These are explicit CLI connections, not a directory of all
projects a GitHub account might be allowed to access. Project creation,
memberships and allocations remain in the cloud portal.

For each configuration, start its Local Cache with `layercache start --config
/path/to/project.json`. Configure an existing Team Cache endpoint through
`layercache setup --team-url https://cache.example.com --config
/path/to/project.json --non-interactive`, then sign in:

```bash
layercache login --github-cli --config /path/to/project.json
```

A team administrator must already have provisioned the project and granted
access. The dashboard uses the existing GitHub/credential-manager refresh flow
in memory and reloads configuration on refresh, so a subsequent CLI login is
picked up. Refreshed project capabilities stay in memory; rotated GitHub sessions
remain in the protected credential manager. An unexpired capability remains
usable when proactive refresh fails, subject to the Team Cache's authorization.

## Display contract

- Current logical artifact bytes, configured capacity, artifact and entry counts,
  backend and eviction policy come from each selected runtime's `/v1/status`.
- Last-24-hours or last-seven-days hit outcomes, integration counts, and net
  estimated build time saved come from that runtime's `/v1/reports`.
- Local and Team observations stay separate. They can overlap, so the dashboard
  does not sum them into a team-wide savings number. Team reports are the events
  observed/reconciled at that Team Cache, not a complete history of every
  engineer's local builds. Native artifacts do not yet have a dedicated native
  reporting integration here. Turbo, Actions and BuildKit reporting does not
  establish Gradle, Nx, Bazel or Xcode task coverage.
- Effective hit rate is hits divided by eligible reported outcomes. Source
  attribution shows observed Local, private remote and trusted Public hits and
  downloaded bytes. Upload totals include cache publications; their destination
  cannot be inferred from the restore-source rows. Transfer phase durations can
  overlap and are not bandwidth.
- Local describes storage on a machine, not Personal ownership. The hosted
  membership authority distinguishes Personal and Team caches. Standalone
  private remote ownership can remain unknown. Public Cache accepts publication
  only from controlled Public Builds, not arbitrary customer uploads.
- Missing evidence is unknown. No eligible outcomes displays a dash for hit
  rate; an outage does not display an empty cache. Status can remain visible when
  reporting is unavailable. Failed dashboard refreshes clear the old snapshot.
- Artifact capacity is not total host disk usage or the separate OCI registry
  capacity. Savings are estimates with evidence coverage and confidence, not
  measured CPU hours or money. Stored bytes cannot currently be split reliably
  by Personal/Team/Public provenance; source hits are not a storage breakdown.

## Pruning

CLI links are read-only by default. To permit administrative maintenance for
the selected connections, start the dashboard with `--allow-prune`. That link
then grants maintenance access as well as read access; share it accordingly.
The cache still checks the configured credential's current administrative
authority. The hosted portal requires current project administrator membership,
an authenticated browser session and its normal same-origin CSRF checks.
Older cache backends without the maintenance capability field remain read-only
in this dashboard until they are upgraded.

Choose a target logical artifact size and confirm the operation. Pruning reuses
the existing cache GC policy and preserves pins, retained claims and active
reads. An impossible target can fail; an embedded cache may already have removed
eligible entries before that failure. The dashboard refreshes after either
result and never retries a mutation automatically. Public Cache maintenance is
not offered through customer views.

Removed logical cache bytes are not measured filesystem savings or an immediate
R2 invoice reduction. Cloud object deletion follows its grace period and read
leases. The dashboard cannot delete source, arbitrary workspace directories or
a separate native engine store. In particular, configured Bazel engine storage
is not part of the artifact store's usage or pruning totals.

## Costs

Costs are unknown until an operator supplies a ledger. The dashboard does not
have provider invoice access or a time-integrated storage meter. It does not
calculate accrued spend by multiplying today's stored bytes by a monthly rate.

For CLI use, pass `--cost-file /path/to/costs.json`. The hosted service accepts
`dashboardCostsFile` in its protected `serve-cloud` configuration. Keep the
ledger outside Git with owner-only permissions. It is read again for each
snapshot; an invalid file displays unavailable costs instead of stale totals.

The following synthetic shape records a project estimate in USD minor units:

```json
{
  "version": 1,
  "entries": [{
    "scope": "project",
    "id": "github.com/example/demo",
    "currency": "USD",
    "periodStart": "2026-10-01T00:00:00Z",
    "periodEnd": "2026-11-01T00:00:00Z",
    "basis": "estimate",
    "complete": false,
    "items": [{"name": "Storage estimate", "amountMinor": 250}]
  }]
}
```

Use `scope: "team"` and the hosted team's ID for a tenant total. Team records
are independent of project records, so shared overhead can be included without
summing duplicate project or Local/remote observations. Each scope and ID has
one selected period. `basis: "recorded"` labels operator-reported charges;
neither basis claims provider verification. `complete: false` shows a known
subtotal and an unknown total. Set it true only when the record covers the
whole stated period and scope. Amounts must be nonnegative exact minor-unit
integers. Currency formatting follows that currency's minor-unit precision.

Hosted team summaries show project count, allocated capacity and the selected
team cost record. Allocated capacity is not current usage or billed storage.
The selected report window and a ledger's cost period are shown separately.

The browser receives only a temporary dashboard credential and selected status
and report fields. Runtime/Team credentials, config paths, and internal storage
details stay in the CLI. The link's fragment is removed after page load, the
credential stays in page memory, and a reload requires reopening or pasting the
complete CLI link, including into the same tab. The link grants access to all
projects selected for that process; keep it private. Stopping the CLI invalidates
it. Upstream redirects are not
followed, and the browser cannot supply upstream endpoints or project selectors.

## Next hosted slices

1. Launch and qualify the implemented service with real GitHub OAuth, a bounded
   deployment, an installable CLI, and an independent fresh-runner cache restore.
2. Enroll CLI machines with explicit team/project authorization, revocable machine
   identities, bounded heartbeats and usage reports. Show last-seen and stale
   state, then add approved cache-policy management. A CLI configuration or daemon
   process ID is not a cloud machine identity.
3. Provision private cloud runners with job leases, cancellation, limits,
   credentials, cleanup, and usage accounting. Public Builds retain their distinct
   controlled-publication contract; they are not the private runner scheduler.

The existing cache data path and artifact identity remain reusable across these
slices. The hosted dashboard consumes authorized project projections and checks
browser sessions server-side without exposing cache credentials.

## Verify command adoption

A dashboard project and a cloud-only CLI configuration do not redirect an
existing command. Launch that command through the existing authenticated client:

```bash
mise exec -- layercache run --config /path/to/project.json -- pnpm check
```

Use the repository's pinned tools and a compatibility profile matching those
actual tools. A profile named for CI does not establish that the developer
machine uses the same Node, package manager, operating system or architecture.
Resolve discrepancies before publishing. Do not replace native Gradle, Expo or
Xcode profiles with a generic JavaScript profile.

Trace each local task, pre-push hook, PR job and main publisher separately. A
bare `pnpm` command can still select another provider or only its workspace
cache. In CI, use an immutable action revision, explicit read-only policy for
eligible PR consumers and the server's trusted publisher rules. Keep tasks
declared noncacheable fresh. Inspect native task summaries together with server
transfers before claiming shared reuse. Storage growth alone establishes no
successful restore or complete reporting coverage.

The Expo provider is a separate native client. Its local archive and extraction
settings do not inherit the Go CLI's `remoteOnly` option, and its selected
platform/toolchain identity needs matching scoped credentials. Likewise, GitHub
compiler snapshots and provider-managed builds are separate caches. Required
pipeline handoffs keep their retained-artifact fallback until their publication
and consumer contract qualifies.

## Development

The Go binary embeds the dashboard. `dashboard/cache-view.ts` provides shared
cache and cost rendering for the CLI and cloud portal. Edit the TypeScript and HTML/CSS under
`internal/dashboard/assets`, then run `pnpm build`. The generated `app.js` is
tracked and CI checks bundle drift. `pnpm typecheck` checks the browser code;
`go test ./internal/dashboard ./internal/cli -run Dashboard` checks the connection
and browser-serving contract.

Run the [browser QA harness](../qa/dashboard/README.md) with `pnpm test:dashboard`
and a built `LAYERCACHE_BIN`. CI installs the pinned Playwright Chromium and
executes the same fixtures and assertions. See the [QA results](../qa/dashboard/RESULTS.md)
for independent review findings, fixes, and verification limits.
