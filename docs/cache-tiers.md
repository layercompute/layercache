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
IDs are denied. Required PR Turbo and BuildKit jobs retain the existing read-only
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

Turborepo owns task hashing and execution. BuildKit owns its graph and native
cache export. Xcode's LLVM client owns compilation keys and CAS graph traversal.
Layer Cache owns authorization, compatible scopes, bounded retention, durable
publication and verified Public Cache reads.

Future Bazel and Nx work must use their native protocols and existing OSS
clients, with real-client acceptance for a cold upload, a fresh checkout restore,
changed-input misses and private-project isolation. Choose permissively licensed
engines where a service already exists; do not duplicate their scheduling or
fingerprinting merely to put Layer Cache's name on it.
