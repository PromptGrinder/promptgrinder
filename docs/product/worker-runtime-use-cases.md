# PromptGrinder Worker Runtime — Primary Use Cases

## Target User

An AI-native software engineer who works with one or more AI coding assistants
every day and wants deterministic, reviewable engineering workflows instead of
ad hoc prompting.

## UC-1: Start a Specialized Worker

As an engineer,

I want to start a predefined engineering worker,

so that I immediately get an AI with the correct identity, context, and
responsibilities.

Example:

```sh
promptgrinder worker start backend-sonar
```

This avoids manually explaining the role in every session.

## UC-2: Resume Previous Work

As an engineer,

I want a worker to resume where it previously stopped,

so that I do not have to explain previous progress.

The worker should restore:

- Current task
- Current branch
- Current worktree
- Current project
- Current state

## UC-3: Run Multiple Workers

As an engineer,

I want multiple engineering workers running simultaneously,

so that Android, backend, documentation, and review work can progress
independently.

Examples:

- Backend Feature Worker
- Android UI Worker
- Documentation Worker
- Reviewer

Each worker has its own identity and workspace.

## UC-4: Worker Isolation

As an engineer,

I want workers to stay within their responsibility,

so that they do not accidentally modify unrelated parts of the project.

For example, a backend worker cannot modify `mobile-android/`, and an Android
worker cannot modify `backend/`, unless explicitly allowed.

## UC-5: Project-Specific Workers

As an engineer,

I want each project to define its own workers,

so that PromptGrinder works across many repositories.

For example, FootyBadger defines:

- `backend-feature`
- `backend-sonar`
- `android-ui`
- `reviewer`

Another project may define completely different workers.

## UC-6: Runtime Independence

As an engineer,

I want to change the underlying AI runtime without changing worker definitions.

For example:

- Today: Codex CLI
- Tomorrow: Claude Code
- Future: another supported runtime

The worker remains identical.

## UC-7: Queue Engineering Tasks

As an engineer,

I want to submit engineering tasks,

so that PromptGrinder assigns them to the correct worker.

Example queue:

- Fix Sonar issue
- Add Android screen
- Update documentation
- Review PR

PromptGrinder determines which worker should execute each task.

## UC-8: Worker Status

As an engineer,

I want to inspect every worker,

so that I know what each AI is doing.

Example:

```text
backend-sonar

State:
Running

Current Task:
Sonar Group 12

Elapsed:
14 minutes

Branch:
backend-sonar-cleanup
```

## UC-9: Pause and Resume Workers

As an engineer,

I want to pause and later resume workers without losing their engineering
context.

## UC-10: Persistent Worker Identity

As an engineer,

I want workers to behave like long-lived team members instead of anonymous AI
sessions.

Each worker should always know:

- Who it is
- Which project it belongs to
- What it owns
- What it may edit
- What runtime it uses

This should not require repeated prompting.

## UC-11: Review Before Merge

As an engineer,

I want completed work to flow into a review stage before being merged.

The review worker should validate:

- Build
- Tests
- Style
- Contracts
- Completion criteria

These checks happen before human review.

## UC-12: Local-First Operation

As an engineer,

I want PromptGrinder to orchestrate workers entirely on my machine,

so that projects remain private and continue working without cloud
orchestration.

## UC-13: Extensible Runtime

As an engineer,

I want PromptGrinder to support additional runtimes without changing project
definitions.

Adding support for a new runtime should require implementing a runtime adapter
rather than redesigning PromptGrinder.

## UC-14: Discover Project Roles

As an engineer,

I want PromptGrinder to inspect repository evidence deterministically,

so that I can start with minimal project and role YAML without an AI call.

```sh
promptgrinder discover
```

Discovery writes only a new `.promptgrinder/` tree and refuses to overwrite
existing files. Generated roles are proposals, not automatically active named
workers.

## UC-15: Enhance Roles With Review

As an engineer,

I want an AI runtime to propose evidence-grounded improvements to discovered
roles,

so that quality gates, context, permissions, and behavior reflect the project
without silently replacing user configuration.

```sh
promptgrinder roles enhance
promptgrinder roles enhance --apply-selected recommendation-id
```

PromptGrinder collects repository evidence, requires structured recommendations,
renders every reason and confidence, and performs approved YAML merges itself.
The AI runtime never edits role files directly.

## UC-16: Trust Ordered Automation

As an engineer,

I want ordered workers to continue only after an explicit safe completion,

so that a clarification or partial result cannot be mistaken for success.

PromptGrinder requires `STATUS: PASS` and `NEXT_PROMPT_SAFE: yes`, preserves the
failure reason, enforces task path policy, and commits only worker-attributed
changes from a clean baseline.

## Success Criteria

PromptGrinder should allow me to think in terms of engineering team members
rather than AI prompts.

Instead of asking:

> How do I prompt Codex?

I should be able to say:

> Start the Backend Sonar Worker.

PromptGrinder is responsible for:

- Loading the worker
- Restoring its state
- Selecting its runtime
- Launching it
- Injecting its identity
- Assigning work

The AI runtime is responsible only for executing engineering tasks.
