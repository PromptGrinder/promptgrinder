---
name: context-health
description: Detect context degradation during long investigations and stop safely before conclusions become unreliable.
---

# Context Health

Apply this skill to investigations, debugging sessions, architecture reviews,
and tasks involving many files, commands, hypotheses, or distinct subsystems.

## Objective

Prevent long-running investigations from continuing after the active context
has become fragmented, contradictory, repetitive, or too large to reason about
reliably.

## Context checkpoint

Run a context-health checkpoint:

- after 15 tool or shell operations;
- after reading 12 substantial files;
- after investigating 3 distinct hypotheses;
- whenever the task crosses into another subsystem;
- whenever earlier evidence must be repeatedly rediscovered;
- before proposing or implementing a fix after a long investigation.

## Continue only when all are true

Continue the current session only when you can state clearly:

1. The original problem.
2. The current leading hypothesis.
3. The evidence supporting it.
4. The evidence contradicting it.
5. The relevant files and components.
6. The next concrete investigation step.
7. Which conclusions are confirmed versus inferred.

## Context degradation indicators

Treat the context as degraded when two or more are true:

- Earlier findings are being repeated or rediscovered.
- File names, endpoints, entities, or hypotheses are being confused.
- The investigation has accumulated several abandoned hypotheses.
- Conclusions rely on details that can no longer be located precisely.
- New work is only weakly related to the original objective.
- The same files are repeatedly reopened to reconstruct state.
- The proposed fix is broader than the evidence supports.
- Test results or database findings are no longer clearly associated with the
  code revision that produced them.
- The investigation summary cannot be written confidently in under 800 words.
- There are unresolved contradictions in the evidence.
- The agent starts guessing rather than verifying.

## Required stop behaviour

When the context is degraded:

1. Stop investigating.
2. Do not modify production code.
3. Do not claim a root cause unless it is already proven.
4. Write a handoff document at:

   `docs/investigations/<yyyy-mm-dd>-<topic>-handoff.md`

5. Include:
   - original objective;
   - current observed behaviour;
   - confirmed findings;
   - rejected hypotheses and why;
   - unresolved hypotheses;
   - database queries already run and their results;
   - commands and tests already run;
   - relevant files and symbols;
   - branch and current commit;
   - uncommitted changes;
   - exact recommended next step;
   - a ready-to-paste prompt for a fresh Codex session.

6. End the current run with:

   `STOPPED: fresh context required`

## Investigation discipline

Maintain a compact investigation ledger throughout the task:

### Objective
One sentence.

### Confirmed facts
Only directly verified facts.

### Active hypothesis
At most one primary hypothesis and two secondary hypotheses.

### Rejected hypotheses
Include the evidence that rejected each one.

### Next verification
One concrete command, query, or code inspection.

Do not let the ledger exceed 1,000 words. Consolidate it rather than appending
indefinitely.

## Scope-change rule

A new issue discovered during the investigation must not silently expand the
task.

Record it under `Follow-up issues` and continue only when it is directly
required to prove or disprove the current root cause. Otherwise leave it for a
separate run.

## Implementation gate

Do not implement a fix unless:

- the failure path is traceable from entry point to incorrect outcome;
- the expected behaviour is explicit;
- the proposed change addresses the demonstrated cause;
- a regression test can be described;
- the context-health checkpoint passes.

If this gate fails, stop and create the handoff instead.
