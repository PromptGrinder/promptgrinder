# Shared-context workflow

Use shared context when several ordered tasks should run in one resumed Codex
session. This example operates only on a disposable repository.

Create and enter a temporary Git repository:

```sh
workdir="$(mktemp -d)"
cd "$workdir"
git init
git config user.name "PromptGrinder Example"
git config user.email "example@invalid"
printf '# Notes\n' > README.md
git add README.md
git commit -m "Initial fixture"
mkdir tasks
```

Create `tasks/10-add-note.md`:

```md
# Add a note

Append a section named `Status` to `README.md` with one sentence saying this is
a disposable shared-context example. Change no other files.
```

Create `tasks/20-review-note.md`:

```md
# Review the note

Review the `Status` section added to `README.md`. Correct it only if it is
missing, inaccurate, or unclear. Report the final heading and sentence.
```

Validate and preview the workflow:

```sh
promptgrinder validate tasks/10-add-note.md
promptgrinder validate tasks/20-review-note.md
promptgrinder run --shared-context --dry-run 'tasks/*.md'
```

Run it when the preview is correct:

```sh
promptgrinder run --shared-context 'tasks/*.md'
promptgrinder sequence current
git log --oneline --decorate
```

PromptGrinder runs the files in name order, resumes the same Codex session for
the second task, and commits each successful task. A failure stops the sequence.
Delete the temporary directory when finished.
