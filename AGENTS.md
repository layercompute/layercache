# LayerCache work

Read [TODOs.md](TODOs.md) before starting a feature, investigating a regression,
or changing priorities. It owns outstanding work and acceptance targets.
Read [CONTEXT.md](CONTEXT.md) when naming cache scopes or recording new concepts.
Before optimizing CI/CD, build/test reuse or runner allocation, read the
[agent CI playbook](docs/ci-optimization/agent-playbook.md).

For a new request, update the relevant TODO with its user outcome, owner,
evidence, and completion target. Implement the smallest useful slice through an
existing native client or permissively licensed OSS seam. Record unsupported
behavior explicitly. After validation or landing, update the TODO status and
link the exact source and evidence before reporting completion.

Review priorities when new evidence changes expected savings, when a milestone
lands, and at the first work session each week. Follow
[the roadmap review](docs/roadmap-review.md). Keep planned work in TODOs rather
than creating a second GitHub issue backlog.

Phone workflow and release changes belong to the active Phone CI owner.
Coordinate through the relevant PR; this repository owns cache storage,
protocols, clients, and qualification. Preserve unrelated dirty worktrees and
operator configuration. Source qualification, CLI installation, and gateway or
cloud deployment are distinct outcomes.

Keep Personal, Team/org, and trusted-publisher Public access distinct. Private
caches retain the native client's complete declared context. Required pipeline
handoffs use retained artifacts; a cache acknowledgement alone establishes no
future retention guarantee. LLM replay is recorded inference, and fresh model
evaluations explicitly bypass it. Product examples keep credentials external.
