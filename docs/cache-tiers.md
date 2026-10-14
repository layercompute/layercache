# Cache tiers and build context

Layer Cache has three visibility tiers. Local Cache is the storage on a machine;
it is useful under every tier and has its own quota and eviction policy.

| Tier | Access | Publication |
| --- | --- | --- |
| Personal Cache | One stable user identity, that user's machines, and explicitly authorized CI | The owner and that owner's authorized builds |
| Team Cache | Authorized members and project-scoped CI in a team or organization | Project writers and administrators, with read-only PR policies where required |
| Public Cache | Verified consumers | Only Layer Cache's trusted build and collector path |

Personal and team projects preserve full build context by default. They use the
native tool's source, dependency, configuration and environment hashes, plus an
explicit compatible platform and toolchain profile. Layer Cache stores the
resulting artifacts without replacing that identity with a commit SHA or a
homegrown source hasher. A task may include private application outputs and
replayable logs. Each task still needs accurate input and output declarations;
Layer Cache cannot identify an environment variable the build tool ignores.

Full context does not mean copying a complete checkout or collecting secrets.
It means caching the task outputs for all declared inputs, including private
application code. Developers keep the same build and test commands. A compatible
cached task can satisfy that invocation; a changed input, incompatible profile,
failed result or missing artifact requires execution. Network and nondeterministic
suites need their own fresh policy. See [local-to-CI reuse](local-ci-reuse.md).

## Personal projects across machines

The cloud dashboard can create one Personal Cache for an approved beta creator.
Its `kind: personal` collection uses the existing project cache engine, storage
budgets and native protocols. Adding a project still requires GitHub repository
administrator access. Connect each machine to its project with the same GitHub
account using the dashboard's normal CLI command.

The owner is GitHub's stable numeric user ID. Personal collections reject
invitations and membership edits. Team membership, including membership in a
team that uses the same repository, does not grant access to the personal
project. Each project has distinct credentials, quota and artifact coordinates.
Existing databases retain their current shared teams as `kind: team`.

Personal GitHub Actions access requires both the provisioned numeric repository
identity and the owner's signed `actor_id` claim. Other actors and missing actor
IDs are denied. Required PR Turbo, Nx and BuildKit jobs retain the existing read-only
policy; a main push, manual run or scheduled run follows the existing writer
policy. This delegates access to the configured repository workflow initiated by
the owner, so that workflow and its code are inside the owner's trust boundary.
GitHub documents the identity in its [OIDC claims reference](https://docs.github.com/en/actions/reference/security/oidc).
The service rechecks the personal delegation when issued capabilities are used.

The current CLI connects one private project endpoint per configuration, with
optional verified Public Cache lookup. Personal and team projects can coexist in
the dashboard and use separate configurations. Combining several private
endpoints in one lookup chain remains future work and requires explicit
precedence and per-destination publication policy.

## Public publication stays separate

Personal and team cache writes never publish to Public Cache. Consumer tokens
cannot perform Public Cache publication. Only the separate trusted collector
can publish signed immutable outputs from Layer Cache-controlled builds using
approved repositories, recipes and pinned worker assets. Consumers verify
identity, signature, digest, expiry and revocation before reuse.

Public signatures authenticate the configured publisher. They do not prove that
the publisher or its build environment is safe. Public Builds retain their
separate worker qualification; the current CLI beta does not establish that
qualification or deploy the self-serve cloud.

## Native tools and OSS ownership

Turborepo and Nx own their task hashing, execution and archive packing. BuildKit
owns its graph and native cache export. Xcode's LLVM client owns compilation keys
and CAS graph traversal.
Layer Cache owns authorization, compatible scopes, bounded retention, durable
publication and verified Public Cache reads.

Owner-only Personal projects merged in
[#28](https://github.com/ziahamza/layercache/pull/28). Native Nx HTTP merged in
[#31](https://github.com/ziahamza/layercache/pull/31) for Local and direct
Personal/Team endpoints. It does not yet provide Local-to-private read-through,
durable Nx publication, confirmation or reporting. Complete Xcode graph retention
merged in [#30](https://github.com/ziahamza/layercache/pull/30) after its real
multiple-root native pressure gate passed. These implementations are included in
beta.4; they do not deploy a hosted service. See the
[verified release instructions](../scripts/README.md).

Complete that private-cache bridge and measure real Phone whole-job savings
before expanding adoption. Native Gradle HTTP for Phone Android comes next,
followed by an isolated, pinned `bazel-remote` sidecar after protocol qualification,
then sccache. Require real-client cold upload, fresh restore, changed-input
misses and private-project isolation. Reuse native protocols and permissively
licensed engines; keep their task hashing and scheduling. See the
[integration plan](oss-integration-plan.md) for the remaining work and the
[launch plan](launch-plan.md) for qualification status.
