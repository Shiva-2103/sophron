# Workflow

## Branches

| Branch | For |
|---|---|
| `main` | Always works. Changes only through pull requests. |
| `plan` | Planning docs |
| `phase-N/name` | One task, for example `phase-1/kind-lab` |
| `fix/name` | Bug fixes |

One task = one branch = one pull request.

## Commit messages

Format: `type(scope): what changed`

| Type | Use for | Example |
|---|---|---|
| `feat` | New feature | `feat(rules): add gpu-idle detector` |
| `fix` | Bug fix | `fix(collector): handle missing metrics` |
| `docs` | Docs | `docs: add phase 1 learning note` |
| `chore` | Tools, CI | `chore(ci): cache go modules` |
| `test` | Tests | `test(cost): node-fit cases` |
| `refactor` | Cleanup, same behavior | `refactor(store): split queries` |

## Pull requests

Every PR answers four things:

1. What changed
2. Why
3. How to test it
4. Learning note updated?

Merge with squash, so each PR is one commit on `main`.

## Issues

- One issue per task
- Labels: phase (`phase-1`) and type (`feature`, `bug`, `docs`)
- Board: Backlog, This phase, In progress, In review, Done

## Releases

- Versions like `v0.1.0`. Before `v1.0.0`, things can change.
- A release is a git tag, images, the Helm chart and a changelog.

## Learning notes

After each phase, add `docs/learning/NN-phase-N-name.md` covering:

1. What we built
2. How
3. Why this way
4. How it fits Sophron
5. A real project doing the same
6. Interview questions it prepares you for

## Diagrams

Diagram images live in `docs/assets/`. Their source is `docs/assets/src/diagrams.html`, drawn in Poppins. To redraw them after editing:

```bash
pip install playwright && playwright install chromium
python3 docs/assets/src/render.py
```
