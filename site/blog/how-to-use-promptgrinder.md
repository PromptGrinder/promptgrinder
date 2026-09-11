---
layout: default
title: From feature idea to a safe AI delivery train | PromptGrinder
description: A practical guide to turning a feature idea into bounded, reviewable AI-assisted engineering work with PromptGrinder.
---

<p class="eyebrow">A practical guide</p>

# From feature idea to a safe AI delivery train

<p class="lede">AI coding agents can move quickly. PromptGrinder makes that speed reviewable: turn a feature into bounded work orders with explicit ownership, Git checkpoints, truthful failure evidence, and safe recovery.</p>

The objective is not to ask an agent to “build the feature.” It is to create an engineering workflow that remains understandable when it succeeds, when it fails, and when somebody else has to review it.

## Start with a specification, not a giant prompt

A feature idea is a useful beginning, but it is not yet an implementation plan. Before workers edit code, write down the decisions they would otherwise have to guess:

- the user-visible behavior and non-goals;
- required data and API contracts;
- platform boundaries and compatibility expectations;
- validation evidence required for completion;
- hard prerequisites such as authoritative data, credentials, legal approval, or infrastructure.

Keep this durable context in a shared file, conventionally `00-specification.pg`. It is context for the train, not normally a worker itself. Each implementation slice receives the small, task-specific part of the job it needs.

That is the first reason to slice: a large feature needs a broad shared understanding, but an individual worker should not have to keep every unrelated detail in context.

## Let the repository define the starting boundaries

Run PromptGrinder discovery before writing a major train:

    promptgrinder discover
    promptgrinder roles enhance
    promptgrinder roles review latest
    promptgrinder roles apply latest --safe

Discovery records the project’s technical shape and creates a reviewable role baseline. Role enhancement gathers bounded evidence from the repository, build, CI, and existing conventions. It produces a proposal; it does not silently broaden permissions.

Inspect the project’s own instructions at the same time: contributor guidance, local `SKILL.md` files, build workflows, deployment conventions, and prior feature handoffs. Skills give workers repository-specific knowledge. Roles decide what they are responsible for. Neither is a blanket permission to modify anything.

## Slice work into independently verifiable outcomes

For a cross-stack feature, a good train might have these outcomes:

1. Define the persisted model.
2. Compose the backend read model.
3. Publish the API contract.
4. Map the Android contract.
5. Implement the UI state and screen.
6. Run cross-stack verification and write the final handoff.

Each slice should have one clear outcome, a dependency position, a narrow write scope, and concrete validation. This creates smaller contexts, smaller diffs, focused reviews, and lower cost. More importantly, it creates rollback points: successful slices can become durable Git checkpoints before the next worker starts.

## Make ownership enforceable

Use frontmatter to state what a worker owns and what it must not touch:

    ---
    id: season-journey-api
    type: implement
    role: backend-feature
    depends_on:
      - journey-composition
    allowed_paths:
      - backend/src/main/**
      - backend/src/test/**
      - backend/openapi/**
      - docs/features/FB-XXX/handoffs/api-contract.md
    forbidden_paths:
      - mobile-android/**
      - infrastructure/**
    validation:
      - cd backend && ./mvnw -Dit.test=OpenApiContractIntegrationTest verify
    ---

`allowed_paths` and `forbidden_paths` are enforced policy, not a suggestion. They stop a documentation worker from changing production code and stop an Android worker from silently redesigning backend behavior.

For sequential work, ownership is often healthiest at a module boundary. A backend slice that owns `backend/**` is usually better than a fragile list of five source files. Tight file-level scopes become valuable when parallel lanes need to avoid merge contention.

## Match roles and models to the work

PromptGrinder separates three questions that are easy to mix together:

- What is the worker responsible for?
- What may it change?
- Which model should do that work?

Roles express responsibility and outer repository boundaries. Slices narrow that role to a concrete outcome. Model policy then selects a model that fits the work and the budget. A documentation or focused test task does not necessarily need the same model as a migration, cross-stack design decision, or final verification.

That is cost control with an engineering reason behind it: smaller contexts, fewer repeated investigations, and model capability matched to risk.

## Use dependencies by default; parallelize only when ownership is real

`depends_on` is a prerequisite, not a preference. If Android consumes an API contract, the Android slice depends on the backend API slice. By default, `run-folder` executes that graph sequentially.

Parallel worktrees are for genuinely independent lanes. For example, an Android privacy remediation may have separate lanes for location consent, account-deletion storage, and release-manifest hardening. Each lane needs its own non-overlapping write scope, fresh context, independent validation, and deterministic integration priority.

    promptgrinder run-folder docs/features/FB-XXX \
      --repo . \
      --parallel-worktrees \
      --fresh \
      --checkpoint \
      --commit-each \
      --require-clean-git

PromptGrinder uses isolated worktrees and integrates successful lanes into the feature branch in priority order. The foreground view shows multiple active lanes, waiting dependencies, worker metadata, integration commits, and a final Git subway map.

Parallelism is not a way to make every feature faster. It is a way to reduce elapsed time where the codebase gives you real, safe independence.

## Run the ordinary train conservatively

For most features, start with the checkpointable sequential command:

    promptgrinder run-folder docs/features/FB-XXX \
      --repo . \
      --checkpoint \
      --commit-each \
      --require-clean-git

Before a costly run, validate the folder and the local environment:

    promptgrinder validate-folder docs/features/FB-XXX --repo .
    promptgrinder doctor --repo . --terminal headless
    command -v promptgrinder
    promptgrinder --version

PromptGrinder RC.6.2 uses capability-based Codex compatibility. Known versions are qualified; newer versions can run provisionally when they pass a safe adapter probe. That avoids blocking a train merely because a CLI version number changed, while still refusing to launch workers if PromptGrinder’s required command contract is unavailable.

## What the result looks like in Git

PromptGrinder does not leave successful work trapped in an agent transcript.
With `--checkpoint --commit-each`, each completed slice becomes a focused,
reviewable commit on the feature branch.

The following is a fictionalized example based on the shape of a real
PromptGrinder checkpoint train. Commit IDs and feature names are deliberately
invented, but the ordered `PromptGrinder: complete …` history is the intended
result:

    $ git log --oneline --decorate feature/reviewable-role-setup
    e91a7c4 (HEAD -> feature/reviewable-role-setup) PromptGrinder: complete 60-final-verify-role-reviews.pg
    a60b12e PromptGrinder: complete 40-implement-interactive-role-review.pg
    79d4fa1 PromptGrinder: complete 30-store-role-review-cli.pg
    4c8e2b9 PromptGrinder: complete 20-refine-role-recommendations.pg
    b17fd63 PromptGrinder: complete 10-create-role-review-domain.pg
    0de4c8a docs: add reviewable role-setup specification and train

That history tells a reviewer more than “an agent made a change.” It shows a
sequence: the domain was established, refinement was added, the CLI was
connected, the interactive surface was implemented, and the final verification
completed. A failure after the third checkpoint does not erase the first three
outcomes or force the team to reconstruct them from conversation history.

The commit is created by PromptGrinder, not by the worker. Workers should not
be instructed to run `git add` or `git commit` when the supervisor owns
`--commit-each`; that keeps the commit boundary aligned with path-policy and
completion evidence.

## Recover from failure by fixing the cause

The useful recovery unit is the last safe slice, not a giant agent conversation. With `--checkpoint` and `--commit-each`, a successful slice records evidence and becomes a focused commit before the next slice runs.

Inspect a train from any terminal:

    promptgrinder sequence list
    promptgrinder sequence <sequence-id>

After correcting the actual cause, resume the same compatible train:

    promptgrinder run-folder docs/features/FB-XXX \
      --repo . \
      --checkpoint \
      --commit-each \
      --require-clean-git \
      --resume

The response depends on the failure category.

### Product or test failure

Do not endlessly retry a red test. Inspect the evidence, repair the real fixture, mock, contract, or implementation defect, validate it, and then resume. The goal is a clean causal fix, not a green status by repetition.

### Recoverable worker interruption

A worker can be interrupted after writing only its own declared output. When PromptGrinder can prove that the partial changes are scoped, it preserves them as an inspectable recovery artifact, restores the clean baseline, and retries only the failed slice. It does not auto-commit partial work, delete user files, stash unrelated changes, or broadly reset the repository.

If anything is ambiguous or outside policy, PromptGrinder stops and tells you what to inspect. Protecting unrelated user work is more important than forcing a retry.

### Missing toolchain capability

An Android worktree does not inherit ignored `local.properties`. Make the Android SDK requirement explicit and export it before the train starts:

    export ANDROID_HOME=/path/to/android/sdk
    export ANDROID_SDK_ROOT="$ANDROID_HOME"

Then declare the requirement in Android slices so preflight fails before a worker or worktree is created:

    required_environment:
      any_of:
        - ANDROID_HOME
        - ANDROID_SDK_ROOT

Sandbox permissions and toolchain setup are different concerns. A broader sandbox can allow local access; it does not configure an SDK.

### A completed audit that blocks product work

Some slices are investigations rather than implementations. A hard-gate audit may correctly establish that a prerequisite is absent. For that explicit case, declare `gate_outcome: BLOCKED`. If the worker returns `STATUS: BLOCKED` and `NEXT_PROMPT_SAFE: no`, PromptGrinder checkpoints the permitted audit evidence, marks the sequence product-blocked, and does not launch dependent implementation slices.

The audit succeeded. The product work is blocked. Those are importantly different outcomes.

## Finish with verification and a handoff

The last slice should verify the feature across its integration boundaries, update documentation, capture evidence, and state any remaining external limitation honestly. It should not casually expand product behavior simply because it is the last worker.

When a parallel train integrates cleanly, PromptGrinder tells you that the feature branch is ready for review and gives the natural next step:

    gh pr create --head <feature-branch>

PromptGrinder coordinates bounded AI work. Git preserves reviewable evidence. People still decide whether the integrated feature is ready to merge.

## The point is disciplined speed

PromptGrinder does not remove engineering judgment. It protects it at the moments where it matters: defining the feature, setting ownership boundaries, choosing what can safely run in parallel, interpreting a failure, and reviewing the integrated result.

That is how AI-assisted development becomes faster without becoming careless.
