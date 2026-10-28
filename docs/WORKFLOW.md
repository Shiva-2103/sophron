# How we work

Simple rules so the history stays clean and every change is easy to review and explain in an interview.

## Branches

| Branch | Purpose |
|---|---|
| `main` | Always works. Protected: changes only through pull requests with green CI. |
| `plan` | Planning docs (this phase). Merged into `main` via PR. |
| `phase-N/<short-name>` | Work for one task, e.g. `phase-1/kind-lab`, `phase-2/collector-informers` |
| `fix/<short-name>` | Bug fixes |

One branch = one task = one pull request. Small PRs are easier to review and to learn from.

## Commits: Conventional Commits

Format: `type(scope): what changed`

| Type | When | Example |
|---|---|---|
| `feat` | New feature | `feat(rules): add kv-cache-pressure detector` |
| `fix` | Bug fix | `fix(collector): handle missing gpu metrics` |
| `docs` | Documentation | `docs: add phase 0 learning note` |
| `chore` | Tooling, deps | `chore(ci): cache go modules` |
| `test` | Tests only | `test(cost): node-fit cases for 8B model` |
| `refactor` | Code change, same behavior | `refactor(store): split snapshot queries` |

Why: readable history, and later we can generate the changelog automatically from it.

## Pull requests

Every PR description answers:
1. **What** changed
2. **Why**
3. **How to test it**
4. **Learning note** updated? (yes / not needed)

Merge with **squash** so each PR becomes one clean commit on `main`.

## Issues and the project board

- One issue per task, labelled with its phase (`phase-1`, `phase-2`, …) and type (`feature`, `bug`, `docs`).
- Board columns: **Backlog → This phase → In progress → In review → Done**.
- A phase is finished when every "done when" box in [ROADMAP.md](ROADMAP.md) is ticked.

## Releases

- Semantic versions: `v0.1.0`, `v0.1.1`, `v0.2.0`. Before `v1.0.0`, things may change.
- A release = a git tag + container images + Helm chart + changelog.

## Learning notes

At the end of every phase, add `docs/learning/NN-phase-N-<name>.md` with:
1. What we built
2. How we built it
3. Why we did it this way
4. How it works inside Sophron
5. A real-world project that does the same thing
6. Interview questions you can now answer
