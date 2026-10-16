# Trusted release producer boundary

Checked 2026-10-16. This is a proposed alternative for release-triggered writers,
not implemented writer authorization. Current release-triggered jobs retain
read-only access.

The active [Phone #317](https://github.com/ziahamza/phone-app/pull/317) candidate
at `17b6fa7c64fbfad433a7991dd647bb659c2b76c4` avoids a policy expansion. It adds
a path-filtered actual main-push Kotlin/Java producer on two vCPUs, with a graph
guard against C++ compilation and APK packaging. PR, manual and `workflow_run` jobs remain
readers. Local qualification has passed; hosted checks and a writer/fresh-reader
pair are pending. Include the added writer's compute in net cost measurement.
The alternative below is needed only if full release publication becomes a
requirement. It must not block qualifying the existing push-write policy.

A release workflow triggered by `workflow_run` may use the default branch's
workflow definition while checking out a different source revision. Its default
ref and OIDC identity do not establish that the checked-out source is trusted.
GitHub warns that untrusted code under this trigger can poison caches.
[Event contract](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_run).

Any future release writer-policy slice should authorize one immutable reusable
build job per project and native integration. Match the exact caller workflow/default ref,
`job_workflow_ref` and `job_workflow_sha`; retain current repository, membership,
profile and expiry checks. GitHub signs the called workflow's identity separately
from caller claims.
[OIDC claims](https://docs.github.com/en/actions/reference/security/oidc#custom-claims-provided-by-github).

The reusable job must validate the successful same-repository default-branch
upstream run before executing checkout code. It must derive and verify the exact
application SHA, perform the existing native build itself, and keep cache writer
credentials inside that job. Returning a writer token to an ordinary caller job
would defeat this boundary. Pin the reusable call to a full commit SHA.
[Reusable workflow calls](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows#calling-a-reusable-workflow).

The inspected REST run contract exposes source, event, result, attempt and
repository identity, but no documented current-run-to-triggering-run link.
`referenced_workflows` describes reusable workflows; retry links describe the
same run's attempts. A caller-supplied successful run ID therefore cannot prove
what triggered or what source executed in that caller.
[Official REST contract](https://github.com/github/rest-api-description/blob/51a4657888cc1db93d8bd68086d117909fd8221c/descriptions/api.github.com/api.github.com.json).

Qualification must cover mismatched workflow/source/attempt, PR and fork inputs,
revocation, outages and token export attempts, then one existing producer and an
independent empty consumer with exact outputs. This policy remains planned;
source and deployment proof and the application owner's workflow adoption are
required. Reuse the existing full build rather than duplicating C++ compilation
and packaging merely to populate cache.
