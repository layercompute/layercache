# Layer Cache

Layer Cache describes how engineers reuse build artifacts across machines and trust boundaries.

It also describes reuse of recorded model responses and successful agent actions
under complete input identities, while current runtime checks remain distinct.

## Language

**Standalone cache**:
An open-source cache operated without a Layer Cache account, on a local machine or one user-managed remote host.
_Avoid_: Free Team Cache, account-required Local Cache

**Cloud account**:
A signed-up identity that authorizes managed collaboration, cache-host coordination and hosted storage through scoped credentials.
_Avoid_: Local daemon identity, storage tier

**Cache host**:
A machine supplying cache storage. Managed cache hosts are enrolled under a Cloud account and retain their projects' access boundaries.
_Avoid_: Team when describing a machine, build runner when describing storage

**Hosted storage**:
Cache capacity operated by Layer Cache for an authorized Cloud account. Hosting is separate from Personal, Team and Public visibility.
_Avoid_: Cloud visibility tier, Public Cache when describing hosting

**Local Cache**:
A bounded cache on one machine that stores reusable artifacts from authorized visibility tiers.
_Avoid_: Local Store, local tier

**Personal Cache**:
A private cache owned by one stable user identity and reusable across that user's machines and authorized CI.
_Avoid_: Local Cache when describing access scope, personal team

**Team**:
A group of engineers sharing project access through administrator, writer, and reader roles.
An organization can use one or more teams.
_Avoid_: tenant

**Project**:
A repository-backed boundary for cache artifacts and build activity within Personal Cache or Team Cache. The same repository can belong to distinct projects without sharing their artifacts.
_Avoid_: Workspace, cache namespace

**Team Cache**:
A private cache shared by engineers and CI within one team.
_Avoid_: Project Cache, team store, team tier

**Public Cache**:
A globally readable cache containing artifacts produced only by Public Builds.
_Avoid_: Global Cache, global tier, public store

**Full build context**:
The source, dependencies, toolchain, configuration and environment declared by the native build system as inputs to a task. Personal Cache and Team Cache reuse outputs for that complete input identity.
_Avoid_: Commit-only cache identity, cached commit check

**Public Build**:
A Layer Cache-controlled build whose outputs may be published to the Public Cache. Engineers may request a Public Build but cannot publish its artifacts themselves.
_Avoid_: Global Build, public build service

**Local Build**:
A build executed on an engineer's machine.
_Avoid_: Local run

**Workspace**:
A short-lived repository checkout and execution environment, including a Git worktree or disposable VM. A Workspace may disappear while its reusable artifacts remain cached.
_Avoid_: Agent box, runner when it is not specifically a CI runner

**Transparent Cache**:
An integration that redirects an existing tool's cache protocol to Layer Cache without changing the workload definition.
_Avoid_: Automatic cache, invisible cache

**Recorded model response**:
A completed model result retained for one exact generation context and sample identity. Reusing it is recorded inference, rather than a new observation of the current model.
_Avoid_: Fresh model result, cached eval pass

**Generation context**:
The complete model, provider, request, tools, observations and sampling context that identifies a model invocation within an authorized project.
_Avoid_: Prompt-only identity, model-name-only identity

**Agent action replay**:
Executing previously recorded agent actions against a current application and checking its current assertions. The recording is reusable work, not a retained verdict that the application still passes.
_Avoid_: Cached E2E pass, skipping runtime assertions
