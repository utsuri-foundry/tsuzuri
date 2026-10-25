# Working on Tsuzuri

Read README.md, CONTRIBUTING.md and the selected issue/work item before editing.
Use ordinary Git branches and sibling worktrees; keep canonical `main` clean.
Record the owner and bounded scope before implementation. Do not claim tasks with
Tsuzuri to develop this repository or alter another operator's worktree.

Preserve existing task identities, coordination refs, receipts and unrelated
changes. Where retained work-item records exist, record verification and
completion there; never delete or reopen terminal IDs. History rewriting and
repository publication require explicit maintainer authorization.

Keep the library and CLI independent of any coordinator service. Preserve
protocol formats, conditional writes, exact-worker recovery and canonical
completion. Read docs/integration.md before changing ownership behavior. Do not
weaken validation or copy defects to satisfy a compatibility test.

Use bounded cancellable native Git calls, structured arguments and safe
credential handling. Keep parsing, effects and rendering distinct. Update
architecture.json when source ownership changes; do not split packages for
cosmetic directory symmetry. Keep useful API and correctness comments.

Run make verify and relevant interoperability checks; record skipped proof honestly.
Use disposable repositories for tests, never production claims or credentials.
Keep changes reviewable and focused; do not add attribution to commits.
Independent review is required before merge. A work assignment alone does not
authorize pushing, merging, publishing, installing or changing visibility; honor
explicit session authorization without requesting it again.
