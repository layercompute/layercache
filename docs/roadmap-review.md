# Reviewing LayerCache's roadmap

[TODOs.md](../TODOs.md) is the backlog and priority owner. Launch documents
record delivery and deployment evidence; protocol documents record contracts.
GitHub issues #33 and #34 are migrated references, not a second active backlog.

Review at each new feature request, material cost/reliability finding, completed
milestone, and the first work session each week. This is a repository workflow;
it does not create an unattended production deployment or background scheduler.

1. Read TODOs, the relevant contract, and current implementation. For Phone
   work, read its live CI task and relevant PR before assigning overlapping work.
   Record exact producing source, toolchain, scope and evidence date.
2. Rank concrete user outcomes by repeated expensive work avoided, whole-job
   time and runner/inference cost after setup/transfer, reliability, engineering
   effort and operating burden. Keep unknown savings unknown. A cache-hit count
   alone does not establish saved compute or cost.
3. Put correctness and reliable required handoffs before adoption. Preserve
   Personal, Team/org and trusted-publisher Public visibility. Distinguish
   source qualification, installed client, application adoption and live service.
4. Find the native OSS interface first. Inspect its pinned source/license and
   real-client behavior; use its hashes, model/device interfaces and executor.
   Define a narrow acceptance test through that interface before building.
5. Update one TODO with owner, state, acceptance target, evidence and next
   action. Keep implementation proposals in the corresponding design/contract
   document. Revise priorities when evidence warrants it instead of appending
   every feature at equal priority.
6. Before reporting completion, check the producing source and required
   acceptance. Update TODOs and cross-links in the same change. Capture
   limitations such as protocol support, unmeasured application savings or
   undeployed services. Close migrated GitHub tracking only after its replacement
   is merged; state explicitly that migration does not complete the feature.

LLM work has two separate measures: recorded replay avoids calls for an exact
input identity; fresh benchmarks measure the current model. E2E replay still
executes actions and validates the current app. Track recorded tokens as
historical usage and fresh calls as current inference. Version model/provider
defaults, sample corpus, prompts/tools, fixtures and relevant observations.

Keep private traces, protected credentials and account billing captures outside
published research. Cite public primary sources and sanitized evidence sufficient
to explain a decision. Preserve an auditable history in Git without maintaining
multiple conflicting roadmaps.
