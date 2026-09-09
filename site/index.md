---
layout: default
title: PromptGrinder | AI delivery trains you can trust
description: PromptGrinder turns AI coding prompts into bounded, reviewable engineering workflows.
---

<p class="eyebrow">AI-assisted engineering, with evidence</p>

# Build features with agents. Keep engineering in control.

<p class="lede">PromptGrinder turns AI coding prompts into deterministic, reviewable delivery trains: smaller slices, explicit ownership, durable Git checkpoints, safe recovery, and isolated parallel worktrees.</p>

<div class="callout">
  <strong>New here?</strong> Read <a href="{{ '/blog/how-to-use-promptgrinder/' | relative_url }}">From feature idea to a safe AI delivery train</a>.
</div>

## Start with the repository

Install PromptGrinder, then let it inspect the project before defining work:

    promptgrinder discover
    promptgrinder roles enhance
    promptgrinder roles review latest

The resulting project and role evidence gives workers useful context while keeping their ownership boundaries explicit.

## Then run a train

    promptgrinder run-folder docs/features/FB-XXX \
      --repo . \
      --checkpoint \
      --commit-each \
      --require-clean-git \
      --detach=false

Read the [installation guide](https://github.com/PromptGrinder/promptgrinder#quick-start), explore the [source repository](https://github.com/PromptGrinder/promptgrinder), or start with the [full guide]({{ '/blog/how-to-use-promptgrinder/' | relative_url }}).
