# Phase 0: Repo and plan

## What we built

- A GitHub repo with an Apache 2.0 license and a Go `.gitignore`
- A `plan` branch with these docs
- Diagrams drawn in HTML, saved as images

No code yet. That's on purpose.

## How

| Step | Command |
|---|---|
| Create the repo | `gh repo create sophron --private --license apache-2.0 --gitignore Go --clone` |
| Make a branch | `git switch -c plan` |
| Save work | `git add` then `git commit -m "docs: ..."` |
| Upload the branch | `git push -u origin plan` |

`-u` links your local branch to GitHub, so next time plain `git push` works.

## Why

| Choice | Reason |
|---|---|
| Plan before code | Every phase has a goal and a finish line |
| Docs in the repo | They change with the code and get reviewed in PRs |
| Apache 2.0 | Safe for companies to use, includes patent protection, used by CNCF projects |
| Branches and PRs, even solo | Keeps `main` working, and the PR history shows your decisions |
| Short commit types (`docs:`, `feat:`) | Clean history, automatic changelogs later |
| Ignore `.env` and kubeconfigs | Secrets never reach GitHub |
| Diagrams as HTML source | Easy to edit, consistent look, rendered in Poppins |

## How it fits Sophron

- README: the front door for users and recruiters
- Architecture: the map every phase follows
- Roadmap: the map cut into bricks
- Workflow: how we work, every time
- Learning notes: your study guide

## Real projects that work this way

- Kubernetes writes a proposal (a KEP) before big changes
- K8sGPT and OpenCost keep a short README, a `docs/` folder, Go code and a Helm chart, all under Apache 2.0

## Interview questions you can now answer

- How do you set up a new open-source project?
- Apache 2.0 vs MIT vs AGPL?
- What's your branching strategy?
- What are Conventional Commits for?
- How do you keep secrets out of git?
