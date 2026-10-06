---
name: handoff
description: Compact the current conversation into a handoff document for another agent to pick up.
argument-hint: "What will the next session be used for?"
---

Write a handoff document summarising the current conversation so a fresh agent
can continue the work.

## Save location

Save to `handoff/` under the current project root directory.

- If `handoff/` does not exist, create it.
- Filename format: `YYYY-MM-DD-<short-slug>.md` (e.g. `2026-06-15-db-refactor.md`).
- If a file with the same name already exists, append a numeric suffix:
  `-2`, `-3`, etc.

## Content rules

- Include a "suggested skills" section in the document, which suggests skills
  that the agent should invoke.
- Do not duplicate content already captured in other artifacts (PRDs, plans,
  ADRs, issues, commits, diffs). Reference them by path or URL instead.
- Redact any sensitive information, such as API keys, passwords, or personally
  identifiable information.
- If the user passed arguments, treat them as a description of what the next
  session will focus on and tailor the doc accordingly.

## Resuming in a new session

Add the following note at the bottom of every handoff document:

```markdown
---
> To resume: read this file in your new session, then follow the suggested skills.
```
