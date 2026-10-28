# Learning note: Phase 0, repo and plan

## 1. What we built

- A public GitHub repo, `sophron`, with an **Apache-2.0 license**, a **README**, and a Go **`.gitignore`**
- A **`plan` branch** containing the architecture, roadmap and workflow docs
- A **draft pull request** from `plan` to `main`
- (Next) branch protection on `main` and a project board

No code yet, on purpose.

## 2. How we built it

With the GitHub CLI (`gh`) and `git`:
1. `gh repo create` made the repo with license and `.gitignore` in one command
2. `git switch -c plan` created the planning branch
3. We added docs as Markdown files and committed them with a Conventional Commit message
4. `git push -u origin plan` uploaded the branch; `-u` links local and remote so future `git push` needs no arguments
5. `gh pr create --draft` opened a pull request marked "not ready to merge yet"

## 3. Why we did it this way

| Decision | Why |
|---|---|
| **Plan before code** | Every later phase has a goal and a "done when" list, so we never wonder what to do next and we can't drift |
| **Docs live in the repo** ("docs as code") | Docs change together with code, are reviewed in PRs, and show your thinking to recruiters |
| **Apache-2.0** | Company-friendly, includes a patent grant, and is the license CNCF projects use, so companies can adopt Sophron without legal worries |
| **Work in a branch + PR, even alone** | Builds the habit real teams use, keeps `main` always working, and the PR history becomes a record of your decisions |
| **Mermaid diagrams** | GitHub renders them automatically; diagrams are text, so they're versioned and diffable |
| **Conventional Commits** | Clean history and automatic changelogs later |

## 4. How it fits into Sophron

- `README.md` is the **front door**: the first thing a user, contributor or recruiter reads.
- `docs/ARCHITECTURE.md` is the **map** every later phase builds against.
- `docs/ROADMAP.md` turns the map into **ordered bricks**; each phase becomes GitHub issues.
- `docs/WORKFLOW.md` keeps **how** we work consistent.
- `docs/learning/` becomes your **study guide and interview prep**.

## 5. Real-world example

- **Kubernetes** plans every significant change as a written proposal (a "KEP", Kubernetes Enhancement Proposal) before code is written.
- **K8sGPT** and **OpenCost** (both CNCF projects) keep the same structure: a README that explains the problem in a few lines, a `docs/` folder, Apache-2.0 license, Go code under `cmd/` and `pkg/` or `internal/`, and a Helm chart.
- Most large open-source projects require PRs with green CI before anything reaches `main`, exactly as we set up here.

## 6. Interview questions you can now answer

- "How do you structure a new open-source project?"
- "Why Apache-2.0 vs MIT vs AGPL?" (Apache adds an explicit patent grant; AGPL forces network services to share source, which some companies avoid.)
- "What's your branching strategy and why?"
- "What are Conventional Commits and what do they enable?"
- "How do you keep a project's documentation from going stale?"
