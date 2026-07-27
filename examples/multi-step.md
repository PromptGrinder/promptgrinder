# Multi-step workflow

Use a run folder when later tasks depend on earlier changes. PromptGrinder
executes top-level files with a two-digit prefix in filename order.

Create a local workflow:

```text
tasks/
├── 10-implement.md
├── 20-test.md
└── 30-review.md
```

`10-implement.md`:

```md
# Implement

Make the smallest code change that satisfies the requested behavior.
Add focused tests and report the files changed.
```

`20-test.md`:

```md
# Test

Review the preceding implementation, run the relevant tests, and fix only
failures caused by that implementation.
```

`30-review.md`:

```md
# Review

Review the completed change for correctness, regression risk, and unnecessary
scope. Run final focused verification and summarize the result.
```

Run and inspect the workflow:

```sh
promptgrinder run-folder tasks/ --repo . --commit-each
promptgrinder sequence current
```

Each successful step receives the preceding completion report and is committed
before the next step begins. A failed step stops the sequence.
