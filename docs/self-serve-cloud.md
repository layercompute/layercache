# Self-serve cloud

The hosted beta lets an approved GitHub user create a team, invite teammates, add repository projects, inspect cache storage and activity, and connect an existing CLI. Other signed-in users can accept invitations and use their teams. Projects reuse the existing Team Cache protocols and isolate artifacts with distinct project identities and signing keys.

The [customer dashboard](dashboard.md) shares cache rendering with the local CLI.
It shows authoritative Personal/Team scope, reported source usage, transfer
phases, project storage and administrator-confirmed pruning. Team summaries
separate allocated capacity from current usage and project observations.
An optional `dashboardCostsFile` in the protected cloud configuration supplies
bounded operator cost records for teams and projects. Missing costs remain
unknown; provider invoice reconciliation and full native metering are future
work. This implementation does not activate public signup or billing.

## Existing projects and operator password sign-in

An operator can adopt existing PostgreSQL/S3 projects through protected
`operatorProjects` configuration rather than creating replacement identities.
Each entry supplies a protected `projectConfigFile`, display `name`, original
numeric `repositoryId`, verified GitHub `owner` with `id` and `login`, and an
explicit `teamId` and `teamName`. Set `alias` to retain an existing container
registry namespace. Imported projects keep their signing keys, native cache
keys, backend, quota and current SQL membership authority. Restart rejects
missing projects or changed immutable bindings and does not restore revoked
memberships. Operator-managed teams expose these management limits in the UI.

For a private operator rollout, `passwordAccountsFile` enables email/password
sign-in without a GitHub OAuth app. The protected file contains an `accounts`
array with canonical `email`, verified owner `user` identity, a cost-12 bcrypt
`passwordHash`, and `mustChangePassword`. It must be an owner-only regular file
inside an owner-only directory, owned by the service account. Keep it outside
Git on persistent storage writable by that single portal process. Accounts must
match imported project owners. Initial credentials require password change
before workspace access. Passwords are 12 to 72 UTF-8 bytes; the service never
stores their plaintext.

Password sign-in reuses encrypted, expiring portal sessions and same-origin
CSRF protection. Rotation requires the current password and persists the new
hash atomically; old sessions stop working. If a response is lost after the
change, try signing in with the new password. Removing an account or replacing
its hash revokes its sessions. Operator recovery updates the protected file;
public signup, email delivery and password-reset emails are not provided.
Bounded login limits use the immediate peer address and ignore forwarded IP
headers. Size a broader public rollout separately from this private mode.

Password sessions manage the adopted dashboard, not GitHub repositories or
team enrollment. Native CLI and CI access still use real GitHub credentials
and the cache's current authority. In password-only mode, connect using an
existing GitHub CLI sign-in:

```sh
layercache connect --cloud https://cloud.example.com \
  --project 'github.com/example/demo' --github-cli --remote-only
```

`--remote-only` writes a client configuration without creating a Local artifact
store. Use that configuration with `layercache run`; a Local daemon is not
needed. Ordinary standalone and managed connections retain Local operation
unless this option is selected. Browser passwords do not become build-cache
credentials. GitHub device login needs a configured OAuth app.

OAuth remains available when its client ID and protected secret are configured
together. Optional `registryUrl`, `registryUsername` and protected
`registryPasswordFile` reuse the existing authenticated project gateway's OCI
proxy. The native registry needs its own storage and retention qualification.
See the [migration procedure](cloud-migration.md) before retiring old storage.

## Standalone and managed service

The open-source CLI and daemon work without a Layer Cache account, with local
storage or one user-managed remote cache host. Remote access still needs the
operator's authentication and network controls.

The managed service requires signup and a scoped token for Team collaboration,
multiple enrolled cache hosts and Layer Cache-hosted storage. An account can
bring its own hosts, choose hosted storage, or combine them. Personal, Team and
trusted-publisher Public visibility remain separate from the hosting choice.
The CLI connects to the cloud service with that authority; the account controls
which enrolled hosts or hosted capacity supply its projects' cache storage.

Existing standalone private-cache APIs and development fixtures do not establish
a deployed signup service or managed host enrollment. The workflow below is the
managed service's intended and locally tested onboarding path; live deployment
and multi-host coordination retain their separate qualification gates.

## Engineer workflow

1. Open the service origin and continue with GitHub.
2. If the operator approved your GitHub user ID as a beta creator, create a team, then add a project using `owner/repository`. You must administer that GitHub repository. Other users can join through an invitation. This prevents registering another repository to obtain its GitHub Actions identity.
3. Copy the project's CLI command. With an existing GitHub CLI sign-in:

   ```sh
   layercache connect --cloud https://cloud.example.com --project PROJECT_ID --github-cli
   ```

   Omit `--github-cli` to use GitHub's device authorization flow. The service's OAuth app must have device flow enabled. The CLI stores that session through its existing protected credential store.
4. Run the `layercache start --config ...` command printed by `connect`. Use the same config for existing `run`, native integration, and `dashboard` commands. Connection alone does not launch a background daemon or modify repository files.
5. Invite a GitHub username as reader, writer, or administrator. The invitation appears when that user signs in; they accept it explicitly. Invitations expire after seven days. No email is sent.

`connect --list` prints accessible teams/projects. Use `--team` and `--project` IDs or unique names to disambiguate. Each connection gets a fresh Local Cache directory and a configuration under the user's configuration directory, namespaced by cloud and project. Existing files are never overwritten. `--config` chooses a new path; use the existing `login` command to refresh an existing connection.

## Roles and identity

| Role | Restore | Publish | Manage team and projects |
| --- | --- | --- | --- |
| Reader | Yes | No | No |
| Writer | Yes | Yes | No |
| Administrator | Yes | Yes | Yes |

Team roles apply to every project in that team. Membership binds to GitHub's stable numeric user ID, including invitations; renaming an account or recycling its username does not transfer access. Cache requests check current membership as well as the signed token's capabilities. Downgrading a writer stops further uploads with their existing token. Removing a member also invalidates outstanding invitations for that member. The last administrator cannot be removed or demoted.

Approved beta creators can also choose **Create Personal Cache**. Its projects
use the same CLI connection and native cache protocols with owner-only access.
The personal collection rejects invitations and membership changes, and team
members cannot access its projects even when the repository matches. Personal
CI additionally requires the owner's signed GitHub actor ID. One personal
collection counts toward the existing per-user and total collection limits;
its projects use the same project and storage caps. See the
[three-tier contract](cache-tiers.md).

GitHub Actions OIDC remains scoped to the repository configured for the project. Previously issued short-lived signed artifact download URLs retain their existing expiry; removing a member does not recall bytes already downloaded.

## Operator setup

Run one service instance behind HTTPS. The process holds exclusive project runtime locks; PostgreSQL support does not make this an active/active service. Existing operator-managed cache deployments can continue separately.

Create a GitHub OAuth app with homepage `https://YOUR_HOST`, callback `https://YOUR_HOST/auth/callback`, and device flow enabled. The browser uses authorization code flow with state and S256 PKCE. It requests `read:user repo` to verify administrator permissions on private repositories. GitHub documents these flows in [Authorizing OAuth apps](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps). A GitHub App with narrower installation permissions is future work.

Use a dedicated physically bounded storage filesystem, following the existing [aggregate storage budget](aggregate-cache-budget.md). The mounted pool must contain `.layercache-pool` with `layercache-pool-v1`; its filesystem size cannot exceed `storagePool.maxBytes`. Put the service data directory inside that pool. Do not point a new service at an existing cache's data directory.

Create the service account and configuration directory first. Run the following preparation as the service account (`layercache` in the example unit), with write access to `/etc/layercache-cloud` and the mounted pool. All protected files must be owned by that same account; root-owned files are rejected when the service runs as `layercache`. Prepare an owner-only template with the CLI:

```sh
umask 077
layercache setup --non-interactive --config /etc/layercache-cloud/project-template.json \
  --role team --project cloud-template \
  --data-dir /srv/layercache-cloud/template --max-size 1073741824 \
  --min-free-bytes 5368709120
openssl rand 32 > /etc/layercache-cloud/session-key
```

The template supplies each project's quota and cache options. Project ID, repository, signing secret, data directory, and membership come from the cloud service. Optional existing `cloudPostgresUrl`/S3 template settings enable the established PostgreSQL/object-storage cache backend. Without those settings cache metadata and artifacts live in the bounded filesystem. Team/session metadata uses SQLite unless `postgresUrlFile` supplies a dedicated PostgreSQL database connection. Keep database and object-storage capacity bounded separately when they are external to this filesystem.

The Local Cache engine preserves at least 5 GiB free on its filesystem even when `--min-free-bytes` is set lower. On the example 8 GiB pool, this leaves roughly 3 GiB for cache data and metadata; size the initial cohort within that budget.

Store the OAuth secret in `/etc/layercache-cloud/github-client-secret` without printing it. Config, template, and secret files must have owner-only permissions. Copy [the example config](../deploy/cloud/config.example.json) and replace the hostname, client ID, paths, and limits.

Set `teamCreatorIds` to one or more canonical decimal GitHub user IDs as JSON strings, for example `["12345678"]`. Get the intended creator's stable numeric ID from GitHub's authenticated `GET /user` response; GitHub usernames may change. The service rejects missing, empty, duplicate, nonnumeric, zero, and noncanonical IDs at startup. Only listed IDs may create teams, regardless of browser UI state. Everyone else may still sign in, accept invitations, and use existing teams. Updating the list requires editing the protected config and restarting the service. Keep admission limited while team capacity and billing are manual.

Then run:

```sh
layercache serve-cloud --config /etc/layercache-cloud/config.json
```

The example [systemd unit](../deploy/cloud/layercache-cloud.service) listens on loopback port 7440. Configure the HTTPS proxy to preserve the exact public Host. Host mismatch is rejected; forwarded host headers do not override it. Probe `/healthz`, then verify real GitHub sign-in, team/project creation, a second user's invitation, CLI connection, upload/restore, access removal, and restart persistence before declaring the service live. Do not expose the QA fake GitHub provider.

Back up the metadata database, session encryption key, and cache storage together. The session key encrypts GitHub browser tokens at rest; changing it makes existing encrypted tokens unusable. Browser sessions last up to 12 hours. When GitHub reports an access-token lifetime, the session ends at least 60 seconds before that lifetime. The deadline starts before the token exchange, and time spent verifying identity or saving the session reduces the cookie's remaining lifetime. Users sign in again when it expires. Keep the key stable across restarts and protect database backups as credentials. Sign out removes the server-side session. OAuth state is single-use and expires after 10 minutes.

Each user can have ten browser sessions. At the limit, a new login keeps the nine existing sessions with the latest expiry and replaces the existing session nearest to expiry. This policy uses remaining lifetime, so the session removed need not be the oldest login.

The service bounds creation to 128 teams total, five created teams per user, ten projects per team, 128 projects total, and 100 pending invitations per team. Capacity conflicts fail explicitly. This first slice has no billing or automated quota purchase flow.

## Delivery boundaries

Included: browser GitHub login, durable teams/projects, invitations and roles, CLI onboarding, Team Cache storage/activity views, Turbo/native/Actions cache HTTP protocols. Reports show recorded cache activity; they do not fabricate savings or build records.

Not included: remote machine enrollment/control, hosted runners, billing, per-project membership overrides, project/team deletion, or a managed BuildKit registry. The first deployment requires an operator-owned OAuth app, hostname, bounded storage, and optional database/object storage credentials. The existing Phone/Parle services are not migrated by this command.

## Validation

`go test ./internal/portal ./internal/cli ./internal/server` covers durable storage, OAuth/CSRF, repository authorization, two-project byte isolation, role changes and access revocation, restart behavior, and protected CLI configuration. The portal store also supports optional disposable PostgreSQL validation; see the QA instructions. `pnpm test:cloud` exercises the real service and browser against a local fake GitHub provider. Chromium, Firefox and WebKit run the same scenario; WebKit is not a claim of testing native Safari hardware.
